package serverupdate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
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
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// env is one service under test with everything the real one talks to
// stubbed: a release server on localhost, two throwaway binaries in a temp
// directory, a real SQLite store, and an exec that records instead of
// replacing the test process.
type env struct {
	svc      *Service
	db       *store.DB
	bus      *events.InProc
	sub      events.Subscription
	dir      string
	self     string
	sibling  string
	release  *httptest.Server
	rel      *releasetest.Release
	state    string
	execs    chan execCall
	restarts chan struct{}
	now      time.Time
	// busy is what Config.Busy reports; the zero value is an idle server.
	busy domain.ServerBusy
	// hold is what Config.Hold reports.
	hold string
	// underSystemd is what Host.UnderSystemd reports.
	underSystemd bool

	// poisonMu guards poisoned, the set of asset names whose published
	// checksum is wrong. The release handler reads it per request.
	poisonMu sync.Mutex
	poisoned map[string]bool
	// missing names assets the release host answers 404 for.
	missing map[string]bool
	// unavailable names assets the release host answers 503 for, and
	// counts the requests for each.
	unavailable map[string]int
	// stalled, when set, receives once the server binary's download has
	// started, which then waits for resume.
	stalled, resume chan struct{}
}

// companionAsset is the aether binary that sits beside aether-server.
func companionAsset() string { return "aether-" + runtime.GOOS + "-" + runtime.GOARCH }

// poisonCompanion breaks only the companion's published checksum, which is
// the partial-swap case: the server's own asset verifies, the one beside
// it does not.
func (e *env) poisonCompanion() {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	e.poisoned = map[string]bool{companionAsset(): true}
}

// isPoisoned reports whether asset's download should not match the manifest.
func (e *env) isPoisoned(asset string) bool {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	return e.poisoned[asset]
}

func (e *env) isMissing(asset string) bool {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	return e.missing[asset]
}

func (e *env) setMissing(asset string) {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	e.missing = map[string]bool{asset: true}
}

func (e *env) setUnavailable(asset string) {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	e.unavailable = map[string]int{asset: 0}
}

// unavailableHit counts a request for an unavailable asset and reports
// whether it is one.
func (e *env) unavailableHit(asset string) bool {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	n, ok := e.unavailable[asset]
	if ok {
		e.unavailable[asset] = n + 1
	}
	return ok
}

func (e *env) unavailableHits(asset string) int {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	return e.unavailable[asset]
}

func (e *env) heal() {
	e.poisonMu.Lock()
	defer e.poisonMu.Unlock()
	e.poisoned, e.missing, e.unavailable = nil, nil, nil
}

type execCall struct {
	path string
	argv []string
	env  []string
}

const (
	newServerBinary = "the new aether-server"
	newCLIBinary    = "the new aether"
	// releaseTagUnderTest is the one tag the stub release server publishes.
	releaseTagUnderTest = "v0.2.0"
)

// releaseServer serves the signed release for tag the way GitHub does.
// Which assets are corrupted in transit is read per request from e, so a
// test can break one asset alone.
func (e *env) releaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	assets := map[string][]byte{
		"aether-server" + suffix: []byte(newServerBinary),
		"aether" + suffix:        []byte(newCLIBinary),
	}
	for _, name := range clientAssets {
		if _, ok := assets[name]; !ok {
			assets[name] = []byte("client " + name)
		}
	}
	rel := releasetest.Serve(t, tag, assets)
	e.rel = rel
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if e.stalled != nil && r.Method == http.MethodGet && strings.HasPrefix(name, "aether-server-") {
			e.stalled <- struct{}{}
			<-e.resume
		}
		if e.isMissing(name) {
			http.NotFound(w, r)
			return
		}
		if e.unavailableHit(name) {
			http.Error(w, "try again later", http.StatusServiceUnavailable)
			return
		}
		if e.isPoisoned(name) {
			_, _ = w.Write(bytes.ToUpper(assets[name]))
			return
		}
		rel.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestBus is an in-process bus with no log, closed with the test.
