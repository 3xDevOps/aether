//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/servergw"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// enhancedServer's "fake" agent serves ACP through the acpmock agent seeded
// into the workspace's repository; its terminal mode sleeps.
type enhancedServer struct {
	srv  *Server
	web  string
	ws   *domain.Workspace
	data string
	stop func(*testing.T)
}

func startEnhancedServer(ctx context.Context, t *testing.T) *enhancedServer {
	t.Helper()
	requireBinary(t, "git")
	rt, verifyNoLeaks, ok := dockerRuntime(t)
	if !ok {
		t.Skip("an enhanced run needs a managed exec in a real container; Docker daemon unreachable")
	}
	agent := filepath.Join(t.TempDir(), "acp-mock")
	build := exec.Command("go", "build", "-o", agent, "./internal/acphost/acpmock/agent")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the mock agent: %v\n%s", err, out)
	}

	whois := &stubWhoIs{}
	whois.set(sshd.WhoIsIdentity{Login: "ada@example.com", NodeID: "node-ada"}, nil)
	data := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{
		DataDir: data, Addr: "127.0.0.1:0", Runtime: rt,
		StandardImage: "busybox", RunContainerTTL: -time.Second, WhoIs: whois,
		ServerBinary: buildServerBinary(t),
		Harnesses: map[string]scheduler.HarnessSpec{
			"fake": {
				TUIArgs: []string{"sh", "-c", "exec sleep 3600"},
				ACPArgs: []string{"sh", "-c", "exec /workspace/acp-mock"},
			},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stopServer := context.WithCancel(ctx)
	go func() { _ = srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)
	gw, err := servergw.New(servergw.Config{SSH: srv.ssh})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gw)
	env := &enhancedServer{srv: srv, web: server.URL, data: data, stop: func(t *testing.T) {
		server.Close()
		_ = gw.Close()
		stopServer()
		_ = srv.Close()
		verifyNoLeaks(t)
	}}

	var caps protocol.GatewayCapabilities
	if status := getJSON(t, env.web+"/api/v1/capabilities", &caps); status != http.StatusOK || !strings.Contains(strings.Join(caps.WS, ","), "acp") {
		t.Fatalf("capabilities %d %+v, want ws acp", status, caps)
	}
	env.ws = &domain.Workspace{Name: "enhanced", BaseBranch: domain.DefaultBaseBranch}
	if err := srv.Store().CreateWorkspace(ctx, env.ws); err != nil {
		t.Fatal(err)
	}
	seedDir := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	bin, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "acp-mock"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", addr, env.ws.ID), "main")
	return env
}

