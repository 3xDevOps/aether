//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// The host half of the conflict-coordination E2E. Both scenarios drive the
// whole wired server over real SSH - control channel, attach, event bus,
// radar, coordination sockets - against the in-process runtime rather than
// Docker, because their agents reach the surfaces a container was given
// from the test process. Everything else on the path is the real thing.
// The container half is coordination_container_integration_test.go.

// TestIntegrationCoordinationEndToEnd is the release gate: two overlapping
// runs on a registered harness manually invoke the MCP bridge, settle the
// overlap through their own coordination sockets, and leave attributed
// timeline entries. A taskless fixture receives the same ordinary assets but
// does not invoke MCP.
func TestIntegrationCoordinationEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")

	e, srv := newCoordEnv(ctx, t, false)
	release := make(chan struct{})
	defer close(release)

	const (
		taskA = "coordinate A"
		taskB = "coordinate B"
		taskC = "coordinate C"
		bodyA = "rewriting login(); done in ~10 min"
		bodyB = "only adding an import - going ahead"
	)
	e.e2e(t).script(taskA, func(c *e2eContainer) { coordAgent{peer: taskB, body: bodyA, release: release}.run(ctx, c) })
	e.e2e(t).script(taskB, func(c *e2eContainer) { coordAgent{peer: taskA, body: bodyB, release: release}.run(ctx, c) })
	e.e2e(t).script(taskC, func(c *e2eContainer) { coordAgent{release: release}.run(ctx, c) })

	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	boCtrl, boClient := srv.control(t, e.bo.key)

	runA := e.launch(t, adaCtrl, taskA, "claude")
	runB := e.launch(t, boCtrl, taskB, "claude")
	runC := e.launch(t, adaCtrl, taskC, "fake")
	attA := openAttach(t, adaClient, runA.ID)
	attB := openAttach(t, boClient, runB.ID)
	attC := openAttach(t, adaClient, runC.ID)

	e.assertRegistered(t, runA)
	e.assertRegistered(t, runB)
	e.assertNoticeOnly(t, runC)

	// Every overlapping agent is told in its own terminal. The registered
	// fixtures then manually invoke MCP; the taskless fixture only observes.
	for _, att := range []*attachConn{attA, attB, attC} {
		att.waitOutput(t, "aether injects")
		att.waitOutput(t, "notice:aether: Overlap: run ")
	}
	attA.waitOutput(t, "assets:manual-mcp")
	attB.waitOutput(t, "assets:manual-mcp")
	attC.waitOutput(t, "assets:manual-mcp")
	// The two registered agents settle it between themselves, each message
	// travelling agent -> MCP tool -> bridge -> its own socket -> mailbox.
	attA.waitOutput(t, "inbox:"+bodyB)
	attB.waitOutput(t, "inbox:"+bodyA)

	// The whole exchange is on the workspace timeline under the run where
	// each server-originated notice or message happened.
	waitEvent(t, sub, &seen, "run A's notice entry", coordNoticeNote(runA.ID, runB.ID))
	waitEvent(t, sub, &seen, "run B's notice entry", coordNoticeNote(runB.ID, runA.ID))
	waitEvent(t, sub, &seen, "run A's coordination note", coordNote(runA.ID, runB.ID))
	waitEvent(t, sub, &seen, "run B's coordination note", coordNote(runB.ID, runA.ID))

	// The taskless fixture intentionally did not message peers.
	if out := attC.output(); strings.Contains(out, "inbox:") || strings.Contains(out, "sent:") {
		t.Errorf("the taskless harness exchanged messages: %q", out)
	}
	for _, att := range []*attachConn{attA, attB, attC} {
		assertNoAgentError(t, att)
	}
	waitOverlap(t, adaCtrl, runA.ID, runB.ID)
}