func newTestBus(t *testing.T) *events.InProc {
	t.Helper()
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func newEnv(t *testing.T, poison bool) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{
		dir:     dir,
		self:    filepath.Join(dir, "aether-server"),
		sibling: filepath.Join(dir, "aether"),

		execs:    make(chan execCall, 4),
		restarts: make(chan struct{}, 4),
		now:      time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
	if poison {
		e.poisoned = map[string]bool{
			"aether-server-" + runtime.GOOS + "-" + runtime.GOARCH: true,
			companionAsset(): true,
		}
	}
	e.release = e.releaseServer(t, releaseTagUnderTest)
	for _, path := range []string{e.self, e.sibling} {
		if err := os.WriteFile(path, []byte("the old binary"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	db, err := storetest.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e.db = db

	bus := newTestBus(t)
	e.bus = bus
	// The feed is per workspace, so a workspace must exist for the phases
	// to have anywhere to land.
	ws := &domain.Workspace{Name: "aether", BaseBranch: "main",
		Environment: domain.WorkspaceEnvironment{}}
	if cerr := db.CreateWorkspace(t.Context(), ws); cerr != nil {
		t.Fatalf("create workspace: %v", cerr)
	}
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeServerUpdate}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	e.sub = sub

	e.state = filepath.Join(dir, "server-update")
	svc, err := New(Config{
		StateDir:   e.state,
		Database:   filepath.Join(dir, "aether.db"),
		Store:      db,
		Bus:        bus,
		Checker:    selfupdate.NewChecker(e.release.URL, time.Hour),
		Executable: e.self,
		Host: Host{
			Exec: func(path string, argv, environ []string) error {
				e.execs <- execCall{path: path, argv: argv, env: environ}
				// The real syscall.Exec never returns on success.
				// Reporting success here would leave the caller's restart
				// hanging, so the stub reports the one thing that does
				// return.
				return errors.New("exec stub")
			},
			Restart:      func() error { e.restarts <- struct{}{}; return nil },
			UnderSystemd: func() bool { return e.underSystemd },
		},
		Busy: func(context.Context) domain.ServerBusy { return e.busy },
		Hold: func() string { return e.hold },
		Now:  func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.svc = svc
	return e
}

// phase waits for the next server.update phase on the feed.
func (e *env) phase(t *testing.T) events.ServerUpdatePayload {
	t.Helper()
	select {
	case ev := <-e.sub.Events():
		payload, ok := ev.Payload.(events.ServerUpdatePayload)
		if !ok {
			t.Fatalf("event payload = %T, want ServerUpdatePayload", ev.Payload)
		}
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a server.update event")
		return events.ServerUpdatePayload{}
	}
}

func (e *env) read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestUpdateNowReplacesBothBinariesAndReExecs(t *testing.T) {
	e := newEnv(t, false)
	res, restart, err := e.svc.Update(t.Context(), "mem_admin",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Status != protocol.ServerUpdateApplying || res.Version != "v0.2.0" || res.RequestedBy != "mem_admin" {
		t.Fatalf("result = %+v, want an applying v0.2.0 by mem_admin", res)
	}
	if restart == nil {
		t.Fatal("Update returned no restart for when=now")
	}
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want %q", got, newServerBinary)
	}
	// The aether beside it is updated too, so the pair never skews.
	if got := e.read(t, e.sibling); got != newCLIBinary {
		t.Fatalf("aether = %q, want %q", got, newCLIBinary)
	}
	// The replaced files stay beside the new ones for a rollback.
	for _, path := range []string{e.self, e.sibling} {
		if got := e.read(t, prevPath(path)); got != "the old binary" {
			t.Fatalf("%s = %q, want the replaced binary", prevPath(path), got)
		}
	}
	for _, want := range []events.ServerUpdatePhase{events.ServerUpdatePreflight, events.ServerUpdateApplying} {
		if p := e.phase(t); p.Phase != want || p.ActorID != "mem_admin" || p.Version != "v0.2.0" {
			t.Fatalf("phase = %+v, want %s v0.2.0 by mem_admin", p, want)
		}
	}

	// Only the new process records applied, once it has booted; until
	// then the swap record says what to verify or roll back.
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Last != nil || state.Pending != nil {
		t.Fatalf("state = %+v, want no outcome and nothing pending before the restart", state)
	}
	rec, err := loadRecord(e.state)
	if err != nil || rec == nil || rec.Phase != recordVerifying || rec.ToVersion != "v0.2.0" || rec.Binaries[0] != e.self {
		t.Fatalf("swap record = %+v, %v; want verifying v0.2.0 starting with %s", rec, err, e.self)
	}

	// The result is written before the restart runs, which is why Update
	// hands the restart back instead of taking it.
	restart()
	if p := e.phase(t); p.Phase != events.ServerUpdateRestarting || p.Version != "v0.2.0" {
		t.Fatalf("phase = %+v, want restarting v0.2.0", p)
	}
	select {
	case call := <-e.execs:
		if call.path != e.self {
			t.Fatalf("exec path = %q, want %q", call.path, e.self)
		}
		if len(call.argv) == 0 || call.argv[0] != e.self {
			t.Fatalf("exec argv = %v, want argv[0] = %q", call.argv, e.self)
		}
		if len(call.env) == 0 {
			t.Fatal("exec passed no environment")
		}
	default:
		t.Fatal("the restart did not re-exec the new binary")
	}
	// The stub exec fails and nothing else would start the new binary, so
	// the swap is undone and recorded with the real error.
	for _, path := range []string{e.self, e.sibling} {
		if got := e.read(t, path); got != "the old binary" {
			t.Fatalf("%s = %q after a failed re-exec, want the old binary back", path, got)
		}
	}
	p := e.phase(t)
	if p.Phase != events.ServerUpdateRolledBack || !strings.Contains(p.Detail, "exec stub") {
		t.Fatalf("phase = %+v, want rolled_back carrying the exec error", p)
	}
	if state, err = e.db.GetServerUpdate(t.Context()); err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateRolledBack || !strings.Contains(state.Last.Detail, "exec stub") {
		t.Fatalf("last after a failed re-exec = %+v, want rolled_back with the real error", state.Last)
	}
	status, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Phase != protocol.ServerUpdatePhaseRolledBack || status.ToVersion != "v0.2.0" || status.Error != state.Last.Detail {
		t.Fatalf("status = %+v, want rolled_back to v0.2.0 with the error", status)
	}
	select {
	case <-e.restarts:
		t.Fatal("a process not under systemd asked systemd to restart it")
	default:
	}
}

func TestUpdateNowRecordsAFailedApply(t *testing.T) {
	e := newEnv(t, true) // poisoned checksums.txt
	_, _, err := e.svc.Update(t.Context(), "mem_admin",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow})
	if err == nil {
		t.Fatal("expected the apply to fail on a checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want the real checksum error", err)
	}
	// A failed apply leaves the running binary alone.
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want the old binary untouched", got)
	}
	e.phase(t) // preflight
	if p := e.phase(t); p.Phase != events.ServerUpdateApplying {
		t.Fatalf("phase = %+v, want applying", p)
	}
	p := e.phase(t)
	if p.Phase != events.ServerUpdateFailed || !strings.Contains(p.Detail, "checksum mismatch") {
		t.Fatalf("second phase = %+v, want failed carrying the real error", p)
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateFailed {
		t.Fatalf("last = %+v, want a failed attempt", state.Last)
	}
	if !strings.Contains(state.Last.Detail, "checksum mismatch") {
		t.Fatalf("last detail = %q, want the real error", state.Last.Detail)
	}
}

func TestUpdateIdleSchedulesOneAtATime(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.1.0", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	res, restart, err := e.svc.Update(t.Context(), "mem_b",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateIdle})
	if err != nil {
		t.Fatalf("second schedule: %v", err)
	}
	if restart != nil {
		t.Fatal("scheduling must not restart anything")
	}
	if res.Status != protocol.ServerUpdateScheduled || res.Version != "v0.2.0" {
		t.Fatalf("result = %+v, want a scheduled v0.2.0", res)
	}
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched until the update applies", got)
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Pending == nil || state.Pending.Version != "v0.2.0" || state.Pending.RequestedBy != "mem_b" {
		t.Fatalf("pending = %+v, want the second request to have replaced the first", state.Pending)
	}
	for _, want := range []domain.MemberID{"mem_a", "mem_b"} {
		if p := e.phase(t); p.Phase != events.ServerUpdateScheduled || p.ActorID != want {
			t.Fatalf("phase = %+v, want scheduled by %s", p, want)
		}
	}
}

func TestUpdateCancelClearsThePending(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.phase(t) // scheduled
	res, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Status != protocol.ServerUpdateCancelled || res.Version != "v0.2.0" {
		t.Fatalf("result = %+v, want cancelled v0.2.0", res)
	}
	if p := e.phase(t); p.Phase != events.ServerUpdateCancelled {
		t.Fatalf("phase = %+v, want cancelled", p)
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Pending != nil {
		t.Fatalf("pending = %+v, want none after a cancel", state.Pending)
	}
	// Cancelling nothing is not an error; the caller asked for no pending
	// update and there is none.
	res, _, err = e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel})
	if err != nil || res.Status != protocol.ServerUpdateCancelled || res.Version != "" {
		t.Fatalf("second cancel = %+v, %v; want an empty cancelled result", res, err)
	}
}

func TestUpdateRejectsWhatIsNotAReleaseTag(t *testing.T) {
	e := newEnv(t, false)
	for _, tag := range []string{"latest", "../../etc/passwd", "https://example.com/x", "v1.2"} {
		_, _, err := e.svc.Update(t.Context(), "mem_a",
			protocol.ServerUpdateParams{Version: tag, When: protocol.ServerUpdateNow})
		if !errors.Is(err, ErrBadTag) {
			t.Fatalf("Update(version=%q) error = %v, want ErrBadTag", tag, err)
		}
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{When: "someday"}); !errors.Is(err, ErrBadWhen) {
		t.Fatalf("Update(when=someday) error = %v, want ErrBadWhen", err)
	}
}

func TestUpdateWithNoVersionResolvesTheLatestRelease(t *testing.T) {
	setVersion(t, "v0.1.0")
	e := newEnv(t, false)
	res, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateIdle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Version != "v0.2.0" {
		t.Fatalf("version = %q, want the latest release v0.2.0", res.Version)
	}
}

// With no version, only a release newer than the running server is an
// update; a release feed with nothing signed yet is no update either.
func TestUpdateWithNoVersionNeverGoesBackwards(t *testing.T) {
	setVersion(t, "v0.3.0")
	e := newEnv(t, false)
	_, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateNow})
	if !errors.Is(err, ErrUpToDate) || !strings.Contains(err.Error(), "aether-server v0.3.0 is not older than the latest release v0.2.0") {
		t.Fatalf("err = %v, want no update", err)
	}
	e.untouched(t)
	e.rel.Unsign()
	if _, _, err = e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("err = %v, want no update for a release without a manifest", err)
	}
	if state, serr := e.db.GetServerUpdate(t.Context()); serr != nil || state.Last != nil {
		t.Fatalf("last = %+v, %v; no update is not a failed attempt", state.Last, serr)
	}
}

