//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// multiAgentScript is the multi-member fake agent, committed to the seed
// repo and dispatched on the task the run was launched with: "collab"
// echoes two injected lines before committing a result, "crash" leaves
// partial work behind and dies. The e2eRuntime fallback registers the
// same behaviours per task key.
const multiAgentScript = `sleep 1
echo agent-ready
case "$1" in
collab)
  read first
  echo "got:$first"
  read second
  echo "got:$second"
  printf 'collab done\n' > result.txt
  ;;
crash)
  printf 'half-finished\n' > partial.txt
  exit 3
  ;;
esac
`

// stubWhoIs is the tailnet identity seam for the E2E suite: whatever it is
// set to is who the server believes the next connections come from. CI
// needs no real tailnet.
type stubWhoIs struct {
	mu  sync.Mutex
	id  sshd.WhoIsIdentity
	err error
}

func (s *stubWhoIs) set(id sshd.WhoIsIdentity, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.err = id, err
}

func (s *stubWhoIs) WhoIs(context.Context, string) (sshd.WhoIsIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, s.err
}

// TestIntegrationMultiMember is the multi-member scenario: three simulated
// clients on one wired server. Ada bootstraps as admin over a stubbed
// tailnet identity, Bo joins pending and is approved, Cam joins with an
// invite code and a key while WhoIs is down - which also proves the auth
// fallback (key members connect, tailnet-only members are refused with the
// banner). The session is administered over the control channel, then the
// members collaborate through the fake agent: Cam steers Bo's run, Bo
// hands it off to Cam, the approval inbox round-trips a pause, the budget
// cap refuses a launch until Ada overrides it, and a crashing agent lands
// failed with its partial work committed as wip.
func TestIntegrationMultiMember(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	if e2e, ok := rt.(*e2eRuntime); ok {
		registerMultiMemberScripts(e2e)
	}
	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh {task}")

	whois := &stubWhoIs{}
	whois.set(sshd.WhoIsIdentity{Login: "ada@example.com", NodeID: "node-ada"}, nil)
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt, WhoIs: whois})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	// Ada: the first tailnet identity to contact a fresh server bootstraps
	// as the admin, with no key exchange at all.
	adaClient, err := dialNoAuth(addr, "ada", nil)
	if err != nil {
		t.Fatalf("ada tailnet dial: %v", err)
	}
	t.Cleanup(func() { _ = adaClient.Close() })
	adaCtrl := openControl(t, adaClient)
	ada := memberInfo(t, adaCtrl)
	if ada.Role != string(domain.RoleAdmin) || ada.Pending {
		t.Fatalf("bootstrap member = %+v, want approved admin", ada)
	}

	// Bo: the second tailnet identity lands pending and is denied
	// everything but server.info until an admin approves.
	whois.set(sshd.WhoIsIdentity{Login: "bo@example.com", NodeID: "node-bo"}, nil)
	boClient, err := dialNoAuth(addr, "bo", nil)
	if err != nil {
		t.Fatalf("bo tailnet dial: %v", err)
	}
	t.Cleanup(func() { _ = boClient.Close() })
	boCtrl := openControl(t, boClient)
	bo := memberInfo(t, boCtrl)
	if !bo.Pending {
		t.Fatal("second tailnet identity did not land pending")
	}
	var pe *protocol.Error
	if err := boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: "ses_nope", Task: "collab", Harness: "fake",
	}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending run.launch = %v, want CodeDenied", err)
	}
	if err := adaCtrl.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: bo.ID}, nil); err != nil {
		t.Fatalf("member.approve: %v", err)
	}

	// Cam: the invite-code key join, driven while WhoIs is unavailable so
	// the same dials prove the degradation contract - key members connect,
	// tailnet-only members are refused with the banner.
	var invite protocol.MemberInviteResult
	if err := adaCtrl.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{}, &invite); err != nil {
		t.Fatalf("member.invite: %v", err)
	}
	whois.set(sshd.WhoIsIdentity{}, errors.New("tailscaled unreachable"))
	camPath, camSigner := writeClientKey(t)
	joinClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "invite:" + invite.Code + ":Cam",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(camSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("invite join dial: %v", err)
	}
	// The join lands during connection setup; a control call on the same
	// connection proves it before the code-less redial below.
	if got := memberInfo(t, openControl(t, joinClient)); got.DisplayName != "Cam" {
		t.Fatalf("invite connection member = %+v, want Cam", got)
	}
	_ = joinClient.Close()
	camClient := dialSSH(t, addr, camSigner)
	camCtrl := openControl(t, camClient)
	cam := memberInfo(t, camCtrl)
	if cam.Role != string(domain.RoleCollaborator) || cam.DisplayName != "Cam" {
		t.Fatalf("invited member = %+v, want collaborator Cam", cam)
	}
	var banner strings.Builder
	if c, derr := dialNoAuth(addr, "bo", &banner); derr == nil {
		_ = c.Close()
		t.Fatal("tailnet-only member connected while WhoIs is down")
	}
	if !strings.Contains(banner.String(), "tailnet identity unavailable") {
		t.Fatalf("fallback banner = %q, want the whois-failure explanation", banner.String())
	}

	// The working session is administered remotely, over the control
	// channel: Ada registers the workspace and opens the session.
	var addedWS protocol.WorkspaceAddResult
	if err := adaCtrl.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "team", Environment: protocol.WorkspaceEnvironment{CustomImage: image}}, &addedWS); err != nil {
		t.Fatalf("workspace.add: %v", err)
	}
	var newSess protocol.SessionNewResult
	if err := adaCtrl.Call(protocol.MethodSessionNew, protocol.SessionNewParams{
		WorkspaceID: addedWS.Workspace.ID, Name: "team effort",
	}, &newSess); err != nil {
		t.Fatalf("session.new: %v", err)
	}
	sessID := newSess.Session.ID

	// Cam seeds the base branch over the SSH git transport with their key.
	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, addedWS.Workspace.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+camPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "Cam")
	runGit(t, seedDir, gitEnv, "config", "user.email", "cam@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), multiAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	// Bo launches; the run shows on Cam's board; Cam watches the terminal
	// and steers Bo's run - collaborators steer each other by default.
	var launched protocol.RunResult
	if err := boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: sessID, Task: "collab", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("bo run.launch: %v", err)
	}
	collab := launched.Run
	var board protocol.RunListResult
	if err := camCtrl.Call(protocol.MethodRunList, protocol.RunListParams{SessionID: sessID}, &board); err != nil {
		t.Fatalf("cam run.list: %v", err)
	}
	if len(board.Runs) != 1 || board.Runs[0].ID != collab.ID {
		t.Fatalf("cam's board = %+v, want bo's run", board.Runs)
	}
	camAtt := openAttach(t, camClient, collab.ID)
	camAtt.waitOutput(t, "agent-ready")
	if err := camCtrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: collab.ID, Message: "steer-from-cam",
	}, nil); err != nil {
		t.Fatalf("cam run.inject into bo's run: %v", err)
	}
	camAtt.waitOutput(t, "got:steer-from-cam")
	if !strings.Contains(camAtt.output(), "Cam injects") {
		t.Errorf("inject banner not attributed to Cam: %q", camAtt.output())
	}
	waitEvent(t, sub, &seen, "cam's steer entry", func(e events.Event) bool {
		p, ok := e.Payload.(events.TimelinePayload)
		return ok && string(e.RunID) == collab.ID && e.ActorID == domain.MemberID(cam.ID) &&
			p.Kind == events.TimelineSteer && p.Message == "steer-from-cam"
	})

	// Presence: Bo heartbeats, Cam holds the attach; the roster reports
	// Bo online and Cam watching the run.
	if err := boCtrl.Call(protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{SessionID: sessID}, nil); err != nil {
		t.Fatalf("presence.heartbeat: %v", err)
	}
	waitRoster(t, adaCtrl, sessID, func(entries []protocol.PresenceEntry) bool {
		var boOnline, camWatching bool
		for _, e := range entries {
			boOnline = boOnline || e.MemberID == bo.ID
			camWatching = camWatching || (e.MemberID == cam.ID && e.State == "watching")
		}
		return boOnline && camWatching
	})

	// Handoff: Bo transfers the run to Cam; ownership and the attributed
	// timeline entry both land.
	if err := boCtrl.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: collab.ID, ToMemberID: cam.ID,
	}, nil); err != nil {
		t.Fatalf("run.handoff: %v", err)
	}
	var after protocol.RunResult
	if err := adaCtrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: collab.ID}, &after); err != nil {
		t.Fatalf("run.get after handoff: %v", err)
	}
	if after.Run.MemberID != cam.ID {
		t.Fatalf("run owner after handoff = %s, want %s", after.Run.MemberID, cam.ID)
	}
	waitEvent(t, sub, &seen, "handoff entry", func(e events.Event) bool {
		p, ok := e.Payload.(events.TimelinePayload)
		return ok && string(e.RunID) == collab.ID && e.ActorID == domain.MemberID(bo.ID) &&
			p.Kind == events.TimelineHandoff && p.Message == cam.ID
	})

	// Approval inbox: an adapter-surfaced pause (the inbox's one source,
	// published on the same bus seam adapters use) reaches every client's
	// inbox, and a steer-holder's decision is attributed.
	if _, err := srv.Bus().Publish(ctx, events.Event{
		SessionID: domain.SessionID(sessID),
		RunID:     domain.RunID(collab.ID),
		Payload: events.AgentEventPayload{
			Kind: events.AgentPause, Tool: "Bash", ToolUseID: "tu-e2e-1", Detail: "rm -rf build/",
		},
	}); err != nil {
		t.Fatalf("publish agent pause: %v", err)
	}
	requestID := waitApproval(t, boCtrl, sessID)
	var decided protocol.ApprovalDecideResult
	if err := boCtrl.Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
		RunID: collab.ID, RequestID: requestID, Approve: true,
	}, &decided); err != nil {
		t.Fatalf("approval.decide: %v", err)
	}
	if decided.Approval.Decision != "approved" || decided.Approval.DecidedBy != bo.ID {
		t.Fatalf("decision = %+v, want approved by Bo", decided.Approval)
	}

	// Budget cap: Ada caps the session, the adapter meters the running run
	// past it, and the next launch is refused before the scheduler is
	// asked - while the running run is untouched. Ada's override admits
	// the next launch.
	if err := adaCtrl.Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: sessID, LimitUSD: 1,
	}, nil); err != nil {
		t.Fatalf("budget.set: %v", err)
	}
	if _, err := srv.Bus().Publish(ctx, events.Event{
		SessionID: domain.SessionID(sessID),
		RunID:     domain.RunID(collab.ID),
		Payload:   events.RunCostPayload{InputTokens: 40000, OutputTokens: 2000, CostUSD: 1.25, Metered: true},
	}); err != nil {
		t.Fatalf("publish run cost: %v", err)
	}
	waitBudgetState(t, adaCtrl, sessID, string(events.BudgetExceeded))
	launchErr := boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: sessID, Task: "blocked", Harness: "fake",
	}, nil)
	if launchErr == nil || !strings.Contains(launchErr.Error(), "session budget exceeded") {
		t.Fatalf("launch past the cap = %v, want the budget refusal", launchErr)
	}
	if err := adaCtrl.Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: sessID, LimitUSD: 1, Override: true,
	}, nil); err != nil {
		t.Fatalf("budget.set override: %v", err)
	}

	// The admitted run crashes: the run lands failed with the exit code in
	// the reason and its partial work committed as wip (the failure
	// table's agent-crash row).
	var crashed protocol.RunResult
	if err := boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: sessID, Task: "crash", Harness: "fake",
	}, &crashed); err != nil {
		t.Fatalf("run.launch under override: %v", err)
	}
	ev := waitEvent(t, sub, &seen, "crash run failed", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && string(e.RunID) == crashed.Run.ID && p.To == domain.RunFailed
	})
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited 3" {
		t.Fatalf("failed reason = %q", p.Reason)
	}
	var pull protocol.RunPullResult
	if err := boCtrl.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: crashed.Run.ID}, &pull); err != nil {
		t.Fatalf("run.pull crashed run: %v", err)
	}
	runGit(t, seedDir, gitEnv, "fetch", "-q", repoURL, pull.Branch)
	if got := runGit(t, seedDir, gitEnv, "show", "FETCH_HEAD:partial.txt"); got != "half-finished\n" {
		t.Fatalf("partial.txt at wip tip = %q", got)
	}
	if got := strings.TrimSpace(runGit(t, seedDir, gitEnv, "log", "-1", "--format=%s", "FETCH_HEAD")); got != "wip: crash" {
		t.Fatalf("crashed branch tip subject = %q", got)
	}

	// Bo steers the run they handed away - still allowed as a
	// collaborator - and the collab run finishes.
	if err := boCtrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: collab.ID, Message: "done",
	}, nil); err != nil {
		t.Fatalf("bo run.inject after handoff: %v", err)
	}
	camAtt.waitOutput(t, "got:done")
	waitEvent(t, sub, &seen, "collab run parked", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && string(e.RunID) == collab.ID && p.To == domain.RunNeedsAttention
	})

	stopServer()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server did not shut down")
	}
	verifyNoLeaks(t)
}

