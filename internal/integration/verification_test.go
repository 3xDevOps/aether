package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestBoundedOutputKeepsPrefixAndMarksTruncation(t *testing.T) {
	var output boundedOutput
	prefix := strings.Repeat("a", protocol.IntegrationMaxOutputBytes-7)
	if n, err := output.Write([]byte(prefix)); err != nil || n != len(prefix) {
		t.Fatalf("prefix write = (%d, %v)", n, err)
	}
	if n, err := output.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("overflow write = (%d, %v)", n, err)
	}
	if len(output.Bytes()) != protocol.IntegrationMaxOutputBytes {
		t.Fatalf("retained output length = %d", len(output.Bytes()))
	}
	if !output.Truncated() {
		t.Fatal("output truncation was not recorded")
	}
	if got := string(output.Bytes()[len(output.Bytes())-7:]); got != "0123456" {
		t.Fatalf("prefix boundary = %q", got)
	}
}

func TestVerificationFailureClassificationObservesTimeoutAndCancellation(t *testing.T) {
	deadlineCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyVerificationFailure(deadlineCtx, context.DeadlineExceeded); got != protocol.VerificationTimedOut {
		t.Fatalf("deadline status = %q", got)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyVerificationFailure(cancelCtx, context.Canceled); got != protocol.VerificationCancelled {
		t.Fatalf("cancel status = %q", got)
	}
	if got := classifyVerificationFailure(context.Background(), errors.New("command failed")); got != protocol.VerificationError {
		t.Fatalf("runtime status = %q", got)
	}
}

type verificationCleanupRuntime struct {
	runtime.Runtime
	findID  runtime.ID
	findErr error
	events  []string
}

func (r *verificationCleanupRuntime) Stop(context.Context, runtime.ID, time.Duration) error {
	r.events = append(r.events, "stop")
	return nil
}

func (r *verificationCleanupRuntime) Destroy(context.Context, runtime.ID) error {
	r.events = append(r.events, "destroy")
	return nil
}

func (r *verificationCleanupRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	r.events = append(r.events, "find")
	return r.findID, r.findErr
}

func TestCleanupVerificationRuntimeReleasesOnlyAfterAbsence(t *testing.T) {
	rt := &verificationCleanupRuntime{findErr: errors.New("runtime unavailable")}
	events := make([]string, 0, 4)
	svc := &Service{
		runtime: rt,
		releaseRuntime: func(context.Context, string) error {
			events = append(events, "release")
			return nil
		},
	}
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err == nil {
		t.Fatal("unknown runtime liveness returned nil")
	}
	if len(events) != 0 {
		t.Fatalf("release events after unknown liveness = %v, want none", events)
	}

	rt.findErr = runtime.ErrNotFound
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err != nil {
		t.Fatalf("cleanup after absent runtime: %v", err)
	}
	if len(events) != 1 || events[0] != "release" {
		t.Fatalf("release events after absent runtime = %v, want [release]", events)
	}

	events = events[:0]
	rt.events = nil
	rt.findErr = nil
	rt.findID = "container-id"
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err != nil {
		t.Fatalf("cleanup after found runtime: %v", err)
	}
	if got, want := append(rt.events, events...), []string{"find", "stop", "destroy", "release"}; len(got) != len(want) {
		t.Fatalf("cleanup event count = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("cleanup events = %v, want %v", got, want)
			}
		}
	}
}