// A run that starts while a scheduled release downloads undoes the swap;
// the update stays pending for the next idle moment.
func TestARunStartedDuringTheDownloadUndoesTheSwap(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	calls := 0
	e.svc.cfg.Busy = func(context.Context) domain.ServerBusy {
		calls++
		if calls == 1 {
			return domain.ServerBusy{}
		}
		return domain.ServerBusy{Runs: 1}
	}
	e.svc.Tick(t.Context())
	for _, path := range []string{e.self, e.sibling} {
		if got := e.read(t, path); got != "the old binary" {
			t.Fatalf("%s = %q, want the previous binary back", path, got)
		}
	}
	if rec, err := loadRecord(e.state); err != nil || rec != nil {
		t.Fatalf("swap record = %+v, %v; want none", rec, err)
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Last != nil {
		t.Fatalf("state = %+v, want the update still pending", state)
	}
	select {
	case call := <-e.execs:
		t.Fatalf("re-executed %v while a run was working", call.argv)
	default:
	}
}

func TestTickAppliesThePendingUpdateOnlyWhenIdle(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if p := e.phase(t); p.Phase != events.ServerUpdateScheduled {
		t.Fatalf("phase = %+v, want scheduled", p)
	}

	// A busy server keeps its binary.
	e.busy = domain.ServerBusy{Runs: 1}
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched while a run is active", got)
	}
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Pending == nil {
		t.Fatal("the pending update was consumed by a non-idle tick")
	}

	// Paused and parked runs are not working, so a server holding only
	// those is idle and the update lands.
	e.busy = domain.ServerBusy{Paused: 2}
	// Under systemd a failed re-exec leaves the new binary for the unit
	// restart, so the swap stays visible here.
	e.underSystemd = true
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the new binary", got)
	}
	if p := e.phase(t); p.Phase != events.ServerUpdatePreflight {
		t.Fatalf("phase = %+v, want preflight", p)
	}
	if p := e.phase(t); p.Phase != events.ServerUpdateApplying {
		t.Fatalf("phase = %+v, want applying", p)
	}
	if p := e.phase(t); p.Phase != events.ServerUpdateRestarting {
		t.Fatalf("phase = %+v, want restarting", p)
	}
	select {
	case <-e.execs:
	default:
		t.Fatal("the idle tick did not re-exec the new binary")
	}
	if state, err = e.db.GetServerUpdate(t.Context()); err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if state.Pending != nil {
		t.Fatalf("pending = %+v, want none once the update applied", state.Pending)
	}

	// Nothing is pending any more, so a later idle tick is a no-op.
	e.svc.Tick(t.Context())
	select {
	case call := <-e.execs:
		t.Fatalf("an idle tick with nothing pending re-executed %v", call.argv)
	default:
	}
}

