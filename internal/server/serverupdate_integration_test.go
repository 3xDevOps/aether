//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

const (
	updatedServerBinary = "aether-server from the release"
	updatedCLIBinary    = "aether from the release"
)

// stubRelease serves the signed release for tag the way GitHub does, so
// the whole update path - resolve, verify, download, swap - runs for real
// against something the test controls.
func stubRelease(t *testing.T, tag string, omit ...string) *releasetest.Release {
	t.Helper()
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	assets := map[string][]byte{}
	for _, name := range []string{
		"aether-linux-amd64", "aether-linux-arm64", "aether-darwin-amd64", "aether-darwin-arm64",
		"aether-windows-amd64.exe", "aether-windows-arm64.exe", "aether-android.apk",
	} {
		assets[name] = []byte("client " + name)
	}
	assets["aether-server"+suffix] = []byte(updatedServerBinary)
	assets["aether"+suffix] = []byte(updatedCLIBinary)
	for _, name := range omit {
		delete(assets, name)
	}
	return releasetest.Serve(t, tag, assets)
}

// TestIntegrationServerUpdateAppliesWhenIdle drives the scheduled path end
// to end through a wired server: an admin schedules an update while a real
// containerized run is working, the poll loop leaves it alone, and the
// moment the run is explicitly resolved the update applies, publishes its
// phases, replaces both binaries, and re-executes.
//
// The re-exec itself is the one thing that cannot run here - it would
// replace the test process - so Config.SelfUpdate supplies an exec that
// records the call and then blocks, exactly as syscall.Exec never returns.
func TestIntegrationServerUpdateAppliesWhenIdle(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, _, verifyNoLeaks := pickRuntime(t)
	dataDir := filepath.Join(shortTempDir(t), "data")

	// The binaries the update replaces. They are inert files, not the test
	// process: the service is told where they are.
	binDir := t.TempDir()
	serverBin := filepath.Join(binDir, "aether-server")
	cliBin := filepath.Join(binDir, "aether")
	for _, path := range []string{serverBin, cliBin} {
		if err := os.WriteFile(path, []byte("the installed binary"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	release := stubRelease(t, "v9.9.9")
	execed := make(chan []string, 1)
	// The exec stub holds the poll loop the way syscall.Exec does - by not
	// returning - until the assertions have read the swapped binaries. The
	// test process is never replaced, so it releases and lets the stub
	// report the one thing a real exec only reports on failure.
	hold := make(chan struct{})
	var releaseOnce sync.Once
	releaseExec := func() { releaseOnce.Do(func() { close(hold) }) }
	t.Cleanup(releaseExec)

	srv, err := New(ctx, Config{
		DataDir:         dataDir,
		Addr:            "127.0.0.1:0",
		Runtime:         rt,
		RunContainerTTL: -time.Second,
		PollInterval:    200 * time.Millisecond,
		SelfUpdate: serverupdate.Config{
			Checker:    selfupdate.NewChecker(release.URL, time.Hour),
			Executable: serverBin,
			// Every host control is injected. A nil one would fall back to
			// nothing - serverupdate.New treats a partial Host as none -
			// but the whole set is spelled out so this test can never
			// reach the developer's own systemd, and so a reader sees
			// exactly which side effects it has replaced.
			Host: serverupdate.Host{
				Exec: func(_ string, argv, _ []string) error {
					execed <- argv
					<-hold
					return errors.New("the test process is never replaced")
				},
				Restart: func() error {
					t.Error("the test asked the host's systemd to restart aether-server")
					return errors.New("refused: this is a test")
				},
				UnderSystemd: func() bool { return false },
			},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Update Admin",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{
		Name:        "update",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "hold the server busy", Harness: "fake",
		Mode: string(domain.LaunchTUI),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := domain.RunID(launched.Run.ID)
	att := openAttach(t, client, launched.Run.ID)
	att.waitOutput(t, "agent-ready")

	// The status method answers before anything is scheduled.
	var status protocol.ServerUpdateStatusResult
	if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	if !status.Capable || status.Pending != nil {
		t.Fatalf("status before scheduling = %+v, want capable with nothing pending", status)
	}

	var scheduled protocol.ServerUpdateResult
	if err := ctrl.Call(protocol.MethodServerUpdate, protocol.ServerUpdateParams{
		Version: "v9.9.9", When: protocol.ServerUpdateIdle,
	}, &scheduled); err != nil {
		t.Fatalf("server.update: %v", err)
	}
	if scheduled.Status != protocol.ServerUpdateScheduled || scheduled.Version != "v9.9.9" {
		t.Fatalf("schedule result = %+v, want a scheduled v9.9.9", scheduled)
	}
	phaseOf := func(want events.ServerUpdatePhase) func(events.Event) bool {
		return func(e events.Event) bool {
			p, ok := e.Payload.(events.ServerUpdatePayload)
			return ok && p.Phase == want
		}
	}
	ev := waitEvent(t, sub, &seen, "server.update scheduled", phaseOf(events.ServerUpdateScheduled))
	if ev.ActorID != member.ID {
		t.Fatalf("scheduled event actor = %q, want %q", ev.ActorID, member.ID)
	}

	// Several poll intervals pass with the run working; the binary must
	// still be the installed one.
	time.Sleep(2 * time.Second)
	if body, rerr := os.ReadFile(serverBin); rerr != nil || string(body) != "the installed binary" {
		t.Fatalf("aether-server = %q (%v) while a run was working, want it untouched", body, rerr)
	}
	if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	if status.Pending == nil || status.Pending.Version != "v9.9.9" || status.Pending.RequestedBy != string(member.ID) {
		t.Fatalf("pending = %+v, want v9.9.9 by the admin", status.Pending)
	}
	// The admin can see why it has not applied instead of watching
	// nothing happen.
	if status.Waiting == nil || status.Waiting.Runs != 1 {
		t.Fatalf("waiting = %+v, want it to name the one working run", status.Waiting)
	}

	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: launched.Run.ID, Message: "done", IdempotencyKey: "serverupdate-done",
	}, nil); err != nil {
		t.Fatalf("run.inject harness completion: %v", err)
	}

	// A clean TUI harness exit leaves a reusable login shell, so the run
	// remains active until the operator explicitly resolves it.
	att.waitOutput(t, "[aether] harness exited with code 0")
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: launched.Run.ID, Message: "printf 'update-login-shell-ready\\n'", IdempotencyKey: "serverupdate-login",
	}, nil); err != nil {
		t.Fatalf("run.inject login-shell probe: %v", err)
	}
	att.waitOutput(t, "update-login-shell-ready")
	var active protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(runID)}, &active); err != nil {
		t.Fatalf("run.get after harness exit: %v", err)
	}
	if active.Run.Status != string(domain.RunRunning) {
		t.Fatalf("run status after harness exit = %q, want running", active.Run.Status)
	}
	var closed protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunClose, protocol.RunCloseParams{
		RunID: string(runID), Outcome: string(domain.RunMerged),
	}, &closed); err != nil {
		t.Fatalf("run.close: %v", err)
	}
	if closed.Run.Status != string(domain.RunMerged) {
		t.Fatalf("closed run status = %q, want merged", closed.Run.Status)
	}
	att.close()
	waitEvent(t, sub, &seen, "run.status merged", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.To == domain.RunMerged
	})

	waitEvent(t, sub, &seen, "server.update applying", phaseOf(events.ServerUpdateApplying))
	waitEvent(t, sub, &seen, "server.update restarting", phaseOf(events.ServerUpdateRestarting))

	select {
	case argv := <-execed:
		if len(argv) == 0 || argv[0] != serverBin {
			t.Fatalf("re-exec argv = %v, want argv[0] = %q", argv, serverBin)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the idle update never re-executed the new binary")
	}
	if body, rerr := os.ReadFile(serverBin); rerr != nil || string(body) != updatedServerBinary {
		t.Fatalf("aether-server = %q (%v), want the release build", body, rerr)
	}
	if body, rerr := os.ReadFile(cliBin); rerr != nil || string(body) != updatedCLIBinary {
		t.Fatalf("aether = %q (%v), want the release build", body, rerr)
	}
	// The update is not applied until the new binary boots and commits
	// it; until then the server reports the restart. Decoded into a fresh
	// result: an absent omitempty field leaves the destination's old value
	// in place, so reusing `status` here would assert against the pending
	// update this call is meant to prove gone.
	var restarting protocol.ServerUpdateStatusResult
	if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &restarting); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	if restarting.Pending != nil {
		t.Fatalf("pending = %+v, want none once the binaries are swapped", restarting.Pending)
	}
	if restarting.Waiting != nil {
		t.Fatalf("waiting = %+v, want none with nothing pending", restarting.Waiting)
	}
	if restarting.Phase != protocol.ServerUpdatePhaseRestarting || restarting.ToVersion != "v9.9.9" {
		t.Fatalf("status = %+v, want restarting to v9.9.9", restarting)
	}
	if restarting.Last != nil {
		t.Fatalf("last = %+v, want no outcome before the new binary boots", restarting.Last)
	}

	releaseExec()
	stopServer()
	if err := <-runDone; err != nil {
		t.Fatalf("server.Run: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("server.Close: %v", err)
	}
	verifyNoLeaks(t)
}

