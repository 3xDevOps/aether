package serverupdate

import (
	"context"
	"errors"
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
	go func() {
		declined <- e.svc.decline(t.Context(), e.svc.known("v0.9.0-nightly.13"))
	}()
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

// A cancel whose release's build cannot be read still cancels, and says
// the release was not recorded as declined; no mark by version alone is
// kept.
func TestCancelWithoutABuildSaysItDidNotDecline(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	e.setMissing(selfupdate.ManifestName)
	res, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel})
	if err != nil || res.Status != protocol.ServerUpdateCancelled || !strings.Contains(res.NotDeclined, "was not recorded as declined") {
		t.Fatalf("cancel = %+v, %v; want cancelled with the note", res, err)
	}
	e.wantPending(t, "", "")
	st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil || st.Declined != nil || st.DeclineNote != res.NotDeclined {
		t.Fatalf("status = %+v, %v; want no mark and the note", st, err)
	}
}

// A cancel during the download of a scheduled update stands: the download
// is undone and nothing is installed. So does a member's idle update of
// another release scheduled meanwhile, which stays pending.
func TestUpdateReplacedDuringTheDownloadIsNotApplied(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(t *testing.T, e *env)
		pending string
	}{
		{"cancel", func(t *testing.T, e *env) {
			if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"member's idle update", func(t *testing.T, e *env) {
			if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
				t.Fatal(err)
			}
		}, "v0.9.0-nightly.14"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := autoEnv(t, selfupdate.ChannelNightly)
			e.busy = domain.ServerBusy{Runs: 1}
			e.svc.checkAndSchedule(t.Context())
			e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
			e.busy, e.underSystemd = domain.ServerBusy{}, true
			e.stalled, e.resume = make(chan struct{}), make(chan struct{})
			done := make(chan struct{})
			go func() { e.svc.Tick(t.Context()); close(done) }()
			<-e.stalled
			tc.replace(t, e)
			close(e.resume)
			<-done
			if got := e.read(t, e.self); got != "the old binary" {
				t.Fatalf("aether-server = %q, want the download undone", got)
			}
			if tc.pending == "" {
				e.wantPending(t, "", "")
			} else {
				e.wantPending(t, tc.pending, "mem_a")
			}
			if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Last != nil {
				t.Fatalf("state = %+v, %v; want no outcome recorded", state, err)
			}
		})
	}
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

// With release checks turned off the server applies nothing it scheduled
// itself, while a member's update still applies.
func TestOptOutHoldsTheServersOwnPendingUpdate(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	t.Setenv(selfupdate.OptOutEnv, "1")
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatal("applied the server's own update with release checks off")
	}
	e.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.13", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatal("a member's update did not apply with release checks off")
	}
}

// A rollback the preflight refuses declines nothing.
func TestRefusedRollbackDeclinesNothing(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	setRunning(t, "v0.9.0-nightly.13", 413, 0)
	e.svc.runCheck(t.Context())
	e.rel.SetStoreSchema(store.SchemaVersion() - 1)
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{
		Version: "v0.9.0-nightly.12", When: protocol.ServerUpdateNow, AllowDowngrade: true,
	}); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("rollback across a store schema = %v, want refused", err)
	}
	if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Declined != nil {
		t.Fatalf("state = %+v, %v; want nothing declined", state, err)
	}
}

// A cancel records the build of the release it cancels from its signed
// manifest, even when the server's last check names another.
func TestCancelDeclinesThePendingReleasesOwnBuild(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.runCheck(t.Context())
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}
	if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Declined == nil || state.Declined.Build != 414 {
		t.Fatalf("state = %+v, %v; want nightly.14 declined at 414", state, err)
	}
}