// registerMultiMemberScripts mirrors multiAgentScript on the in-process
// runtime, keyed by task.
func registerMultiMemberScripts(rt *e2eRuntime) {
	rt.script("collab", func(c *e2eContainer) {
		for range 2 {
			line, ok := c.readStdinLine()
			if !ok {
				return
			}
			c.output("got:" + line + "\r\n")
		}
		_ = os.WriteFile(filepath.Join(c.spec.WorktreeHostPath, "result.txt"), []byte("collab done\n"), 0o644)
	})
	rt.script("crash", func(c *e2eContainer) {
		_ = os.WriteFile(filepath.Join(c.spec.WorktreeHostPath, "partial.txt"), []byte("half-finished\n"), 0o644)
		c.exitNow(3)
	})
}

// dialNoAuth dials with no auth methods at all - the pure tailnet client
// shape - capturing any auth banner when banner is non-nil.
func dialNoAuth(addr, user string, banner *strings.Builder) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	if banner != nil {
		cfg.BannerCallback = func(message string) error {
			banner.WriteString(message)
			return nil
		}
	}
	return ssh.Dial("tcp", addr, cfg)
}

// memberInfo is the caller's own member row, per server.info.
func memberInfo(t *testing.T, ctrl *protocol.Client) protocol.Member {
	t.Helper()
	var info protocol.ServerInfoResult
	if err := ctrl.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	return info.Member
}

