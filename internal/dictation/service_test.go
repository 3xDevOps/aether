package dictation

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type heldEngine struct{ release chan struct{} }

func (h heldEngine) Transcribe(ctx context.Context, _ []byte) (string, error) {
	select {
	case <-h.release:
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (heldEngine) Close() error { return nil }

// One clip transcribes at a time; four more wait, the next is refused as
// busy, and a waiter whose request ends gives its place up.
func TestQueueAdmitsOneAndQueuesFour(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := heldEngine{release: make(chan struct{})}
	svc := New(dir, engine)
	wav, err := os.ReadFile("testdata/speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	send := func(ctx context.Context, m string) {
		go func() {
			_, terr := svc.Transcribe(ctx, domain.MemberID(m), wav, nil)
			errs <- terr
		}()
	}
	send(t.Context(), "m0")
	waitFor(t, func() bool { return len(svc.slot) == 1 })
	cancelled, cancel := context.WithCancel(t.Context())
	send(cancelled, "m1")
	for i := 2; i <= 4; i++ {
		send(t.Context(), "m"+strconv.Itoa(i))
	}
	waitFor(t, func() bool { return svc.queuedNow() == 4 })
	_, err = svc.Transcribe(t.Context(), "m5", wav, nil)
	if perr, ok := err.(*protocol.Error); !ok || perr.Code != protocol.CodeUnavailable || perr.Message != "dictation is busy: 4 requests queued; try again" {
		t.Fatalf("fifth waiter = %v, want busy", err)
	}
	cancel()
	if err := <-errs; err == nil {
		t.Fatal("the cancelled waiter succeeded")
	}
	waitFor(t, func() bool { return svc.queuedNow() == 3 })
	send(t.Context(), "m5")
	waitFor(t, func() bool { return svc.queuedNow() == 4 })
	close(engine.release)
	for range 5 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// A waiter gives up after maxWait with the busy error, before a gateway
// would time out.
func TestQueueWaitIsBounded(t *testing.T) {
	old := maxWait
	maxWait = 50 * time.Millisecond
	defer func() { maxWait = old }()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := heldEngine{release: make(chan struct{})}
	defer close(engine.release)
	svc := New(dir, engine)
	wav, err := os.ReadFile("testdata/speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = svc.Transcribe(t.Context(), "m0", wav, nil) }()
	waitFor(t, func() bool { return len(svc.slot) == 1 })
	_, err = svc.Transcribe(t.Context(), "m1", wav, nil)
	if perr, ok := err.(*protocol.Error); !ok || perr.Code != protocol.CodeUnavailable ||
		perr.Message != "dictation is busy: waited 50ms and 0 requests are still queued; try again" {
		t.Fatalf("waiter = %v, want busy after maxWait", err)
	}
	if svc.queuedNow() != 0 {
		t.Fatalf("queued = %d after the waiter left", svc.queuedNow())
	}
}

func (s *Service) queuedNow() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queued
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