func TestTickWithNothingPendingDoesNothing(t *testing.T) {
	e := newEnv(t, false)
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched", got)
	}
}

func TestRestartFallsBackToSystemdWhenExecFails(t *testing.T) {
	e := newEnv(t, false)
	e.underSystemd = true
	_, restart, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	e.phase(t) // preflight
	e.phase(t) // applying
	restart()
	e.phase(t) // restarting
	select {
	case <-e.restarts:
	default:
		t.Fatal("a failed re-exec under systemd did not fall back to systemctl restart")
	}
	// The restarted unit runs the new binary, whose own start decides.
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the new binary left for systemd to start", got)
	}
}

func TestStatusReportsTheWholeState(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	got, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.Capable {
		t.Fatal("a writable binary directory must report capable")
	}
	if got.ManualCommands != nil {
		t.Fatalf("manual commands = %v, want none while the server can update itself", got.ManualCommands)
	}
	if got.Pending == nil || got.Pending.Version != "v0.2.0" || got.Pending.RequestedBy != "mem_a" {
		t.Fatalf("pending = %+v, want v0.2.0 by mem_a", got.Pending)
	}
	if got.Pending.RequestedAt != e.now.Format(time.RFC3339) {
		t.Fatalf("requested at = %q, want %q", got.Pending.RequestedAt, e.now.Format(time.RFC3339))
	}
	if got.Last != nil {
		t.Fatalf("last = %+v, want none before any attempt", got.Last)
	}
}