// updateHarness is one data directory and one pair of installed binaries
// that successive server processes share, the way a host keeps them
// across the restart an update causes. Each start plays one process:
// the old binary, the new one, or the old one restored.
type updateHarness struct {
	dataDir, serverBin, cliBin string
	release                    *releasetest.Release
	signer                     ssh.Signer
	member                     *domain.Member
	execs                      chan string
	drained                    chan []string
	// skew moves the update service's clock forward.
	skew atomic.Int64
}

func newUpdateHarness(t *testing.T, release *releasetest.Release) *updateHarness {
	t.Helper()
	binDir := t.TempDir()
	h := &updateHarness{
		dataDir:   filepath.Join(shortTempDir(t), "data"),
		serverBin: filepath.Join(binDir, "aether-server"),
		cliBin:    filepath.Join(binDir, "aether"),
		release:   release,
		execs:     make(chan string, 4),
		drained:   make(chan []string, 4),
	}
	for _, path := range []string{h.serverBin, h.cliBin} {
		if err := os.WriteFile(path, []byte("the installed binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, h.signer = writeClientKey(t)
	return h
}

func (h *updateHarness) host(t *testing.T) serverupdate.Host {
	return serverupdate.Host{
		Exec: func(path string, _, _ []string) error {
			h.execs <- path
			return errors.New("the test process is never replaced")
		},
		Restart: func() error {
			t.Error("the test asked the host's systemd to restart aether-server")
			return errors.New("refused: this is a test")
		},
		UnderSystemd: func() bool { return false },
	}
}

// running is one server process of the harness.
type running struct {
	srv   *Server
	ctrl  *protocol.Client
	trial *serverupdate.Trial
	stop  func() error
}

// start boots a server process reporting ver, through the same steps
// cmd/aether-server takes: the trial first, then the server and the
// trial's deadline, then the commit once SSH is bound and runs recovered.
func (h *updateHarness) start(t *testing.T, ctx context.Context, ver string) *running {
	t.Helper()
	setServerVersion(t, ver)
	trial, err := serverupdate.Boot(UpdateStateDir(h.dataDir), h.host(t))
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	srv, err := New(runCtx, Config{
		DataDir:      h.dataDir,
		Addr:         "127.0.0.1:0",
		Runtime:      newE2ERuntime(),
		PollInterval: 200 * time.Millisecond,
		SelfUpdate: serverupdate.Config{
			Checker:    selfupdate.NewChecker(h.release.URL, time.Hour),
			Executable: h.serverBin,
			Host:       h.host(t),
			Now:        func() time.Time { return time.Now().Add(time.Duration(h.skew.Load())) },
			Drain: func(argv []string) {
				h.drained <- argv
				cancel()
			},
		},
	})
	if err != nil {
		cancel()
		t.Fatalf("server.New: %v", err)
	}
	if _, err = trial.Migrated(); err != nil {
		cancel()
		t.Fatalf("Migrated: %v", err)
	}
	if h.member == nil {
		h.member = &domain.Member{
			DisplayName: "Update Admin",
			PublicKey:   string(ssh.MarshalAuthorizedKey(h.signer.PublicKey())),
			Color:       "#e6194b",
			Role:        domain.RoleAdmin,
		}
		if err := srv.Store().CreateMember(ctx, h.member); err != nil {
			t.Fatalf("seed member: %v", err)
		}
		if err := srv.Store().CreateWorkspace(ctx, &domain.Workspace{Name: "update", BaseBranch: domain.DefaultBaseBranch}); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)
	<-srv.Recovered()
	if err := trial.Commit(ctx, srv.Store(), srv.Bus()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var once sync.Once
	var runErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			runErr = <-done
		})
		return runErr
	}
	t.Cleanup(func() { _ = stop() })
	return &running{srv: srv, ctrl: openControl(t, dialSSH(t, addr, h.signer)), trial: trial, stop: stop}
}

func setServerVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func (r *running) status(t *testing.T) protocol.ServerUpdateStatusResult {
	t.Helper()
	var out protocol.ServerUpdateStatusResult
	if err := r.ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &out); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	return out
}