// TestIntegrationCoordinationKillSwitch walks the switch through its three
// interesting positions against one data directory and one set of live
// containers: cold start off, off -> on, and on -> off.
func TestIntegrationCoordinationKillSwitch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	e, srv := newCoordEnv(ctx, t, true)
	release := make(chan struct{})
	defer close(release)

	const taskA, taskB, taskC = "kill switch A", "kill switch B", "kill switch C"
	for _, task := range []string{taskA, taskB, taskC} {
		e.e2e(t).script(task, func(c *e2eContainer) { coordAgent{release: release}.run(ctx, c) })
	}

	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	boCtrl, boClient := srv.control(t, e.bo.key)
	runA := e.launch(t, adaCtrl, taskA, "claude")
	runB := e.launch(t, boCtrl, taskB, "claude")
	attA := openAttach(t, adaClient, runA.ID)
	attB := openAttach(t, boClient, runB.ID)

	// Cold start with the switch off: the image still receives the staged CLI,
	// but no socket or coordination directory is provisioned.
	attA.waitOutput(t, "assets:none")
	attB.waitOutput(t, "assets:none")
	e.assertNoCoordination(t, runA)
	e.assertNoCoordination(t, runB)
	if _, err := os.Stat(filepath.Join(e.dataDir, "coord")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("coordination directory exists with coordination off (stat error %v)", err)
	}
	// The radar still reacts, and it is the only coordination side effect.
	waitOverlap(t, adaCtrl, runA.ID, runB.ID)
	assertNoNotice(t, attA, attB)
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID)
	drain(sub, &seen)
	assertNoCoordNote(t, seen)
	// A swarm's integrator and workers talk over the same mailbox, so
	// mission.create names the switch that took it away.
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(e.ada.id), Harness: "claude", Mode: string(domain.LaunchTUI)}
	createErr := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: "swarm with coordination off", IdempotencyKey: "kill-switch-swarm",
		Integrator:            protocol.MissionIntegrator(integrator),
		ExecutionChoices:      []protocol.MissionExecutionChoice{integrator},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
	}, nil)
	const wantCreate = "swarms need conflict coordination; the server was started with --conflict-coordination=false: scheduler: coordination is unavailable"
	if createErr == nil || !strings.Contains(createErr.Error(), wantCreate) {
		t.Errorf("mission.create with coordination off = %v, want %q", createErr, wantCreate)
	}

	// Off -> on. Only a new run gains the bridge; the two containers that
	// predate the switch keep exactly what they were given.
	srv.stop()
	srv = e.start(ctx, t, false)
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, adaClient = srv.control(t, e.ada.key)
	attA2 := waitAttach(t, adaClient, runA.ID)

	runC := e.launch(t, adaCtrl, taskC, "claude")
	attC := openAttach(t, adaClient, runC.ID)
	attC.waitOutput(t, "assets:manual-mcp")
	e.assertRegistered(t, runC)
	e.assertNoCoordination(t, runA)
	e.assertNoCoordination(t, runB)
	attA2.waitOutput(t, "notice:aether: Overlap: run ")
	attC.waitOutput(t, "notice:aether: Overlap: run ")

	// On -> off. Run C's already-created container retains its read-only
	// mounts, but the service unlinks the socket on recovery.
	srv.stop()
	dir := e.coordDir(runC.ID)
	srv = e.start(ctx, t, true)
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, _ = srv.control(t, e.ada.key)
	e.assertRegistered(t, runC)
	if _, serr := os.Stat(filepath.Join(dir, coordtransport.SocketName)); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("the coordination socket survived the switch being turned off (stat error %v)", serr)
	}
	assertToolsUnavailable(ctx, t, filepath.Join(dir, coordtransport.SocketName), runA.ID)

	// No side effect anywhere, and the radar is still exactly as it was.
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID, runC.ID)
	drain(sub, &seen)
	assertNoCoordNote(t, seen)
	waitOverlap(t, adaCtrl, runC.ID, runA.ID)
}

// coordEnv is the fixture the coordination scenarios share: one data
// directory and one runtime, so the server can be restarted with the kill
// switch in a different position while the containers it left behind stay
// alive.
type coordEnv struct {
	rt    runtime.Runtime
	image string
	// serverBinary is what the scheduler stages as the in-container bridge;
	// empty stages the running binary, which under `go test` is the test
	// binary and has no mcp subcommand.
	serverBinary string
	dataDir      string
	keyPath      string

	ws  *domain.Workspace
	ada coordMember
	bo  coordMember
}

// coordMember is one seeded member and the key its client connects with.
type coordMember struct {
	id  domain.MemberID
	key ssh.Signer
}