func TestIncapableServerRefusesAndNamesTheManualCommands(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	e := newEnv(t, false)
	if err := os.Chmod(e.dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.dir, 0o755) })

	if e.svc.Capable() {
		t.Fatal("a read-only binary directory must report incapable")
	}
	_, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow})
	if !errors.Is(err, ErrIncapable) {
		t.Fatalf("Update error = %v, want ErrIncapable", err)
	}
	got, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Capable {
		t.Fatal("status reports capable on a read-only binary directory")
	}
	want := ManualCommands()
	if len(got.ManualCommands) != len(want) {
		t.Fatalf("manual commands = %v, want %v", got.ManualCommands, want)
	}
	for i, cmd := range want {
		if got.ManualCommands[i] != cmd {
			t.Fatalf("manual commands = %v, want %v", got.ManualCommands, want)
		}
	}
}

// A cancel must work on a server that cannot update itself: nothing can be
// pending there, but refusing to say so would be a dead end.
func TestCancelIsAllowedOnAnIncapableServer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	e := newEnv(t, false)
	if err := os.Chmod(e.dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.dir, 0o755) })
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

// A companion that fails verification must leave both binaries alone: a
// half-updated pair would have the CLI and the server disagreeing about
// the protocol, which is worse than no update at all.
func TestAFailedCompanionLeavesBothBinariesUntouched(t *testing.T) {
	e := newEnv(t, false)
	e.poisonCompanion()

	_, _, err := e.svc.Update(t.Context(), "mem_admin",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow})
	if err == nil {
		t.Fatal("expected the apply to fail on the companion's checksum")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want the real checksum error", err)
	}
	for _, path := range []string{e.self, e.sibling} {
		if got := e.read(t, path); got != "the old binary" {
			t.Fatalf("%s = %q, want it untouched", path, got)
		}
	}
	// Nothing staged is left lying around either.
	entries, rerr := os.ReadDir(e.dir)
	if rerr != nil {
		t.Fatalf("read %s: %v", e.dir, rerr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".aether") {
			t.Fatalf("a staged file survived the failure: %s", entry.Name())
		}
	}
	state, serr := e.db.GetServerUpdate(t.Context())
	if serr != nil {
		t.Fatalf("GetServerUpdate: %v", serr)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateFailed {
		t.Fatalf("last = %+v, want a failed attempt", state.Last)
	}
}

