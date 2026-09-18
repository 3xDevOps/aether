package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
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