func (r *running) update(p protocol.ServerUpdateParams) error {
	if p.When == "" {
		p.When = protocol.ServerUpdateNow
	}
	return r.ctrl.Call(protocol.MethodServerUpdate, p, nil)
}

func readBinary(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func wantCode(t *testing.T, err error, code int, contains ...string) {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("err = %v, want code %d", err, code)
	}
	for _, s := range contains {
		if !strings.Contains(pe.Message, s) {
			t.Fatalf("message %q does not contain %q", pe.Message, s)
		}
	}
}

// The preflight refuses a release whose clients cannot follow, naming the
// asset and its URL, before anything is downloaded.
func TestIntegrationServerUpdatePreflightNamesTheMissingClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for name, tc := range map[string]struct {
		omit   []string
		tamper string
		want   []string
	}{
		"unlisted": {omit: []string{"aether-android.apk"}, want: []string{"v0.2.0 does not publish aether-android.apk"}},
		"size":     {tamper: "aether-windows-arm64.exe", want: []string{"aether-windows-arm64.exe (", "/releases/download/v0.2.0/aether-windows-arm64.exe): Content-Length is 1"}},
	} {
		t.Run(name, func(t *testing.T) {
			release := stubRelease(t, "v0.2.0", tc.omit...)
			if tc.tamper != "" {
				release.Tamper(tc.tamper, []byte("x"))
			}
			h := newUpdateHarness(t, release)
			old := h.start(t, ctx, "v0.1.0")
			wantCode(t, old.update(protocol.ServerUpdateParams{Version: "v0.2.0"}), protocol.CodeInvalidState, tc.want...)
			if readBinary(t, h.serverBin) != "the installed binary" {
				t.Fatal("a refused update replaced the server binary")
			}
			if st := old.status(t); st.Phase != protocol.ServerUpdatePhaseFailed || !strings.Contains(st.Error, tc.want[0]) {
				t.Fatalf("status = %+v, want failed with the preflight error", st)
			}
		})
	}
}