// A pending update that has not applied says what it is waiting for, so an
// admin is never left watching nothing happen. Paused runs are listed but
// do not hold it back.
func TestStatusSaysWhatAPendingUpdateIsWaitingFor(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.busy = domain.ServerBusy{Runs: 2, Paused: 1, Shells: 3}
	got, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Waiting == nil {
		t.Fatal("a pending update on a busy server reports nothing to wait for")
	}
	if *got.Waiting != (protocol.ServerUpdateWaiting{Runs: 2, Paused: 1, Shells: 3}) {
		t.Fatalf("waiting = %+v, want 2 runs, 1 paused, 3 shells", *got.Waiting)
	}

	// Only paused runs left: nothing is holding it back any more.
	e.busy = domain.ServerBusy{Paused: 1}
	if got, err = e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Waiting != nil {
		t.Fatalf("waiting = %+v, want none once only paused runs remain", *got.Waiting)
	}
}

// Nothing pending means nothing to wait for, and the run table is never
// scanned for it.
func TestStatusReportsNoWaitWithoutAPendingUpdate(t *testing.T) {
	e := newEnv(t, false)
	e.busy = domain.ServerBusy{Runs: 5}
	got, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Waiting != nil {
		t.Fatalf("waiting = %+v with nothing pending, want none", *got.Waiting)
	}
}

// A server that cannot tell what it is doing is never idle.
func TestAnUnknownBusyStateNeverApplies(t *testing.T) {
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.busy = domain.ServerBusy{Unknown: true}
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched on an unknown busy state", got)
	}
}

// A held server applies nothing, from the RPC or the idle poll, and leaves
// the binaries alone.
func TestAHeldServerDoesNotApply(t *testing.T) {
	e := newEnv(t, false)
	e.hold = "Sysbox is installing"
	_, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow})
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "Sysbox is installing") {
		t.Fatalf("Update now = %v, want ErrHeld naming the reason", err)
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched while held", got)
	}
}

// A trial that has not committed owns swap.json until its Commit, which
// would overwrite a new update's record: no update starts before then.
func TestAnUncommittedTrialRefusesANewUpdate(t *testing.T) {
	e := newEnv(t, false)
	if err := saveRecord(e.state, &swapRecord{Phase: recordVerifying, FromVersion: "v0.1.0", ToVersion: "v0.1.5", Binaries: []string{e.self}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "v0.1.5 is still verifying its first start") {
		t.Fatalf("Update now = %v, want ErrBusy naming the uncommitted trial", err)
	}
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched", got)
	}
	rec, err := loadRecord(e.state)
	if err != nil || rec == nil || rec.ToVersion != "v0.1.5" || rec.Phase != recordVerifying {
		t.Fatalf("swap record = %+v, %v; want the trial's own", rec, err)
	}
}

func TestServerRunningAReplacedBinaryAsksForARestart(t *testing.T) {
	e := newEnv(t, false)
	next := e.self + ".next"
	if err := os.WriteFile(next, []byte("the release `aether update` installed"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Rename(next, e.self); err != nil {
		t.Fatalf("replace the binary: %v", err)
	}

	_, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow})
	if !errors.Is(err, ErrIncapable) || !strings.Contains(err.Error(), "sudo systemctl restart aether-server") {
		t.Fatalf("Update error = %v, want ErrIncapable naming the restart", err)
	}
	got, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Capable || !strings.Contains(got.Incapable, "was replaced on disk") {
		t.Fatalf("status = capable %v, incapable %q", got.Capable, got.Incapable)
	}
	data, err := os.ReadFile(e.self + ".prev")
	if err == nil {
		t.Fatalf("a refused update must not keep %s as .prev", data)
	}
}