// coordServer is one running server: its SSH address and an idempotent
// stop that waits for a clean shutdown.
type coordServer struct {
	srv  *Server
	addr string
	stop func()
}

// newCoordEnv seeds the fixture on the in-process runtime, which the two
// scenarios below force because their agents reach container surfaces from
// the test process.
func newCoordEnv(ctx context.Context, t *testing.T, disabled bool) (*coordEnv, *coordServer) {
	t.Helper()
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake"}
	return e, e.seed(ctx, t, disabled)
}

// seed brings the server up on a fresh data directory and gives it two
// members, a workspace, and a base branch pushed over the SSH transport.
func (e *coordEnv) seed(ctx context.Context, t *testing.T, disabled bool) *coordServer {
	t.Helper()
	requireBinary(t, "git")
	if e.dataDir == "" {
		// A coordination socket path is capped near 108 bytes and already
		// carries a 26-character run ID, so a scenario whose name is long
		// enough to overflow t.TempDir() sets a short root of its own.
		e.dataDir = filepath.Join(t.TempDir(), "data")
	}
	srv := e.start(ctx, t, disabled)

	adaPath, adaKey := writeClientKey(t)
	_, boKey := writeClientKey(t)
	e.keyPath = adaPath
	e.ada = coordMember{id: e.seedMember(ctx, t, srv, "Ada", "#e6194b", adaKey), key: adaKey}
	e.bo = coordMember{id: e.seedMember(ctx, t, srv, "Bo", "#3cb44b", boKey), key: boKey}

	e.ws = &domain.Workspace{
		Name:        "coord",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err := srv.srv.Store().CreateWorkspace(ctx, e.ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	e.seedRepo(t, srv.addr)
	return srv
}

func (e *coordEnv) seedMember(ctx context.Context, t *testing.T, srv *coordServer, name, color string, key ssh.Signer) domain.MemberID {
	t.Helper()
	m := &domain.Member{
		DisplayName: name,
		PublicKey:   string(ssh.MarshalAuthorizedKey(key.PublicKey())),
		Color:       color,
		Role:        domain.RoleAdmin,
	}
	if err := srv.srv.Store().CreateMember(ctx, m); err != nil {
		t.Fatalf("seed member %s: %v", name, err)
	}
	return m.ID
}

// seedRepo pushes a base branch into the workspace repo over the SSH git
// transport, which is what run checkouts are cut from.
func (e *coordEnv) seedRepo(t *testing.T, addr string) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+e.keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, dir, env, "init", "-q", "-b", "main")
	runGit(t, dir, env, "config", "user.name", "E2E")
	runGit(t, dir, env, "config", "user.email", "e2e@localhost")
	runGit(t, dir, env, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "README.md"), "# coordination seed\n")
	runGit(t, dir, env, "add", "-A")
	runGit(t, dir, env, "commit", "-q", "-m", "seed")
	runGit(t, dir, env, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", addr, e.ws.ID), "main")
}

// start brings a server up on the shared data directory and runtime. The
// SSH port is fresh on every start, so clients redial after a restart.
func (e *coordEnv) start(ctx context.Context, t *testing.T, disabled bool) *coordServer {
	t.Helper()
	srv, err := New(ctx, Config{
		DataDir:              e.dataDir,
		Addr:                 "127.0.0.1:0",
		Runtime:              e.rt,
		StandardImage:        e.image,
		CoordinationDisabled: disabled,
		ServerBinary:         e.serverBinary,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	s := &coordServer{srv: srv, addr: waitSSHAddr(t, srv)}
	s.stop = sync.OnceFunc(func() {
		cancel()
		select {
		case rerr := <-done:
			if rerr != nil {
				t.Errorf("server.Run: %v", rerr)
			}
		case <-time.After(30 * time.Second):
			t.Error("server did not shut down")
		}
	})
	t.Cleanup(s.stop)
	return s
}

func (s *coordServer) subscribe(ctx context.Context, t *testing.T) events.Subscription {
	t.Helper()
	sub, err := s.srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func (s *coordServer) control(t *testing.T, key ssh.Signer) (*protocol.Client, *ssh.Client) {
	t.Helper()
	client := dialSSH(t, s.addr, key)
	return openControl(t, client), client
}

func (e *coordEnv) launch(t *testing.T, ctrl *protocol.Client, task, harnessName string) protocol.Run {
	t.Helper()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: harnessName,
		Mode: string(domain.LaunchTUI),
	}, &launched); err != nil {
		t.Fatalf("run.launch %q on harness %s: %v", task, harnessName, err)
	}
	if launched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("run %q status = %q, want running", task, launched.Run.Status)
	}
	return launched.Run
}

