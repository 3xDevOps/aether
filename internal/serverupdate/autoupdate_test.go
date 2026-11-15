package serverupdate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store"
)

// autoEnv is a capable server running nightly build 412 on channel (""
// leaves it unchosen), with
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
	e.rel.SetBuild("v0.9.0-beta.3", selfupdate.ChannelBeta, 420, 0)
	publishPointer(t, e.rel, "nightly", pointer("nightly", "v0.9.0-nightly.13", 413), releasetest.NightlyKey)
	if channel == "" {
		return e
	}
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

// A server that has not chosen its channel follows beta but schedules
// nothing by itself, however new beta's build.
func TestUnchosenServerSchedulesNothing(t *testing.T) {
	e := autoEnv(t, "")
	publishPointer(t, e.rel, "beta", pointer("beta", "v0.9.0-beta.3", 420), releasetest.Key)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	if st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{}); err != nil || !st.ChannelPending || !st.UpdateAvailable {
		t.Fatalf("status = %+v, %v; want beta.3 offered on an unchosen channel", st, err)
	}
}

// A binary built from source never replaces itself unattended; the newer
// release is still offered.
func TestSourceBuildSchedulesNothing(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	setRunning(t, "v0.9.0-nightly.3-5-gabc1234-dirty", 0, 0)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	if st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{}); err != nil || !st.UpdateAvailable {
		t.Fatalf("status = %+v, %v; want nightly.13 offered", st, err)
	}
}

// The declined mark is in the store: removing or corrupting check.json,
// and restarting, keeps a cancelled build from being scheduled again.
func TestDeclineSurvivesCheckStateAndRestart(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}
	check := filepath.Join(e.state, "check.json")
	if err := os.WriteFile(check, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := e.restart(t)
	restarted.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	if err := os.Remove(check); err != nil {
		t.Fatal(err)
	}
	restarted = e.restart(t)
	restarted.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	st, err := restarted.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil || st.Declined == nil || st.Declined.Build != 413 {
		t.Fatalf("status after a restart = %+v, %v; want nightly.13 declined", st, err)
	}
}

// A rollback's or cancel's decline while the server's scheduling waits on
// the network does not wait for it, and the scheduling, deciding again
// afterwards, schedules nothing. A cancel while a check holds the slot
// returns at once too.
func TestDeclineDuringSchedulingHolds(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.runCheck(t.Context())
	probing, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.onRequest = func() { once.Do(func() { close(probing); <-resume }) }
	done := make(chan struct{})
	go func() { e.svc.schedule(t.Context()); close(done) }()
	<-probing
	declined := make(chan error, 1)
	go func() { declined <- e.svc.decline(t.Context(), e.svc.known("v0.9.0-nightly.13")) }()
	select {
	case err := <-declined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the decline waited on the scheduling's network")
	}
	close(resume)
	<-done
	e.wantPending(t, "", "")

	e.svc.run <- struct{}{}
	defer func() { <-e.svc.run }()
	cancelled := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel})
		cancelled <- err
	}()
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel waited on a running check")
	}
}

// A scheduled update cancelled while its idle moment's preflight runs is
// not applied.
func TestCancelDuringTheIdlePreflightHolds(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	probing, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.onRequest = func() { once.Do(func() { close(probing); <-resume }) }
	done := make(chan struct{})
	go func() { e.svc.Tick(t.Context()); close(done) }()
	<-probing
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}
	close(resume)
	<-done
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatal("a cancelled update was applied")
	}
	if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Last != nil {
		t.Fatalf("state = %+v, %v; want the cancel as the only outcome", state, err)
	}
}

