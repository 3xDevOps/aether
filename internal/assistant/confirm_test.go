package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// ask starts a confirmation and returns once the registry lists it.
func ask(t *testing.T, r *confirmations, ctx context.Context) (protocol.AssistantConfirmation, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- r.confirm(ctx, protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run r1"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if set := r.snapshot(); len(set) == 1 {
			return set[0], done
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the confirmation was never listed")
	return protocol.AssistantConfirmation{}, nil
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("confirm did not return")
		return nil
	}
}

func TestConfirmationFilledAndApproved(t *testing.T) {
	r := newConfirmations(func() {})
	c, done := ask(t, r, t.Context())
	if c.ID == "" || c.CreatedAt.IsZero() || c.ExpiresAt.Sub(c.CreatedAt) != confirmTTL {
		t.Fatalf("confirmation = %+v, want an id and a %s expiry", c, confirmTTL)
	}
	if err := r.decide(c.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("approved confirmation returned %v", err)
	}
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("pending after approval = %+v", got)
	}
	if err := r.decide(c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("second answer = %v, want ErrNoConfirmation", err)
	}
}

func TestConfirmationDeclined(t *testing.T) {
	r := newConfirmations(func() {})
	c, done := ask(t, r, t.Context())
	if err := r.decide(c.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("declined confirmation returned %v, want a decline", err)
	}
}

func TestConfirmationExpires(t *testing.T) {
	r := newConfirmations(func() {})
	r.ttl = 20 * time.Millisecond
	c, done := ask(t, r, t.Context())
	if err := wait(t, done); !errors.Is(err, errExpired) {
		t.Fatalf("expired confirmation returned %v", err)
	}
	if err := r.decide(c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("answer after expiry = %v, want ErrNoConfirmation", err)
	}
}

func TestConfirmationSessionEndAndContext(t *testing.T) {
	r := newConfirmations(func() {})
	_, done := ask(t, r, t.Context())
	r.cancelAll()
	if err := wait(t, done); !errors.Is(err, errSessionEnded) {
		t.Fatalf("confirmation after a reset returned %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, done = ask(t, r, ctx)
	cancel()
	if err := wait(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("confirmation with a cancelled context returned %v", err)
	}
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("pending after both = %+v", got)
	}
}
