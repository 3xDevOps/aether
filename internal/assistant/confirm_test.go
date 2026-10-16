package assistant

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
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

// answerCard makes the call c and answers the card it shows.
func answerCard(t *testing.T, svc *Service, m *member, c protocol.AssistantConfirmation, yes bool) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- svc.approver(m)(t.Context(), c) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(m.confirms.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no card was shown")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.confirms.decide(m.confirms.snapshot()[0].ID, yes); err != nil {
		t.Fatal(err)
	}
	return wait(t, done)
}

func TestApproverPassesWithoutACardOnlyWhileAutoApprovalIsOn(t *testing.T) {
	svc := New(Config{Dir: t.TempDir(), Store: newMemStore()})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	approve := svc.approver(m)
	c := protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run r1"}
	on, off := true, false
	transcript := func() string {
		b, _ := os.ReadFile(svc.logPath("m1"))
		return string(b)
	}
	answer := func(yes bool) error { return answerCard(t, svc, m, c, yes) }

	if err := answer(true); err != nil {
		t.Fatalf("approved card = %v", err)
	}
	if !strings.Contains(transcript(), `"title":"Approved: Stop run r1"`) {
		t.Fatalf("transcript = %q, want the approval", transcript())
	}

	if got, err := svc.SetSettings(t.Context(), "m1", protocol.AssistantSettingsParams{AutoApprove: &on}); err != nil || !got.AutoApprove {
		t.Fatalf("turning auto-approval on = %+v, %v", got, err)
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

	if _, err := svc.SetSettings(t.Context(), "m1", protocol.AssistantSettingsParams{AutoApprove: &off}); err != nil {
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
	st.settings["m1"] = store.AssistantSettings{AutoApprove: true}
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
	st.settings["m1"] = store.AssistantSettings{AutoApprove: true}
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
		read := st.settingsReads > 0
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

func TestAutoApprovalStillAsksForAnotherMembersRunAndUnlistedMethods(t *testing.T) {
	st := newMemStore()
	st.settings["m1"] = store.AssistantSettings{AutoApprove: true}
	svc := New(Config{Dir: t.TempDir(), Store: st})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	for _, c := range []protocol.AssistantConfirmation{
		{Method: protocol.MethodRunInject, Summary: `Send to run r1, owned by Grace: "hi"`, OtherOwner: "Grace"},
		{Method: protocol.MethodRunKill, Summary: "Stop run r1, owned by Grace", OtherOwner: "Grace"},
		{Method: "run.delete", Summary: "Delete run r1"},
	} {
		if err := answerCard(t, svc, m, c, true); err != nil {
			t.Fatalf("%s: approved card = %v", c.Summary, err)
		}
	}
	if b, _ := os.ReadFile(svc.logPath("m1")); strings.Contains(string(b), "Auto-approved") || strings.Count(string(b), `"title":"Approved: `) != 3 {
		t.Fatalf("transcript = %q, want three approved cards and no auto-approval", b)
	}
	if got := st.settings["m1"].Approvals; got != 0 {
		t.Fatalf("approvals counted = %d, want none for calls auto-approval does not cover", got)
	}
}

func TestSuggestionFollowsManualApprovals(t *testing.T) {
	st := newMemStore()
	svc := New(Config{Dir: t.TempDir(), Store: st})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	c := protocol.AssistantConfirmation{Method: protocol.MethodRunLaunch, Summary: "Launch claude"}
	suggested := func() bool {
		t.Helper()
		got, err := svc.Settings(t.Context(), "m1")
		if err != nil {
			t.Fatal(err)
		}
		return got.SuggestAutoApprove
	}
	approveN := func(n int) {
		t.Helper()
		for range n {
			if err := answerCard(t, svc, m, c, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	approveN(2)
	if suggested() {
		t.Fatal("suggested after two approvals")
	}
	if err := answerCard(t, svc, m, c, false); !errors.Is(err, errDeclined) {
		t.Fatalf("declined card = %v", err)
	}
	approveN(2)
	if suggested() {
		t.Fatal("suggested after a decline and two approvals")
	}
	m.confirms.ttl = 10 * time.Millisecond
	if err := svc.approver(m)(t.Context(), c); !errors.Is(err, errExpired) {
		t.Fatalf("unanswered card = %v", err)
	}
	m.confirms.ttl = confirmTTL
	approveN(2)
	if suggested() {
		t.Fatal("suggested after a timeout and two approvals")
	}
	approveN(1)
	if !suggested() {
		t.Fatal("not suggested after three approvals in a row")
	}
	other := protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run r1, owned by Grace", OtherOwner: "Grace"}
	if err := answerCard(t, svc, m, other, true); err != nil || !suggested() || st.settings["m1"].Approvals != 3 {
		t.Fatalf("approving a card on another member's run = %v, approvals %d", err, st.settings["m1"].Approvals)
	}
}

func TestSuggestionAnswers(t *testing.T) {
	later := protocol.AssistantSettingsParams{Suggestion: protocol.AssistantSuggestionLater}
	never := protocol.AssistantSettingsParams{Suggestion: protocol.AssistantSuggestionNever}
	on, off := true, false
	eligible := store.AssistantSettings{Approvals: suggestAfter}
	set := func(t *testing.T, svc *Service, p protocol.AssistantSettingsParams) protocol.AssistantSettings {
		t.Helper()
		got, err := svc.SetSettings(t.Context(), "m1", p)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	newService := func(t *testing.T, st *memStore) *Service {
		svc := New(Config{Dir: t.TempDir(), Store: st})
		t.Cleanup(func() { _ = svc.Close() })
		return svc
	}

	t.Run("not now snoozes 7 days, then 30, then stops", func(t *testing.T) {
		st := newMemStore()
		svc := newService(t, st)
		for i, snooze := range []time.Duration{firstSnooze, secondSnooze} {
			st.settings["m1"] = store.AssistantSettings{Approvals: suggestAfter, Snoozes: i}
			if got, _ := svc.Settings(t.Context(), "m1"); !got.SuggestAutoApprove {
				t.Fatalf("answer %d: not suggested before it", i+1)
			}
			before := time.Now()
			if got := set(t, svc, later); got.SuggestAutoApprove {
				t.Fatalf("answer %d: still suggested after Not now", i+1)
			}
			row := st.settings["m1"]
			if row.Approvals != 0 || row.NoSuggest || row.SnoozedUntil.Before(before.Add(snooze)) || row.SnoozedUntil.After(time.Now().Add(snooze)) {
				t.Fatalf("answer %d: settings = %+v, want the count reset and a %s snooze", i+1, row, snooze)
			}
			row.Approvals = suggestAfter
			st.settings["m1"] = row
			if got, _ := svc.Settings(t.Context(), "m1"); got.SuggestAutoApprove {
				t.Fatalf("answer %d: suggested while snoozed", i+1)
			}
			row.SnoozedUntil = time.Now().Add(-time.Second)
			st.settings["m1"] = row
			if got, _ := svc.Settings(t.Context(), "m1"); !got.SuggestAutoApprove {
				t.Fatalf("answer %d: not suggested after the snooze ended", i+1)
			}
		}
		set(t, svc, later)
		if row := st.settings["m1"]; !row.NoSuggest {
			t.Fatalf("third Not now = %+v, want it to count as never", row)
		}
	})

	t.Run("don't suggest again", func(t *testing.T) {
		st := newMemStore()
		st.settings["m1"] = eligible
		svc := newService(t, st)
		if got := set(t, svc, never); got.SuggestAutoApprove || !st.settings["m1"].NoSuggest {
			t.Fatalf("after never: %+v, %+v", got, st.settings["m1"])
		}
		row := st.settings["m1"]
		row.Approvals = 10
		st.settings["m1"] = row
		if got, _ := svc.Settings(t.Context(), "m1"); got.SuggestAutoApprove {
			t.Fatal("suggested after Don't suggest again")
		}
	})

	t.Run("turning on ends it, turning off after on stops it", func(t *testing.T) {
		st := newMemStore()
		st.settings["m1"] = eligible
		svc := newService(t, st)
		if got := set(t, svc, protocol.AssistantSettingsParams{AutoApprove: &on}); !got.AutoApprove || got.SuggestAutoApprove || st.settings["m1"].Approvals != 0 {
			t.Fatalf("after turning on: %+v, %+v", got, st.settings["m1"])
		}
		set(t, svc, protocol.AssistantSettingsParams{AutoApprove: &off})
		row := st.settings["m1"]
		row.Approvals = 10
		st.settings["m1"] = row
		if got, _ := svc.Settings(t.Context(), "m1"); got.SuggestAutoApprove || !row.NoSuggest {
			t.Fatalf("after turning off: %+v, %+v", got, row)
		}
	})

	t.Run("turning off while off keeps it", func(t *testing.T) {
		st := newMemStore()
		st.settings["m1"] = eligible
		svc := newService(t, st)
		if got := set(t, svc, protocol.AssistantSettingsParams{AutoApprove: &off}); !got.SuggestAutoApprove {
			t.Fatalf("after turning off while off: %+v", got)
		}
	})
}