// An older tag needs allow_downgrade; an older store schema is refused
// whatever the flag says.
func TestIntegrationServerUpdateDowngradeGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release := stubRelease(t, "v0.2.0")
	h := newUpdateHarness(t, release)
	srv := h.start(t, ctx, "v0.3.0")
	wantCode(t, srv.update(protocol.ServerUpdateParams{Version: "v0.2.0"}), protocol.CodeInvalidParams,
		"v0.2.0 is older than the running v0.3.0", "--allow-downgrade")

	release.SetStoreSchema(store.SchemaVersion() - 1)
	wantCode(t, srv.update(protocol.ServerUpdateParams{Version: "v0.2.0", AllowDowngrade: true}), protocol.CodeInvalidParams,
		"this database is at schema")

	release.SetStoreSchema(store.SchemaVersion())
	if err := srv.update(protocol.ServerUpdateParams{Version: "v0.2.0", AllowDowngrade: true}); err != nil {
		t.Fatalf("allowed downgrade: %v", err)
	}
	if readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatal("the allowed downgrade did not install the release")
	}
}

// The whole trial: the old process swaps and drains, the new binary fails
// to boot until its next start rolls back with the real error, and the
// restored binary records it. A second update then boots and the new
// process records applied.
func TestIntegrationServerUpdateRollsBackAndCommits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release := stubRelease(t, "v0.2.0")
	release.SetStoreSchema(store.SchemaVersion() + 1)
	h := newUpdateHarness(t, release)
	stateDir := UpdateStateDir(h.dataDir)

	old := h.start(t, ctx, "v0.1.0")
	if err := old.update(protocol.ServerUpdateParams{Version: "v0.2.0"}); err != nil {
		t.Fatalf("server.update: %v", err)
	}
	argv := <-h.drained
	if err := old.stop(); err != nil {
		t.Fatalf("drained server.Run: %v", err)
	}
	if argv[0] != h.serverBin || readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatalf("drained argv %v with %q installed, want the new binary", argv, readBinary(t, h.serverBin))
	}

	// The new binary fails its first starts with a real error; the start
	// after the limit puts the old binaries back and executes them.
	boot := errors.New("store: apply migration 999: no such table: widgets")
	setServerVersion(t, "v0.2.0")
	for range 2 {
		trial, err := serverupdate.Boot(stateDir, h.host(t))
		if err != nil {
			t.Fatalf("trial start: %v", err)
		}
		_ = trial.Fail(boot)
	}
	if _, err := serverupdate.Boot(stateDir, h.host(t)); err == nil {
		t.Fatal("the start past the limit did not roll back")
	}
	if path := <-h.execs; path != h.serverBin {
		t.Fatalf("rollback executed %s, want %s", path, h.serverBin)
	}
	if readBinary(t, h.serverBin) != "the installed binary" || readBinary(t, h.cliBin) != "the installed binary" {
		t.Fatal("the rollback did not restore the previous binaries")
	}

	restored := h.start(t, ctx, "v0.1.0")
	st := restored.status(t)
	if st.Phase != protocol.ServerUpdatePhaseRolledBack || st.FromVersion != "v0.1.0" || st.ToVersion != "v0.2.0" ||
		!strings.Contains(st.Error, boot.Error()) {
		t.Fatalf("status after the rollback = %+v, want rolled_back v0.1.0 -> v0.2.0 with the real error", st)
	}
	if st.Last == nil || st.Last.Outcome != protocol.ServerUpdateRolledBack {
		t.Fatalf("last = %+v, want rolled_back", st.Last)
	}

	// The fixed release boots: the new process, and only it, records
	// applied.
	release.SetStoreSchema(store.SchemaVersion())
	if err := restored.update(protocol.ServerUpdateParams{Version: "v0.2.0"}); err != nil {
		t.Fatalf("server.update: %v", err)
	}
	<-h.drained
	if err := restored.stop(); err != nil {
		t.Fatalf("drained server.Run: %v", err)
	}
	updated := h.start(t, ctx, "v0.2.0")
	st = updated.status(t)
	if st.Phase != protocol.ServerUpdatePhaseApplied || st.FromVersion != "v0.1.0" || st.ToVersion != "v0.2.0" || st.Error != "" {
		t.Fatalf("status after the new binary booted = %+v, want applied v0.1.0 -> v0.2.0", st)
	}
	if st.Last == nil || st.Last.Outcome != protocol.ServerUpdateApplied || st.Last.Version != "v0.2.0" {
		t.Fatalf("last = %+v, want applied v0.2.0", st.Last)
	}
}

