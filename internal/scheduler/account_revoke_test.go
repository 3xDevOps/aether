package scheduler

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

const continuedOnAda = "Your previous turn stopped because Grace revoked access to their account. The session continues on Ada's account. Continue the task where you stopped."

// launchOnGrace starts Ada's Enhanced run on Grace's shared account.
func (e *shareEnv) launchOnGrace(t *testing.T) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	return run
}

func (e *shareEnv) revoke(t *testing.T) {
	t.Helper()
	if _, err := e.db.RevokeAccountShare(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.RevokeAccount(e.owner.ID, e.member.ID)
}

func (e *shareEnv) assertMovedToAda(t *testing.T, rt *acpRuntime, run domain.RunID, notes *roomNotes, note int) {
	t.Helper()
	waitFor(t, "the session on Ada's account", func() bool {
		row, err := e.db.GetRun(t.Context(), run)
		return err == nil && row.AccountMemberID == e.member.ID
	})
	if row, err := e.db.GetRun(t.Context(), run); err != nil || row.Status.Terminal() {
		t.Fatalf("moved run = %+v, %v; want it live", row, err)
	}
	segments, err := e.db.ListRunAccountSegments(t.Context(), run)
	if err != nil || len(segments) != 2 || segments[0].AccountMemberID != e.owner.ID || segments[0].Reason != store.RunAccountLaunchReason ||
		segments[1].AccountMemberID != e.member.ID || segments[1].Reason != revokedSegment {
		t.Fatalf("segments = %+v, %v", segments, err)
	}
	waitFor(t, "the room note", func() bool {
		all := notes.all()
		return len(all) > note && all[note] == "Continued on Ada's account because Grace revoked access to their account."
	})
	c := e.rt.byName(string(run))
	if c == nil || c.spec.Env["AETHER_ACCOUNT_MEMBER_ID"] != string(e.member.ID) {
		t.Fatalf("new container = %+v, want it on Ada's account", c)
	}
	e.assertOwnerExposedOnlyAt(t, c.spec)
	waitFor(t, "the continuation prompt", func() bool {
		execs := rt.all()
		prompts := execs[len(execs)-1].wire.prompts(t)
		return len(prompts) == 1 && prompts[0][0].Text != nil && prompts[0][0].Text.Text == continuedOnAda
	})
}

func TestRevokeMovesALiveRunToTheLaunchersOwnAccount(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run := e.launchOnGrace(t)
	first := e.rt.byName(string(run.ID))

	e.revoke(t)

	e.assertMovedToAda(t, rt, run.ID, notes, 0)
	if e.rt.byName(string(run.ID)) == first || e.containerCount() != 1 {
		t.Fatalf("%d containers, want only the session's new one", e.containerCount())
	}
	waitFor(t, "the move settled on Ada's login", func() bool {
		sc, err := e.sched.readSidecar(run.ID)
		return err == nil && sc.AccountSwap == nil
	})
}

func TestRevokeStopsARunNoOtherAccountCanTake(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		setup        func(*shareEnv, domain.RunID)
	}{
		{"no account", "account share revoked: Grace revoked access to their account; no other account can run claude", func(e *shareEnv, _ domain.RunID) {
			e.sched.UseUsageFallback(UsageFallback{Usage: claudeUsage{e.member.ID: 100}, AuthorizationMu: &sync.Mutex{}, Note: (&roomNotes{}).post})
		}},
		{"paused", "account share revoked: Grace revoked access to their account; cannot continue on another account: the session is paused", func(e *shareEnv, run domain.RunID) {
			if err := e.sched.Pause(t.Context(), run, e.member.ID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _, notes := newFallbackEnv(t, enhancedClaude)
			e.share(t)
			run := e.launchOnGrace(t)
			tc.setup(e, run.ID)

			e.revoke(t)

			got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
			if got.Reason != tc.reason || got.AccountMemberID != e.owner.ID || len(notes.all()) != 0 {
				t.Fatalf("run = reason %q account %s, want %q on Grace's account", got.Reason, got.AccountMemberID, tc.reason)
			}
			waitFor(t, "the container removed", func() bool { return e.rt.byName(string(run.ID)) == nil })
		})
	}
}

// A revoke touches only runs on the revoked account that the revoked member
// may no longer use: Ada's run on her own account and Lin's on Grace's, still
// shared with Lin, keep their containers, through the sweep too.
func TestRevokeLeavesOtherRunsAlone(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	lin := e.addRecipient(t)
	if _, err := e.db.ShareAccount(t.Context(), e.owner.ID, lin.ID); err != nil {
		t.Fatal(err)
	}
	own, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "own", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.sched.Launch(t.Context(), e.ws.ID, lin.ID, e.owner.ID, "lin's", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	containers := map[domain.RunID]*fakeContainer{own.ID: e.rt.byName(string(own.ID)), other.ID: e.rt.byName(string(other.ID))}

	e.revoke(t)
	e.sched.leaveRevokedAccounts(t.Context(), nil)
	time.Sleep(100 * time.Millisecond)

	for id, want := range map[domain.RunID]domain.MemberID{own.ID: e.member.ID, other.ID: e.owner.ID} {
		row, err := e.db.GetRun(t.Context(), id)
		if err != nil || row.Status != domain.RunRunning || row.AccountMember() != want {
			t.Fatalf("run %s = %+v, %v; want it running on %s", id, row, err, want)
		}
		if c := e.rt.byName(string(id)); c != containers[id] || c.currentState() != "running" {
			t.Fatalf("run %s lost its container", id)
		}
	}
	if len(notes.all()) != 0 {
		t.Fatalf("notes = %v, want no move", notes.all())
	}
}

// After a revoke moved Ada's run off Grace's account, Grace can share it again:
// a new run launches on it and stays there until the next revoke.
func TestShareAgainAfterARevoke(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	first := e.launchOnGrace(t)
	e.revoke(t)
	e.assertMovedToAda(t, rt, first.ID, notes, 0)

	e.share(t)
	second := e.launchOnGrace(t)
	container := e.rt.byName(string(second.ID))
	e.sched.leaveRevokedAccounts(t.Context(), nil)
	time.Sleep(100 * time.Millisecond)
	if row, err := e.db.GetRun(t.Context(), second.ID); err != nil || row.AccountMemberID != e.owner.ID || e.rt.byName(string(second.ID)) != container {
		t.Fatalf("run on the shared-again account = %+v, %v; want it kept on Grace's account", row, err)
	}

	e.revoke(t)
	e.assertMovedToAda(t, rt, second.ID, notes, 1)
	if row, err := e.db.GetRun(t.Context(), first.ID); err != nil || row.AccountMemberID != e.member.ID {
		t.Fatalf("first run = %+v, %v; want it still on Ada's account", row, err)
	}
	if !slices.Equal(notes.all()[:1], []string{"Continued on Ada's account because Grace revoked access to their account."}) {
		t.Fatalf("notes = %v", notes.all())
	}
}

// A revoke whose move the server never started, as when it stopped first,
// is found by the sweep.
func TestSweepMovesARunOnARevokedAccount(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run := e.launchOnGrace(t)
	if _, err := e.db.RevokeAccountShare(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}

	e.sched.leaveRevokedAccounts(t.Context(), nil)

	e.assertMovedToAda(t, rt, run.ID, notes, 0)
}