func (e *coordEnv) coordDir(run string) string {
	return filepath.Join(e.dataDir, "coord", run)
}

// e2e is the in-process runtime, which only the scenarios that force it
// may reach.
func (e *coordEnv) e2e(t *testing.T) *e2eRuntime {
	t.Helper()
	rt, ok := e.rt.(*e2eRuntime)
	if !ok {
		t.Fatalf("this scenario needs the in-process runtime, not %T", e.rt)
	}
	return rt
}

func (e *coordEnv) container(t *testing.T, run string) *e2eContainer {
	t.Helper()
	c := e.e2e(t).container(run)
	if c == nil {
		t.Fatalf("no container for run %s", run)
	}
	return c
}

// assertRegistered is the positive mount contract for a run that can
// manually invoke MCP: the staged bridge, CLI, and socket directory are
// read-only, while no harness-owned MCP config or launch flag is synthesized.
func (e *coordEnv) assertRegistered(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if slices.Contains(c.spec.Command, "--mcp-config") {
		t.Errorf("run %s received obsolete automatic MCP config: %v", run.ID, c.spec.Command)
	}
	for _, target := range []string{coordtransport.MountDir, coordtransport.BinaryPath, coordtransport.CLIPath} {
		m, ok := c.mount(target)
		if !ok || !m.ReadOnly {
			t.Fatalf("run %s has no read-only mount at %s: %+v", run.ID, target, c.spec.Mounts)
		}
	}
}

// assertNoticeOnly is a run whose fixture does not manually invoke MCP:
// ordinary coordination assets are still available, but no launch profile
// registration is injected.
func (e *coordEnv) assertNoticeOnly(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if slices.Contains(c.spec.Command, "--mcp-config") {
		t.Errorf("run %s received obsolete automatic MCP config: %v", run.ID, c.spec.Command)
	}
	for _, target := range []string{coordtransport.MountDir, coordtransport.CLIPath} {
		m, ok := c.mount(target)
		if !ok || !m.ReadOnly {
			t.Errorf("run %s has no read-only %s mount: %+v", run.ID, target, c.spec.Mounts)
		}
	}
}

