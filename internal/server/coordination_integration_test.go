//go:build integration

package server

import (
	"context"
	"fmt"
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
	e.assertUnregistered(t, runC)

	attA.waitOutput(t, "assets:manual-mcp")
	attB.waitOutput(t, "assets:manual-mcp")
	attC.waitOutput(t, "assets:manual-mcp")
	// The two registered agents settle it between themselves, each message
	// travelling agent -> MCP tool -> bridge -> its own socket -> mailbox.
	attA.waitOutput(t, "inbox:"+bodyB)
	attB.waitOutput(t, "inbox:"+bodyA)

	// The exchange stays attributed to the original sending runs.
	waitEvent(t, sub, &seen, "run A's coordination message", coordMessage(runA.ID, runB.ID))
	waitEvent(t, sub, &seen, "run B's coordination message", coordMessage(runB.ID, runA.ID))

	var history protocol.CoordMessagesListResult
	if err := boCtrl.Call(protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{
		WorkspaceID: string(e.ws.ID), RunID: runA.ID,
	}, &history); err != nil {
		t.Fatalf("coord.messages.list: %v", err)
	}
	bodies := make([]string, 0, len(history.Messages))
	for _, m := range history.Messages {
		bodies = append(bodies, m.Body)
	}
	if !slices.Contains(bodies, bodyA) || !slices.Contains(bodies, bodyB) {
		t.Fatalf("history for run A = %q, want both directions", bodies)
	}

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

	// The policy switch removes peer and mission authority, not authenticated
	// run discovery or the independent development broker.
	e.assertRunAuthority(ctx, t, runA, true, runB.ID)
	e.assertRunAuthority(ctx, t, runB, true, runA.ID)
	waitOverlap(t, adaCtrl, runA.ID, runB.ID)
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID)
	drain(sub, &seen)
	assertNoCoordMessage(t, seen)
	// A swarm needs the disabled mailbox authority.
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(e.ada.id), Harness: "claude", Mode: string(domain.LaunchTUI)}
	createErr := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: "swarm with coordination off", IdempotencyKey: "kill-switch-swarm",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	}, nil)
	if createErr == nil {
		t.Error("mission.create succeeded with coordination off")
	}

	// Off -> on restores coordination authority for recovered runs as well
	// as new runs; all retain their run-scoped development identity.
	srv.stop()
	srv = e.start(ctx, t, false)
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, adaClient = srv.control(t, e.ada.key)
	waitAttach(t, adaClient, runA.ID)

	runC := e.launch(t, adaCtrl, taskC, "claude")
	attC := openAttach(t, adaClient, runC.ID)
	e.assertRunAuthority(ctx, t, runC, false, runA.ID)
	e.assertRunAuthority(ctx, t, runA, false, runB.ID)
	e.assertRunAuthority(ctx, t, runB, false, runA.ID)
	waitOverlap(t, adaCtrl, runC.ID, runA.ID)

	// On -> off recovers the same sockets, serving discovery and development
	// while refusing mailbox and mission calls.
	srv.stop()
	srv = e.start(ctx, t, true)
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, _ = srv.control(t, e.ada.key)
	for _, run := range []protocol.Run{runA, runB, runC} {
		e.assertRunAuthority(ctx, t, run, true, runA.ID)
	}
	for _, att := range []*attachConn{attA, attB, attC} {
		assertNoAgentError(t, att)
	}

	// No side effect anywhere, and the radar is still exactly as it was.
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID, runC.ID)
	drain(sub, &seen)
	assertNoCoordMessage(t, seen)
	waitOverlap(t, adaCtrl, runC.ID, runA.ID)
}