// TestIntegrationEnhancedRunGateway drives an enhanced run through the
// dashboard gateway on real Docker, with the acpmock agent as its ACP server,
// the way the session view does.
func TestIntegrationEnhancedRunGateway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := startEnhancedServer(ctx, t)
	defer env.stop(t)
	web := env.web

	var launched protocol.RunResult
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(env.ws.ID), Task: "say pong", Harness: "fake", Mode: string(domain.LaunchACP)})
	if status := postJSON(t, web+"/api/v1/run.launch", string(params), &launched); status != http.StatusOK {
		t.Fatalf("run.launch status %d", status)
	}
	runID := launched.Run.ID

	stream := dialWS(t, ctx, web, "/ws/acp/"+runID)
	defer func() { _ = stream.CloseNow() }()
	if err := wsjson.Write(ctx, stream, protocol.ACPStreamRequest{Write: true, ControlSessionID: "session-view"}); err != nil {
		t.Fatal(err)
	}
	var ack protocol.ACPStreamResponse
	if err := wsjson.Read(ctx, stream, &ack); err != nil || !ack.OK || !ack.HasControl {
		t.Fatalf("acp ack %+v (%v)", ack, err)
	}
	lease := protocol.ACPLease{ControlSessionID: "session-view", ControlGeneration: ack.ControlGeneration}
	var said strings.Builder
	var pending *acphost.Request
	next := func(desc string, done func() bool) {
		t.Helper()
		for !done() {
			var frame protocol.ACPFrame
			if err := wsjson.Read(ctx, stream, &frame); err != nil {
				t.Fatalf("stream ended waiting for %s: %v (said %q)", desc, err, said.String())
			}
			var it acphost.Item
			if json.Unmarshal(frame.Item, &it) != nil {
				continue
			}
			switch {
			case it.Kind == acphost.KindMessage && it.Message.Role == "assistant":
				said.WriteString(it.Message.Text)
			case it.Kind == acphost.KindRequest && it.Request.Status == acphost.RequestPending:
				pending = it.Request
			}
		}
	}
	next("the task's answer", func() bool { return strings.Contains(said.String(), "pong") })
	waitTurns(ctx, t, env.srv, domain.RunID(runID), 1)
	original, err := env.srv.Store().GetRun(ctx, domain.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	socket := waitMissionSocket(t, filepath.Join(env.data, "coord", runID))
	reportOutcome := func(outcome, key, reason string) {
		t.Helper()
		if err := pacedCall(ctx, socket, protocol.MethodCoordReport, protocol.CoordReportParams{
			Outcome: outcome, Summary: "Verified the current turn.", IdempotencyKey: key,
		}, nil); err != nil {
			t.Fatalf("coord.report %s: %v", outcome, err)
		}
		params, _ := json.Marshal(protocol.RunIDParams{RunID: runID})
		for {
			var got protocol.RunResult
			if status := postJSON(t, web+"/api/v1/run.get", string(params), &got); status != http.StatusOK {
				t.Fatalf("run.get after report: %d", status)
			}
			if got.Run.Status == string(domain.RunNeedsAttention) && got.Run.Reason == reason {
				if !got.Run.OutcomeUnseen || got.Run.Paused || got.Run.FinishedAt != nil || got.Run.ContainerRetainedUntil != nil {
					t.Fatalf("report stopped the Enhanced session: %+v", got.Run)
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("reported outcome never became ready: %+v", got.Run)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	reportOutcome(protocol.CoordOutcomeSuccess, "first-turn", "agent reported success")

	inject, _ := json.Marshal(protocol.RunInjectParams{
		RunID: runID, Message: acpmock.PromptAskPermission, IdempotencyKey: "ask-1",
		ControlSessionID: lease.ControlSessionID, ControlGeneration: lease.ControlGeneration,
	})
	var posted protocol.RunRoomPostResult
	if status := postJSON(t, web+"/api/v1/run.inject", string(inject), &posted); status != http.StatusOK || posted.Receipt != "sent" || posted.Outcome != acphost.OutcomeSent {
		t.Fatalf("run.inject %d %+v", status, posted)
	}
	next("the permission request", func() bool { return pending != nil })
	answer := func() (int, webgate.ErrorBody) {
		body, _ := json.Marshal(protocol.RunInputAnswerParams{RunID: runID, RequestID: pending.ID, OptionID: "allow", ACPLease: lease})
		var out webgate.ErrorBody
		return postJSON(t, web+"/api/v1/run.input.answer", string(body), &out), out
	}
	if status, _ := answer(); status != http.StatusOK {
		t.Fatalf("run.input.answer status %d", status)
	}
	if status, out := answer(); status == http.StatusOK || out.Error == nil || out.Error.Code != protocol.CodeConflict ||
		!strings.Contains(string(out.Error.Data), protocol.ErrorReasonAlreadyAnswered) {
		t.Fatalf("second answer: %d %+v", status, out.Error)
	}
	next("the answered turn", func() bool { return strings.Contains(said.String(), "permission: allow") })
	waitTurns(ctx, t, env.srv, domain.RunID(runID), 2)
	reportOutcome(protocol.CoordOutcomeFailure, "second-turn", "agent reported failure")
	followup, _ := json.Marshal(protocol.RunInjectParams{
		RunID: runID, Message: "say pong", IdempotencyKey: "follow-up-after-failure",
		ControlSessionID: lease.ControlSessionID, ControlGeneration: lease.ControlGeneration,
	})
	if status := postJSON(t, web+"/api/v1/run.inject", string(followup), &posted); status != http.StatusOK || posted.Receipt != "sent" {
		t.Fatalf("follow-up after reported failure: %d %+v", status, posted)
	}
	next("the follow-up answer on the same stream", func() bool { return strings.Count(said.String(), "pong") == 2 })
	waitTurns(ctx, t, env.srv, domain.RunID(runID), 3)
	continued, err := env.srv.Store().GetRun(ctx, domain.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if original.HarnessSessionID == "" || continued.HarnessSessionID != original.HarnessSessionID ||
		continued.OutcomeUnseen || continued.FinishedAt != nil {
		t.Fatalf("follow-up did not preserve the original agent session: before=%+v after=%+v", original, continued)
	}

	closeParams, _ := json.Marshal(protocol.RunCloseParams{RunID: runID, Outcome: string(domain.RunAbandoned)})
	var closed protocol.RunResult
	if status := postJSON(t, web+"/api/v1/run.close", string(closeParams), &closed); status != http.StatusOK {
		t.Fatalf("run.close status %d", status)
	}
	for {
		var frame protocol.ACPFrame
		err := wsjson.Read(ctx, stream, &frame)
		if err == nil {
			continue
		}
		if websocket.CloseStatus(err) != websocket.StatusServiceRestart {
			t.Fatalf("stream after close: %v, want 1012", err)
		}
		break
	}
}
