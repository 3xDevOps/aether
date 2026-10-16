package assistant

import (
	"context"
	"errors"
	"os"
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

func TestApproverPassesWithoutACardOnlyWhileAutoApprovalIsOn(t *testing.T) {
	svc := New(Config{Dir: t.TempDir(), Store: newMemStore()})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	approve := svc.approver(m)
	c := protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run r1"}
	transcript := func() string {
		b, _ := os.ReadFile(svc.logPath("m1"))
		return string(b)
	}
	answer := func(yes bool) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- approve(t.Context(), c) }()
		deadline := time.Now().Add(5 * time.Second)
		for len(m.confirms.snapshot()) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("no card was shown while auto-approval is off")
			}
			time.Sleep(time.Millisecond)
		}
		if err := m.confirms.decide(m.confirms.snapshot()[0].ID, yes); err != nil {
			t.Fatal(err)
		}
		return wait(t, done)
	}

	if err := answer(true); err != nil {
		t.Fatalf("approved card = %v", err)
	}
	if !strings.Contains(transcript(), `"title":"Approved: Stop run r1"`) {
		t.Fatalf("transcript = %q, want the approval", transcript())
	}

	if got, err := svc.SetAutoApprove(t.Context(), "m1", true); err != nil || !got.AutoApprove {
		t.Fatalf("SetAutoApprove(true) = %+v, %v", got, err)
	}
	if err := approve(t.Context(), c); err != nil {
		t.Fatalf("auto-approved call = %v", err)
	}
	if n := len(m.confirms.snapshot()); n != 0 {
		t.Fatalf("%d cards shown while auto-approval is on", n)
	}
	log := transcript()
	if !strings.Contains(log, `"title":"Auto-approval turned on"`) || !strings.Contains(log, `"title":"Auto-approved: Stop run r1"`) ||
		!strings.Contains(log, "run.kill passed without a card: auto-approval is on.") {
		t.Fatalf("transcript = %q, want the change and the auto-approved call", log)
	}

	if _, err := svc.SetAutoApprove(t.Context(), "m1", false); err != nil {
		t.Fatal(err)
	}
	if err := answer(false); !errors.Is(err, errDeclined) {
		t.Fatalf("declined card after turning auto-approval off = %v", err)
	}
	if strings.Count(transcript(), "Auto-approved:") != 1 || !strings.Contains(transcript(), "Auto-approval turned off") {
		t.Fatalf("transcript = %q", transcript())
	}
}

func TestApproverRefusesACallItCannotRecord(t *testing.T) {
	st := newMemStore()
	st.auto["m1"] = true
	svc := New(Config{Dir: t.TempDir(), Store: st})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	m.removing = true
	err := svc.approver(m)(t.Context(), protocol.AssistantConfirmation{Method: protocol.MethodEnvSave, Summary: "Save"})
	if !errors.Is(err, ErrRemoving) || !strings.Contains(err.Error(), "record the approval") {
		t.Fatalf("auto-approved call that cannot be recorded = %v, want refused", err)
	}
}

func TestAutoApprovalRefusesACallWhoseSessionEndedWhileItWaited(t *testing.T) {
	st := newMemStore()
	st.auto["m1"] = true
	svc := New(Config{Dir: t.TempDir(), Store: st})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	ctx, cancel := context.WithCancel(t.Context())
	m.op.Lock()
	done := make(chan error, 1)
	go func() {
		done <- svc.approver(m)(ctx, protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run r1"})
	}()
	for {
		st.mu.Lock()
		read := st.autoReads > 0
		st.mu.Unlock()
		if read {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the call did not wait for the reset holding the lock: %v", err)
	default:
	}
	cancel()
	m.op.Unlock()
	if err := wait(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("call whose session ended during a reset = %v, want refused", err)
	}
	if b, _ := os.ReadFile(svc.logPath("m1")); strings.Contains(string(b), "Auto-approved") {
		t.Fatalf("transcript = %q, want no approval", b)
	}
}