// Once an update can no longer be stopped, a cancel says so instead of
// reporting a cancel that changes nothing: an update applied now, and a
// scheduled one that has taken its pending row.
func TestCancelRefusesAnUpdateAlreadyCommitted(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.underSystemd = true
	e.stalled, e.resume = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.13", When: protocol.ServerUpdateNow})
		done <- err
	}()
	<-e.stalled
	_, _, err := e.svc.Update(t.Context(), "mem_b", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "v0.9.0-nightly.13 is applying now and can no longer be cancelled") {
		t.Fatalf("cancel during an update applied now = %v", err)
	}
	close(e.resume)
	if err = <-done; err != nil {
		t.Fatal(err)
	}

	sched := autoEnv(t, selfupdate.ChannelNightly)
	sched.busy = domain.ServerBusy{Runs: 1}
	sched.svc.checkAndSchedule(t.Context())
	pending := sched.pending(t)
	sched.busy = domain.ServerBusy{}
	restart, err := sched.svc.apply(t.Context(), *pending, false, true)
	if err != nil || restart == nil {
		t.Fatalf("apply = %v", err)
	}
	if _, _, err := sched.svc.Update(t.Context(), "mem_b", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); !errors.Is(err, ErrBusy) {
		t.Fatalf("cancel after the scheduled update took its row = %v, want refused", err)
	}
	sched.svc.release()
}

// A release whose self-schedule keeps failing is retried on the backoff,
// then declined after maxRetries in a row instead of every 5 minutes for
// ever.
func TestFailingSelfScheduleBacksOffThenDeclines(t *testing.T) {
	old := probeRetryDelay
	probeRetryDelay = 0
	t.Cleanup(func() { probeRetryDelay = old })
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.setUnavailable("aether-android.apk")
	for i := range maxRetries {
		e.svc.checkAndSchedule(t.Context())
		e.wantPending(t, "", "")
		st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
		if err != nil || !strings.Contains(st.ScheduleError, "not scheduling v0.9.0-nightly.13 by itself") {
			t.Fatalf("status = %+v, %v; want the schedule error", st, err)
		}
		if i < maxRetries-1 {
			if wait := e.svc.current().NextAt.Sub(e.now); wait != backoff(i+1) {
				t.Fatalf("after failure %d the next check is in %s, want %s", i+1, wait, backoff(i+1))
			}
		}
	}
	if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Declined == nil || state.Declined.Build != 413 {
		t.Fatalf("state = %+v, %v; want nightly.13 declined after %d failures", state, err, maxRetries)
	}
}

// A swap whose undo fails keeps its record at swapping, so the next start
// puts the previous binary back instead of running the release as a
// trial, and a member's update scheduled meanwhile stays pending. A crash
// between the swap and its commit is rolled back the same way.
func TestSwapNotCommittedIsRolledBackAtStart(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	e.stalled, e.resume = make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() { e.svc.Tick(t.Context()); close(done) }()
	<-e.stalled
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.self + ".prev"); err != nil {
		t.Fatal(err)
	}
	close(e.resume)
	<-done
	e.wantPending(t, "v0.9.0-nightly.14", "mem_a")
	if rec, err := loadRecord(e.state); err != nil || rec == nil || rec.Phase != recordSwapping {
		t.Fatalf("record = %+v, %v; want it left at swapping", rec, err)
	}
	// The previous binary is gone, so this rollback is incomplete; what
	// matters is that the release is not run as a trial.
	_, _ = Boot(e.state, e.svc.cfg.Host)
	if rec, _ := loadRecord(e.state); rec != nil && rec.Phase == recordVerifying {
		t.Fatal("the next start would run the cancelled release as a trial")
	}

	crash := autoEnv(t, selfupdate.ChannelNightly)
	m, err := feed(nil, crash.release.URL).Fetch(t.Context(), "v0.9.0-nightly.13")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = swap(t.Context(), crash.svc.plan(store.PendingServerUpdate{Version: "v0.9.0-nightly.13"}), "v0.9.0-nightly.13", m); err != nil {
		t.Fatal(err)
	}
	if got := crash.read(t, crash.self); got != newServerBinary {
		t.Fatalf("aether-server = %q after the swap", got)
	}
	if _, err = Boot(crash.state, crash.svc.cfg.Host); err != nil {
		t.Fatal(err)
	}
	if got := crash.read(t, crash.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want the previous binary back after a crash before the commit", got)
	}
}