// assertNoCoordination is what a run launched with the kill switch off
// carries: the version-matched CLI only, with no socket or bridge mount.
func (e *coordEnv) assertNoCoordination(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if slices.Contains(c.spec.Command, "--mcp-config") {
		t.Errorf("run %s received obsolete automatic MCP config: %v", run.ID, c.spec.Command)
	}
	m, ok := c.mount(coordtransport.CLIPath)
	if !ok || !m.ReadOnly {
		t.Errorf("run %s has no read-only CLI mount with coordination off: %+v", run.ID, c.spec.Mounts)
	}
	for _, target := range []string{coordtransport.MountDir, coordtransport.BinaryPath} {
		if _, ok := c.mount(target); ok {
			t.Errorf("run %s has a %s mount with coordination off: %+v", run.ID, target, c.spec.Mounts)
		}
	}
	if _, err := os.Stat(e.coordDir(run.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("run %s has a coordination directory with coordination off (stat error %v)", run.ID, err)
	}
}

func (e *coordEnv) assertNoMail(ctx context.Context, t *testing.T, srv *coordServer, runs ...string) {
	t.Helper()
	mail, ok := srv.srv.Store().(store.MessageStore)
	if !ok {
		t.Fatal("the store has no run mailbox")
	}
	for _, run := range runs {
		n, err := mail.CountUnackedRunMessages(ctx, domain.RunID(run))
		if err != nil {
			t.Fatalf("count messages for run %s: %v", run, err)
		}
		if n != 0 {
			t.Errorf("run %s holds %d messages with coordination off", run, n)
		}
	}
}

// assertToolsUnavailable drives the real bridge at a socket the kill
// switch unlinked: every tool must report Aether's unavailable code.
func assertToolsUnavailable(ctx context.Context, t *testing.T, sock, peer string) {
	t.Helper()
	cs, stop, err := bridgeSession(ctx, sock)
	if err != nil {
		t.Fatalf("start a bridge on the inert socket: %v", err)
	}
	defer stop()
	calls := []struct {
		tool string
		args any
	}{
		{toolStatus, nil},
		{toolSend, protocol.CoordSendParams{ToRunID: peer, Body: "anyone there?"}},
		{toolInbox, nil},
	}
	for _, call := range calls {
		res, cerr := callTool(ctx, cs, call.tool, call.args, nil)
		if cerr == nil {
			t.Errorf("%s answered with coordination off", call.tool)
			continue
		}
		if code := toolErrorCode(res); code != protocol.CodeUnavailable {
			t.Errorf("%s error code = %d, want %d (unavailable): %v", call.tool, code, protocol.CodeUnavailable, cerr)
		}
	}
}

// waitAttach attaches once the run's PTY session is live: after a restart
// the scheduler re-attaches surviving containers asynchronously, so the
// first attempts legitimately find no session yet.
func waitAttach(t *testing.T, client *ssh.Client, run string) *attachConn {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		att, err := tryAttach(t, client, run)
		if err == nil {
			return att
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never got its PTY session back: %v", run, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitOverlap polls the conflict radar over the control channel until it
// reports the pair in conflict. The radar answers whatever the kill switch
// is doing, which is exactly the point.
func waitOverlap(t *testing.T, ctrl *protocol.Client, run, peer string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var res protocol.RunOverlapsResult
		if err := ctrl.Call(protocol.MethodRunOverlaps, struct{}{}, &res); err != nil {
			t.Fatalf("run.overlaps: %v", err)
		}
		for _, o := range res.Overlaps {
			if o.RunID != run {
				continue
			}
			for _, p := range o.With {
				if p.RunID == peer {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the radar never reported run %s overlapping run %s", run, peer)
}

// coordNote matches the server-originated timeline entry a coordination
// message leaves on the sending run.
func coordNote(run, to string) func(events.Event) bool {
	return timelineNote(run, "coordination message to run "+to+": ")
}

// coordNoticeNote matches the server-originated timeline entry the overlap
// notice leaves on the run it was delivered to.
func coordNoticeNote(run, peer string) func(events.Event) bool {
	return timelineNote(run, "coordination notice: run "+peer+" is also editing ")
}

func timelineNote(run, prefix string) func(events.Event) bool {
	return func(e events.Event) bool {
		p, ok := e.Payload.(events.TimelinePayload)
		return ok && string(e.RunID) == run && e.ActorID == "" &&
			p.Kind == events.TimelineNote && strings.HasPrefix(p.Message, prefix)
	}
}

// drain collects everything already published without waiting for more.
func drain(sub events.Subscription, seen *[]events.Event) {
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				return
			}
			*seen = append(*seen, e)
		default:
			return
		}
	}
}

// assertNoCoordNote covers both entries coordination writes - the notice
// and the message - so the kill switch stays honest about either one.
func assertNoCoordNote(t *testing.T, seen []events.Event) {
	t.Helper()
	for _, e := range seen {
		p, ok := e.Payload.(events.TimelinePayload)
		if ok && strings.HasPrefix(p.Message, "coordination ") {
			t.Errorf("coordination reached the timeline with the kill switch off: %+v", p)
		}
	}
}

// assertNoNotice gives the injector a beat past the overlap the radar has
// already reported, then insists nothing was said.
func assertNoNotice(t *testing.T, atts ...*attachConn) {
	t.Helper()
	time.Sleep(2 * time.Second)
	for _, att := range atts {
		if out := att.output(); strings.Contains(out, "Overlap:") || strings.Contains(out, "notice:") {
			t.Errorf("a notice was injected with coordination off: %q", out)
		}
	}
}

func assertNoAgentError(t *testing.T, att *attachConn) {
	t.Helper()
	if out := att.output(); strings.Contains(out, "agent-error:") {
		t.Errorf("the scripted agent reported an error: %q", out)
	}
}