// waitRoster polls presence.roster until ok accepts the entries; the
// roster is fed asynchronously from the bus.
func waitRoster(t *testing.T, ctrl *protocol.Client, session string, ok func([]protocol.PresenceEntry) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	var last protocol.PresenceRosterResult
	for time.Now().Before(deadline) {
		last = protocol.PresenceRosterResult{}
		if err := ctrl.Call(protocol.MethodPresenceRoster, protocol.PresenceRosterParams{SessionID: session}, &last); err != nil {
			t.Fatalf("presence.roster: %v", err)
		}
		if ok(last.Members) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("roster never settled; last = %+v", last.Members)
}

// waitApproval polls the approval inbox until the raised request appears,
// returning its ID.
func waitApproval(t *testing.T, ctrl *protocol.Client, session string) string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		var inbox protocol.ApprovalListResult
		if err := ctrl.Call(protocol.MethodApprovalList, protocol.ApprovalListParams{SessionID: session}, &inbox); err != nil {
			t.Fatalf("approval.list: %v", err)
		}
		if len(inbox.Approvals) == 1 && inbox.Approvals[0].Decision == "requested" {
			return inbox.Approvals[0].ID
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the pause never reached the approval inbox")
	return ""
}

// waitBudgetState polls budget.get until the session reaches state; spend
// is folded in asynchronously from the bus.
func waitBudgetState(t *testing.T, ctrl *protocol.Client, session, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	var last protocol.BudgetResult
	for time.Now().Before(deadline) {
		last = protocol.BudgetResult{}
		if err := ctrl.Call(protocol.MethodBudgetGet, protocol.BudgetGetParams{SessionID: session}, &last); err != nil {
			t.Fatalf("budget.get: %v", err)
		}
		if last.State == state {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("budget never reached %q; last = %+v", state, last)
}