// An update interrupted mid-download is still pending when the server
// comes back, and the first idle poll applies it.
func TestIntegrationServerUpdateRetriesFromPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h := newUpdateHarness(t, stubRelease(t, "v0.2.0"))
	first := h.start(t, ctx, "v0.1.0")
	// What an update that died mid-download leaves: the pending row it
	// wrote before downloading, and nothing replaced.
	if err := first.srv.Store().SetPendingServerUpdate(ctx, &store.PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: h.member.ID, RequestedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}
	h.start(t, ctx, "v0.1.0")
	select {
	case <-h.drained:
	case <-time.After(30 * time.Second):
		t.Fatal("the pending update was not retried after the restart")
	}
	if readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatal("the retried update did not install the release")
	}
}

// The server checks for releases itself: a failed check is reported
// verbatim with its backoff persisted, and a refresh after the release
// appears clears it.
func TestIntegrationServerUpdateBackgroundCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release := stubRelease(t, "v0.2.0")
	release.SetLatest("")
	h := newUpdateHarness(t, release)
	srv := h.start(t, ctx, "v0.1.0")

	var st protocol.ServerUpdateStatusResult
	deadline := time.Now().Add(10 * time.Second)
	for st = srv.status(t); st.CheckedAt == ""; st = srv.status(t) {
		if time.Now().After(deadline) {
			t.Fatal("the background check never ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.CheckError == "" || !strings.Contains(st.CheckError, "503") {
		t.Fatalf("status = %+v, want the check's own error", st)
	}
	data, err := os.ReadFile(filepath.Join(UpdateStateDir(h.dataDir), "check.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Failures int       `json:"failures"`
		NextAt   time.Time `json:"next_at"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(persisted.NextAt); persisted.Failures != 1 || wait < 4*time.Minute || wait > 6*time.Minute {
		t.Fatalf("persisted check = %+v (retry in %s), want one failure retried in about 5m", persisted, wait)
	}

	release.SetLatest("v0.2.0")
	var fresh protocol.ServerUpdateStatusResult
	if err := srv.ctrl.Call(protocol.MethodServerUpdateStatus, protocol.ServerUpdateStatusParams{Refresh: true}, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.CheckError != st.CheckError {
		t.Fatalf("a refresh right after a check = %+v, want that check's answer", fresh)
	}
	h.skew.Store(int64(time.Minute))
	fresh = protocol.ServerUpdateStatusResult{}
	if err := srv.ctrl.Call(protocol.MethodServerUpdateStatus, protocol.ServerUpdateStatusParams{Refresh: true}, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.CheckError != "" || fresh.LatestVersion != "v0.2.0" || !fresh.UpdateAvailable || fresh.NotesURL == "" {
		t.Fatalf("status after a refresh = %+v, want v0.2.0 available", fresh)
	}
}
