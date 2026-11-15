package serverupdate

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store"
)

// autoEnv is a capable server running nightly build 412 on channel, with
// nightly.12 to nightly.14 and beta.1 and beta.2 published and nightly.json
// naming nightly.13 (build 413).
func autoEnv(t *testing.T, channel string) *env {
	t.Helper()
	setRunning(t, "v0.9.0-nightly.12", 412, 0)
	e := newEnv(t, false)
	for tag, build := range map[string]int{"v0.9.0-nightly.12": 412, "v0.9.0-nightly.13": 413, "v0.9.0-nightly.14": 414} {
		e.rel.SetBuild(tag, selfupdate.ChannelNightly, build, 0)
	}
	e.rel.SetBuild("v0.9.0-beta.1", selfupdate.ChannelBeta, 380, 0)
	e.rel.SetBuild("v0.9.0-beta.2", selfupdate.ChannelBeta, 412, 0)
	publishPointer(t, e.rel, "nightly", pointer("nightly", "v0.9.0-nightly.13", 413), releasetest.NightlyKey)
	if err := e.db.SetReleaseChannel(t.Context(), channel); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) pending(t *testing.T) *store.PendingServerUpdate {
	t.Helper()
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state.Pending
}

func (e *env) wantPending(t *testing.T, version string, by domain.MemberID) {
	t.Helper()
	p := e.pending(t)
	if version == "" {
		if p != nil {
			t.Fatalf("pending = %+v, want none", p)
		}
		return
	}
	if p == nil || p.Version != version || p.RequestedBy != by {
		t.Fatalf("pending = %+v, want %s by %s", p, version, by)
	}
}

// A newer build on the channel is scheduled by the server, named as the
// actor, and applied at the first idle moment through the pending path.
func TestServerSchedulesANewerBuildAndAppliesItWhenIdle(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	if p := e.phase(t); p.Phase != events.ServerUpdateScheduled || p.ActorID != ServerActor {
		t.Fatalf("phase = %+v, want scheduled by the server", p)
	}
	st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending == nil || !st.Pending.ByServer || st.Pending.RequestedBy != string(ServerActor) {
		t.Fatalf("status pending = %+v, want it marked as the server's", st.Pending)
	}
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatal("a busy server applied the update")
	}
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatal("the idle server did not apply the update it scheduled")
	}
	for _, want := range []events.ServerUpdatePhase{events.ServerUpdatePreflight, events.ServerUpdateApplying, events.ServerUpdateRestarting} {
		if p := e.phase(t); p.Phase != want || p.ActorID != ServerActor {
			t.Fatalf("phase = %+v, want %s by the server", p, want)
		}
	}
}

// A super admin's cancel holds until a higher build ships.
func TestCancelledBuildIsNotScheduledAgain(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Declined == nil || st.Declined.Version != "v0.9.0-nightly.13" || st.Declined.Build != 413 {
		t.Fatalf("declined = %+v, want nightly.13 (413)", st.Declined)
	}
	// A super admin's own update ignores the mark.
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("a super admin's update of the declined build: %v", err)
	}
	e.wantPending(t, "v0.9.0-nightly.13", "mem_a")
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}

	publishPointer(t, e.rel, "nightly", pointer("nightly", "v0.9.0-nightly.14", 414), releasetest.NightlyKey)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.14", ServerActor)
}

// A rollback with allow_downgrade is not undone by the next check.
func TestRollbackIsNotUndoneByTheNextCheck(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	setRunning(t, "v0.9.0-nightly.13", 413, 0)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	e.underSystemd = true
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{
		Version: "v0.9.0-nightly.12", When: protocol.ServerUpdateNow, AllowDowngrade: true,
	}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// The process that comes back runs the build rolled back to.
	setRunning(t, "v0.9.0-nightly.12", 412, 0)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
}

// A scheduled release that fails its 3 retries is recorded failed and not
// scheduled again; nor is one whose last attempt rolled back.
func TestFailingReleaseIsNotRequeued(t *testing.T) {
	old := probeRetryDelay
	probeRetryDelay = 0
	t.Cleanup(func() { probeRetryDelay = old })
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	e.busy = domain.ServerBusy{}
	e.setUnavailable("aether-android.apk")
	for range maxRetries {
		e.svc.Tick(t.Context())
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.Last == nil || state.Last.Outcome != store.ServerUpdateFailed {
		t.Fatalf("state after %d failures = %+v, want recorded failed", maxRetries, state)
	}
	e.heal()
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")

	rolled := autoEnv(t, selfupdate.ChannelNightly)
	if err := rolled.db.SetLastServerUpdate(t.Context(), store.ServerUpdateAttempt{
		Version: "v0.9.0-nightly.13", Outcome: store.ServerUpdateRolledBack, Detail: "did not start", At: rolled.now,
	}); err != nil {
		t.Fatal(err)
	}
	rolled.svc.checkAndSchedule(t.Context())
	rolled.wantPending(t, "", "")
}

// The server never replaces a member's pending update.
func TestServerLeavesAMembersPendingUpdateAlone(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.13", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	publishPointer(t, e.rel, "nightly", pointer("nightly", "v0.9.0-nightly.14", 414), releasetest.NightlyKey)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", "mem_a")
}

// A server that left nightly for a channel below its build schedules
// nothing until that channel reaches its build, then takes it.
func TestWaitingServerSchedulesNothingUntilTheChannelCatchesUp(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelBeta)
	publishPointer(t, e.rel, "beta", pointer("beta", "v0.9.0-beta.1", 380), releasetest.Key)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	publishPointer(t, e.rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-beta.2", ServerActor)
}

// AETHER_NO_UPDATE_CHECK turns the scheduling off with the check.
func TestOptOutSchedulesNothing(t *testing.T) {
	t.Setenv(selfupdate.OptOutEnv, "1")
	e := autoEnv(t, selfupdate.ChannelNightly)
	if err := e.svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.svc.Close() })
	if _, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	e.wantPending(t, "", "")
	if n := e.rel.Requests("/releases/download/channels/nightly.json"); n != 0 {
		t.Fatalf("asked for nightly.json %d times with the check off", n)
	}
}