// An update the server scheduled that rolled back stays declined after an
// unrelated update replaces the last attempt.
func TestRolledBackServerUpdateIsDeclined(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	trial := &Trial{dir: e.state, rec: &swapRecord{
		Phase: recordRolledBack, FromVersion: "v0.9.0-nightly.12", ToVersion: "v0.9.0-nightly.13", ToBuild: 413,
		RequestedBy: string(ServerActor), Error: "did not start", At: e.now,
	}}
	if err := saveRecord(e.state, trial.rec); err != nil {
		t.Fatal(err)
	}
	if err := trial.Commit(t.Context(), e.db, e.bus); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetLastServerUpdate(t.Context(), store.ServerUpdateAttempt{Version: "v0.9.0-nightly.11", Outcome: store.ServerUpdateFailed, Detail: "x", At: e.now}); err != nil {
		t.Fatal(err)
	}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
}

// A build from source at a clean tag is not a stamped release and never
// replaces itself.
func TestUnstampedBuildSchedulesNothing(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	setRunning(t, "v0.9.0-nightly.12", 0, 0)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
}

// A version-only declined mark is kept beside a later mark with a build.
func TestDeclinedVersionSurvivesABuildMark(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	if err := e.svc.decline(t.Context(), selfupdate.Release{Version: "v0.9.0-nightly.14"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.decline(t.Context(), selfupdate.Release{Version: "v0.9.0-nightly.13", Build: 413}); err != nil {
		t.Fatal(err)
	}
	publishPointer(t, e.rel, "nightly", pointer("nightly", "v0.9.0-nightly.14", 414), releasetest.NightlyKey)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
}

// A check that ran on the old channel schedules nothing once the channel
// changed, and a server update refused at its idle moment because the
// channel moved does not raise the declined mark.
func TestChannelChangeDuringACheckSchedulesNothing(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.runCheck(t.Context())
	if err := e.db.SetReleaseChannel(t.Context(), selfupdate.ChannelBeta); err != nil {
		t.Fatal(err)
	}
	e.svc.schedule(t.Context())
	e.wantPending(t, "", "")

	if err := e.db.SetReleaseChannel(t.Context(), selfupdate.ChannelNightly); err != nil {
		t.Fatal(err)
	}
	e.svc.schedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	if err := e.db.SetReleaseChannel(t.Context(), selfupdate.ChannelStable); err != nil {
		t.Fatal(err)
	}
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	e.svc.Tick(t.Context())
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.Declined != nil || state.Last == nil || !strings.Contains(state.Last.Detail, "does not take") {
		t.Fatalf("state = %+v (last %+v), want refused without a declined mark", state, state.Last)
	}
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatal("installed a release the channel does not take")
	}
}

// A channel change keeps a pending update whose manifest cannot be read,
// and never fetches one for nightly, which takes every release.
func TestChannelChangeKeepsAPendingUpdateItCannotCheck(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelBeta)
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-beta.3", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	e.setMissing(selfupdate.ManifestName)
	set, err := e.svc.SetChannel(t.Context(), "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelStable})
	if err != nil || set.Retired != "" {
		t.Fatalf("switch = %+v, %v; want nothing retired", set, err)
	}
	e.wantPending(t, "v0.9.0-beta.3", "mem_a")
	e.heal()
	before := e.rel.Requests("/releases/download/v0.9.0-beta.3/" + selfupdate.ManifestName)
	if _, err := e.svc.SetChannel(t.Context(), "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly}); err != nil {
		t.Fatal(err)
	}
	if n := e.rel.Requests("/releases/download/v0.9.0-beta.3/" + selfupdate.ManifestName); n != before {
		t.Fatalf("fetched the pending release's manifest for nightly (%d requests)", n-before)
	}
	e.wantPending(t, "v0.9.0-beta.3", "mem_a")
}

// restart is the next process on e's store and state directory.
func (e *env) restart(t *testing.T) *Service {
	t.Helper()
	svc, err := New(Config{
		StateDir: e.state, Database: filepath.Join(e.dir, "aether.db"), Store: e.db, Bus: e.bus,
		Checker: selfupdate.NewChecker(e.release.URL, time.Hour), Executable: e.self, Host: e.svc.cfg.Host,
		Busy: e.svc.cfg.Busy, Hold: e.svc.cfg.Hold, Now: e.svc.cfg.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
