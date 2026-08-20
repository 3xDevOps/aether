//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// The conflict-coordination E2E. It drives the whole wired server over
// real SSH - control channel, attach, event bus, radar, coordination
// sockets - against the in-process runtime rather than Docker, because the
// agent has to reach the surfaces its container was given and the staged
// bridge under `go test` is the test binary, which has no mcp subcommand.
// Everything else on the path is the real thing.

// mcpConfigTarget is where a registered harness is told to read its MCP
// config, inside the container.
var mcpConfigTarget = path.Join(mcpbridge.MountDir, coord.ConfigName)

// TestIntegrationCoordinationEndToEnd is the release gate: two overlapping
// runs on a registered harness are told about each other, settle it
// through the real MCP tools over their own coordination sockets, and
// leave attributed timeline entries - while a run on a harness with no MCP
// registration gets the notice and nothing else.
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
	e.rt.script(taskA, func(c *e2eContainer) { coordAgent{peer: taskB, body: bodyA, release: release}.run(ctx, c) })
	e.rt.script(taskB, func(c *e2eContainer) { coordAgent{peer: taskA, body: bodyB, release: release}.run(ctx, c) })
	e.rt.script(taskC, func(c *e2eContainer) { coordAgent{release: release}.run(ctx, c) })

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

	// Every overlapping agent is told, in its own terminal.
	for _, att := range []*attachConn{attA, attB, attC} {
		att.waitOutput(t, "aether injects")
		att.waitOutput(t, "notice:[aether] Overlap: run ")
	}
	attC.waitOutput(t, "assets:notice-only")

	// The two registered agents settle it between themselves, each message
	// travelling agent -> MCP tool -> bridge -> its own socket -> mailbox.
	attA.waitOutput(t, "inbox:"+bodyB)
	attB.waitOutput(t, "inbox:"+bodyA)

	// The whole exchange is on the session timeline - the notice each run
	// was given and the message each one sent - attributed to the owner of
	// the run it happened to.
	waitEvent(t, sub, &seen, "run A's notice entry", coordNoticeNote(runA.ID, e.ada.id, runB.ID))
	waitEvent(t, sub, &seen, "run B's notice entry", coordNoticeNote(runB.ID, e.bo.id, runA.ID))
	waitEvent(t, sub, &seen, "run A's coordination note", coordNote(runA.ID, e.ada.id, runB.ID))
	waitEvent(t, sub, &seen, "run B's coordination note", coordNote(runB.ID, e.bo.id, runA.ID))

	// The unregistered harness never joined the conversation.
	if out := attC.output(); strings.Contains(out, "inbox:") || strings.Contains(out, "sent:") {
		t.Errorf("the unregistered harness exchanged messages: %q", out)
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
		e.rt.script(task, func(c *e2eContainer) { coordAgent{release: release}.run(ctx, c) })
	}

	sub := srv.subscribe(ctx, t)
	var seen []events.Event
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	boCtrl, boClient := srv.control(t, e.bo.key)
	runA := e.launch(t, adaCtrl, taskA, "claude")
	runB := e.launch(t, boCtrl, taskB, "claude")
	attA := openAttach(t, adaClient, runA.ID)
	attB := openAttach(t, boClient, runB.ID)

	// Cold start with the switch off: a registered harness is launched
	// exactly as it was before the feature existed. Nothing is staged,
	// nothing is provisioned, no argument is added.
	attA.waitOutput(t, "assets:none")
	attB.waitOutput(t, "assets:none")
	e.assertNoCoordination(t, runA)
	e.assertNoCoordination(t, runB)
	for _, dir := range []string{filepath.Join(e.dataDir, "coord"), filepath.Join(e.dataDir, "runtime", "bin")} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists with coordination off (stat error %v)", dir, err)
		}
	}
	// The radar is the one thing that still reacts, and it is the only one.
	waitOverlap(t, adaCtrl, runA.ID, runB.ID)
	assertNoNotice(t, attA, attB)
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID)
	drain(sub, &seen)
	assertNoCoordNote(t, seen)

	// Off -> on. Only a new run gains the bridge; the two containers that
	// predate the switch keep exactly what they were given.
	srv.stop()
	srv = e.start(ctx, t, false)
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, adaClient = srv.control(t, e.ada.key)

	// The surviving containers are re-attached asynchronously, and a notice
	// is only ever injected into a live terminal - so wait for run A's
	// before giving the radar something new to say.
	attA2 := waitAttach(t, adaClient, runA.ID)

	runC := e.launch(t, adaCtrl, taskC, "claude")
	attC := openAttach(t, adaClient, runC.ID)
	attC.waitOutput(t, "assets:mcp")
	e.assertRegistered(t, runC)
	e.assertNoCoordination(t, runA)
	e.assertNoCoordination(t, runB)

	// The unprovisioned run is notice-only: the banner reaches it, the
	// tools never can.
	attA2.waitOutput(t, "notice:[aether] Overlap: run ")
	attC.waitOutput(t, "notice:[aether] Overlap: run ")

	// On -> off. What run C's container already holds stays physically
	// where it is - the mount, the config, the argument it launched with.
	srv.stop()
	dir := e.coordDir(runC.ID)
	config, err := os.ReadFile(filepath.Join(dir, coord.ConfigName))
	if err != nil {
		t.Fatalf("read run C's MCP config: %v", err)
	}
	srv = e.start(ctx, t, true)
	// A fresh subscription, so what is collected below is only what the
	// server published while the switch was off.
	sub, seen = srv.subscribe(ctx, t), nil
	adaCtrl, _ = srv.control(t, e.ada.key)
	e.assertRegistered(t, runC)
	if after, rerr := os.ReadFile(filepath.Join(dir, coord.ConfigName)); rerr != nil || !bytes.Equal(after, config) {
		t.Errorf("run C's MCP config changed when coordination was turned off: %s (err %v)", after, rerr)
	}
	// What makes any of it work is gone: the socket is unlinked and every
	// method behind it answers unavailable before touching anything.
	if _, serr := os.Stat(filepath.Join(dir, coord.SocketName)); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("the coordination socket survived the switch being turned off (stat error %v)", serr)
	}
	assertToolsUnavailable(ctx, t, filepath.Join(dir, coord.SocketName), runA.ID)

	// No side effect anywhere, and the radar is still exactly as it was.
	e.assertNoMail(ctx, t, srv, runA.ID, runB.ID, runC.ID)
	drain(sub, &seen)
	assertNoCoordNote(t, seen)
	waitOverlap(t, adaCtrl, runC.ID, runA.ID)
}