// A scheduled update that is no longer pending when a session starts
// during its download is not announced as waiting for the next idle
// moment.
func TestCancelledUpdateIsNotRescheduledWhenBusy(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.checkAndSchedule(t.Context())
	if p := e.phase(t); p.Phase != events.ServerUpdateScheduled {
		t.Fatalf("phase = %+v", p)
	}
	e.busy = domain.ServerBusy{}
	e.stalled, e.resume = make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() { e.svc.Tick(t.Context()); close(done) }()
	<-e.stalled
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatal(err)
	}
	e.busy = domain.ServerBusy{Runs: 1}
	close(e.resume)
	<-done
	for {
		select {
		case ev := <-e.sub.Events():
			if p, _ := ev.Payload.(events.ServerUpdatePayload); p.Phase == events.ServerUpdateScheduled {
				t.Fatalf("announced %+v for a cancelled update", p)
			}
		case <-time.After(200 * time.Millisecond):
			return
		}
	}
}

// failingSchedule is a store whose SchedulePendingServerUpdate fails.
type failingSchedule struct{ store.Store }

func (failingSchedule) SchedulePendingServerUpdate(context.Context, store.PendingServerUpdate) (bool, error) {
	return false, errors.New("disk I/O error")
}

// A release whose pending write keeps failing is declined after
// maxRetries, and the decline does not deadlock on the decision lock the
// write ran under.
func TestFailingScheduleWriteDeclinesWithoutDeadlock(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	svc, err := New(Config{
		StateDir: e.state, Database: filepath.Join(e.dir, "aether.db"), Store: failingSchedule{e.db}, Bus: e.bus,
		Checker: selfupdate.NewChecker(e.release.URL, time.Hour), Executable: e.self, Host: e.svc.cfg.Host,
		Busy: e.svc.cfg.Busy, Hold: e.svc.cfg.Hold, Now: e.svc.cfg.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for range maxRetries {
			svc.checkAndSchedule(t.Context())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("scheduling deadlocked")
	}
	if state, err := e.db.GetServerUpdate(t.Context()); err != nil || state.Declined == nil || state.Declined.Build != 413 {
		t.Fatalf("state = %+v, %v; want nightly.13 declined", state, err)
	}
	if _, _, err := svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatalf("a cancel after the declines: %v", err)
	}
}

// A swap left at swapping, whose undo failed, refuses a new update until
// a restart has put the previous binaries back.
func TestUnfinishedSwapRefusesANewUpdate(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	if err := saveRecord(e.state, &swapRecord{Phase: recordSwapping, FromVersion: "v0.9.0-nightly.12", ToVersion: "v0.9.0-nightly.13", Binaries: []string{e.self}, At: e.now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateNow}); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "was not undone") {
		t.Fatalf("update over an unfinished swap = %v, want refused", err)
	}
	if _, err := Stage(t.Context(), e.svc.plan(store.PendingServerUpdate{Version: "v0.9.0-nightly.14"}), "v0.9.0-nightly.14"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Stage over an unfinished swap = %v, want refused", err)
	}
}

// While an update applied now is past cancelling, a new idle or now
// update is refused with the same reason, so nothing is cleared silently.
func TestCommittedUpdateRefusesANewOne(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.underSystemd = true
	e.stalled, e.resume = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.13", When: protocol.ServerUpdateNow})
		done <- err
	}()
	<-e.stalled
	for _, when := range []string{protocol.ServerUpdateIdle, protocol.ServerUpdateNow} {
		if _, _, err := e.svc.Update(t.Context(), "mem_b", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: when}); !errors.Is(err, ErrBusy) {
			t.Errorf("%s update during a committed apply = %v, want refused", when, err)
		}
	}
	close(e.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := e.pending(t); p != nil {
		t.Fatalf("pending = %+v, want none after the applied update", p)
	}
}
