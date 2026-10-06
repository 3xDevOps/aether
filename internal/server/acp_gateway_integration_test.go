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

// TestIntegrationEnhancedRunGateway drives an enhanced run through the
// dashboard gateway on real Docker, with the acpmock agent as its ACP server,
// the way the session view does.
func TestIntegrationEnhancedRunGateway(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
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
	srv, err := New(ctx, Config{
		DataDir: filepath.Join(t.TempDir(), "data"), Addr: "127.0.0.1:0", Runtime: rt,
		StandardImage: "busybox", RunContainerTTL: -time.Second, WhoIs: whois,
		ServerBinary: buildServerBinary(t),
		Harnesses: map[string]scheduler.HarnessSpec{
			"fake": {ACPArgs: []string{"sh", "-c", "exec /workspace/acp-mock"}},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { _ = srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)
	gw, err := servergw.New(servergw.Config{SSH: srv.ssh})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gw.Close() }()
	server := httptest.NewServer(gw)
	defer server.Close()
	web := server.URL

	var caps protocol.GatewayCapabilities
	if status := getJSON(t, web+"/api/v1/capabilities", &caps); status != http.StatusOK || !strings.Contains(strings.Join(caps.WS, ","), "acp") {
		t.Fatalf("capabilities %d %+v, want ws acp", status, caps)
	}
	ws := &domain.Workspace{Name: "enhanced", BaseBranch: domain.DefaultBaseBranch}
	if err := srv.Store().CreateWorkspace(ctx, ws); err != nil {
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
	runGit(t, seedDir, gitEnv, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID), "main")

	var launched protocol.RunResult
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: "say pong", Harness: "fake", Mode: string(domain.LaunchACP)})
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
	stopServer()
	_ = srv.Close()
	verifyNoLeaks(t)
}