// coordEnv is the fixture the coordination scenarios share: one data
// directory and one runtime, so the server can be restarted with the kill
// switch in a different position while the containers it left behind stay
// alive.
type coordEnv struct {
	rt           runtime.Runtime
	image        string
	browserImage string
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
		// carries a run ID, so a scenario whose name is long
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
		BrowserImage:         e.browserImage,
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

// assertUnregistered is a run whose fixture does not manually invoke MCP:
// ordinary coordination assets are still available, but no launch profile
// registration is injected.
func (e *coordEnv) assertUnregistered(t *testing.T, run protocol.Run) {
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

// assertDevelopmentDiscovery checks the authenticated identity and independent
// development authority, including when conflict coordination is disabled.
func assertDevelopmentDiscovery(t *testing.T, status protocol.CoordStatusResult, run protocol.Run, disabled bool, development ...string) {
	t.Helper()
	if status.RunID != run.ID || status.WorkspaceID != run.WorkspaceID || status.MemberID != run.MemberID {
		t.Fatalf("discovery identity = %+v, want run %+v", status, run)
	}
	for _, method := range append([]string{protocol.MethodCoordStatus}, development...) {
		if !slices.Contains(status.Capabilities, method) {
			t.Errorf("run %s did not advertise %s: %v", run.ID, method, status.Capabilities)
		}
	}
	if disabled {
		if len(status.Peers) != 0 || status.Unread != 0 || status.Assignment != nil {
			t.Errorf("disabled coordination exposed peer or mission state: %+v", status)
		}
		for _, method := range status.Capabilities {
			if method != protocol.MethodCoordStatus && !strings.HasPrefix(method, "dev.") {
				t.Errorf("disabled coordination advertised %s", method)
			}
		}
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

func (e *coordEnv) assertRunAuthority(ctx context.Context, t *testing.T, run protocol.Run, disabled bool, peer string) {
	t.Helper()
	sock := filepath.Join(e.coordDir(run.ID), coordtransport.SocketName)
	var status protocol.CoordStatusResult
	if err := coordtransport.Call(ctx, sock, protocol.MethodCoordStatus, nil, &status); err != nil {
		t.Fatalf("run %s discovery: %v", run.ID, err)
	}
	assertDevelopmentDiscovery(t, status, run, disabled)
	if slices.Contains(status.Capabilities, protocol.MethodDevTerminalList) {
		var terminals protocol.DevTerminalListResult
		if err := coordtransport.Call(ctx, sock, protocol.MethodDevTerminalList, nil, &terminals); err != nil {
			t.Fatalf("run %s development terminal list: %v", run.ID, err)
		}
	}
	if !disabled {
		if !slices.Contains(status.Capabilities, protocol.MethodCoordSend) {
			t.Errorf("run %s did not regain peer messaging: %v", run.ID, status.Capabilities)
		}
		if err := coordtransport.Call(ctx, sock, protocol.MethodCoordInbox, nil, nil); err != nil {
			t.Errorf("run %s did not regain mailbox access: %v", run.ID, err)
		}
		return
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodCoordSend, protocol.CoordSendParams{ToRunID: peer, Body: "anyone there?"}},
		{protocol.MethodCoordInbox, nil},
		{protocol.MethodTaskList, nil},
		{protocol.MethodRunReport, nil},
	} {
		err := coordtransport.Call(ctx, sock, call.method, call.params, nil)
		if code := coordtransport.ErrorCode(err); code != protocol.CodeUnavailable {
			t.Errorf("%s with coordination off = %v, want unavailable (%d)", call.method, err, protocol.CodeUnavailable)
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

// coordMessage matches the server-originated event a coordination message
// leaves on the sending run.
func coordMessage(run, to string) func(events.Event) bool {
	return func(e events.Event) bool {
		p, ok := e.Payload.(events.CoordMessagePayload)
		return ok && string(e.RunID) == run && e.ActorID == "" &&
			string(p.FromRunID) == run && string(p.ToRunID) == to
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

// assertNoCoordMessage verifies the kill switch suppresses message events.
func assertNoCoordMessage(t *testing.T, seen []events.Event) {
	t.Helper()
	for _, e := range seen {
		if p, ok := e.Payload.(events.CoordMessagePayload); ok {
			t.Errorf("coordination published a message with the kill switch off: %+v", p)
		}
	}
}

func assertNoAgentError(t *testing.T, att *attachConn) {
	t.Helper()
	if out := att.output(); strings.Contains(out, "agent-error:") {
		t.Errorf("the scripted agent reported an error: %q", out)
	}
}