// coordEnv is the fixture both tests share: one data directory and one
// in-process runtime, so the server can be restarted with the kill switch
// in a different position while the containers it left behind stay alive.
type coordEnv struct {
	rt      *e2eRuntime
	dataDir string
	keyPath string

	ws   *domain.Workspace
	sess *domain.Session
	ada  coordMember
	bo   coordMember
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

func newCoordEnv(ctx context.Context, t *testing.T, disabled bool) (*coordEnv, *coordServer) {
	t.Helper()
	requireBinary(t, "git")
	e := &coordEnv{rt: newE2ERuntime(), dataDir: filepath.Join(t.TempDir(), "data")}
	srv := e.start(ctx, t, disabled)

	adaPath, adaKey := writeClientKey(t)
	_, boKey := writeClientKey(t)
	e.keyPath = adaPath
	e.ada = coordMember{id: e.seedMember(ctx, t, srv, "Ada", "#e6194b", adaKey), key: adaKey}
	e.bo = coordMember{id: e.seedMember(ctx, t, srv, "Bo", "#3cb44b", boKey), key: boKey}

	e.ws = &domain.Workspace{Name: "coord", Environment: domain.WorkspaceEnvironment{CustomImage: "e2e/fake"}}
	if err := srv.srv.Store().CreateWorkspace(ctx, e.ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	e.sess = &domain.Session{WorkspaceID: e.ws.ID, Name: "coordination", BaseBranch: "main"}
	if err := srv.srv.Store().CreateSession(ctx, e.sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	e.seedRepo(t, srv.addr)
	return e, srv
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
		CoordinationDisabled: disabled,
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
		SessionID: string(e.sess.ID), Task: task, Harness: harnessName,
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

func (e *coordEnv) container(t *testing.T, run string) *e2eContainer {
	t.Helper()
	c := e.rt.container(run)
	if c == nil {
		t.Fatalf("no container for run %s", run)
	}
	return c
}

// assertRegistered is the whole registration contract for one run: the
// read-only bridge and coordination mounts, the config the server wrote
// into the coordination directory naming the staged bridge, the argument
// pointing the harness at it, and no trace of any of it in the worktree.
func (e *coordEnv) assertRegistered(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if argv := c.spec.Command; len(argv) < 2 ||
		argv[len(argv)-2] != "--mcp-config" || argv[len(argv)-1] != mcpConfigTarget {
		t.Fatalf("run %s argv = %v, want it to end with --mcp-config %s", run.ID, c.spec.Command, mcpConfigTarget)
	}
	for _, target := range []string{mcpbridge.MountDir, mcpbridge.BinaryPath} {
		m, ok := c.mount(target)
		if !ok || !m.ReadOnly {
			t.Fatalf("run %s has no read-only mount at %s: %+v", run.ID, target, c.spec.Mounts)
		}
	}
	raw, err := os.ReadFile(filepath.Join(e.coordDir(run.ID), coord.ConfigName))
	if err != nil {
		t.Fatalf("read the MCP config written for run %s: %v", run.ID, err)
	}
	var doc struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the MCP config written for run %s: %v", run.ID, err)
	}
	bridge, ok := doc.Servers[mcpbridge.ServerName]
	if !ok || bridge.Type != "stdio" || bridge.Command != mcpbridge.BinaryPath ||
		len(bridge.Args) != 1 || bridge.Args[0] != "mcp" {
		t.Fatalf("run %s MCP config = %s, want a stdio %s server running the staged bridge",
			run.ID, raw, mcpbridge.ServerName)
	}
	if _, err := os.Stat(filepath.Join(c.spec.WorktreeHostPath, ".mcp.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("run %s has an .mcp.json in its worktree (stat error %v)", run.ID, err)
	}
}

// assertNoticeOnly is the degradation a harness without MCP registration
// gets: provisioned like any other run, but never pointed at the bridge.
func (e *coordEnv) assertNoticeOnly(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if slices.Contains(c.spec.Command, "--mcp-config") {
		t.Errorf("unregistered harness argv = %v, want no MCP registration", c.spec.Command)
	}
	if _, ok := c.mount(mcpbridge.MountDir); !ok {
		t.Errorf("run %s has no coordination mount: %+v", run.ID, c.spec.Mounts)
	}
	if _, err := os.Stat(filepath.Join(e.coordDir(run.ID), coord.ConfigName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("run %s got an MCP config it cannot read (stat error %v)", run.ID, err)
	}
}

// assertNoCoordination is what a run launched with the kill switch off
// carries: no mounts, no directory, no argument.
func (e *coordEnv) assertNoCoordination(t *testing.T, run protocol.Run) {
	t.Helper()
	c := e.container(t, run.ID)
	if slices.Contains(c.spec.Command, "--mcp-config") {
		t.Errorf("run %s argv = %v, want no MCP registration", run.ID, c.spec.Command)
	}
	for _, target := range []string{mcpbridge.MountDir, mcpbridge.BinaryPath} {
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

// coordNote matches the timeline entry a coordination message leaves on
// the sending run, attributed to that run's owner.
func coordNote(run string, actor domain.MemberID, to string) func(events.Event) bool {
	return timelineNote(run, actor, "coordination message to run "+to+": ")
}

// coordNoticeNote matches the timeline entry the overlap notice leaves on
// the run it was delivered to, attributed to that run's owner.
func coordNoticeNote(run string, actor domain.MemberID, peer string) func(events.Event) bool {
	return timelineNote(run, actor, "coordination notice: run "+peer+" is also editing ")
}

func timelineNote(run string, actor domain.MemberID, prefix string) func(events.Event) bool {
	return func(e events.Event) bool {
		p, ok := e.Payload.(events.TimelinePayload)
		return ok && string(e.RunID) == run && e.ActorID == actor &&
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
