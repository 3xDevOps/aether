//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/dashboard"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationDashboard is the dashboard lifecycle scenario: a stack
// booted with the dashboard enabled hands the token table to the SSH
// transport (dash.token.mint works over real SSH), and the minted token
// drives the fake agent over the same HTTP/WS wire the SPA uses - launch,
// the run appearing on the board, the live terminal view over the attach
// WebSocket, a steer injected from the card, and a second run killed from
// its card - while the events WebSocket streams both lifecycles, the
// patch endpoint renders the run's diff through the git engine handoff,
// and the disk endpoint reads the wired data directory. A tokenless
// request is refused. The gateway's own behaviours live in
// internal/dashboard's tests; this scenario pins the seams.
func TestIntegrationDashboard(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", DashboardAddr: "127.0.0.1:0", Runtime: rt})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Dash Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#3cb44b",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{Name: "dash", Environment: domain.WorkspaceEnvironment{CustomImage: image}}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	sess := &domain.Session{WorkspaceID: ws.ID, Name: "dash-e2e", BaseBranch: "main"}
	if err = srv.Store().CreateSession(ctx, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}

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

	// The token table handoff: dash.token.mint is served on the SSH
	// control channel only through sshd.Config.Services.Dashboard, which
	// svc_dashboard.go must have attached before sshd.New consumed it.
	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)
	var minted protocol.DashTokenMintResult
	if err := ctrl.Call(protocol.MethodDashTokenMint, protocol.DashTokenMintParams{}, &minted); err != nil {
		t.Fatalf("dash.token.mint over SSH: %v", err)
	}
	if minted.Token == "" || minted.ExpiresAt == "" {
		t.Fatalf("dash.token.mint = %+v", minted)
	}
	base := "http://" + waitDashboardAddr(t, srv)

	// A tokenless request is refused before it reaches anything.
	resp, err := http.Get(base + "/api/v1/disk")
	if err != nil {
		t.Fatalf("unauthenticated GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET status = %d, want 401", resp.StatusCode)
	}

	// Subscribe to the events WebSocket before launching, so the run's
	// whole lifecycle arrives on it.
	wsCtx, wsCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer wsCancel()
	evConn, _, err := websocket.Dial(wsCtx, "ws"+base[len("http"):]+"/ws/events?token="+minted.Token, nil)
	if err != nil {
		t.Fatalf("dial /ws/events: %v", err)
	}
	defer evConn.CloseNow()
	if err := wsjson.Write(wsCtx, evConn, protocol.SubscribeRequest{}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	var ack protocol.SubscribeResponse
	if err := wsjson.Read(wsCtx, evConn, &ack); err != nil || !ack.OK {
		t.Fatalf("subscribe ack = %+v (err %v)", ack, err)
	}

	// Drive the fake agent over the gateway: launch, hydrate the board,
	// inject. Every call runs through the same control-channel handlers
	// the SSH transport serves, via the RPC bridge the wiring passed in.
	var launched protocol.RunResult
	dashAPI(t, base, minted.Token, protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(sess.ID), Task: "dashboard e2e", Harness: "fake",
	}, &launched)
	if launched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("launched run status = %q, want running", launched.Run.Status)
	}

	var board protocol.RunListResult
	dashAPI(t, base, minted.Token, protocol.MethodRunList, protocol.RunListParams{SessionID: string(sess.ID)}, &board)
	found := false
	for _, r := range board.Runs {
		found = found || r.ID == launched.Run.ID
	}
	if !found {
		t.Fatalf("hydrated board %+v is missing launched run %s", board.Runs, launched.Run.ID)
	}

	// The terminal view: the run's live PTY over the gateway's attach
	// WebSocket - the SPA's terminal wire, not the SSH subsystem.
	term := openWSAttach(t, wsCtx, base, minted.Token, launched.Run.ID)
	term.waitOutput(t, "agent-ready")
	dashAPI(t, base, minted.Token, protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: launched.Run.ID, Message: "ping-dash",
	}, nil)
	term.waitOutput(t, "got:ping-dash")

	// The lifecycle reaches the browser transport: the run parks at
	// needs-attention on the WebSocket stream.
	waitWireStatus(t, wsCtx, evConn, launched.Run.ID, domain.RunNeedsAttention)

	// Kill from the card: a second run - its agent parked waiting for
	// input - is stopped through the gateway and lands abandoned
	// ("killed") on the events stream.
	var doomed protocol.RunResult
	dashAPI(t, base, minted.Token, protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(sess.ID), Task: "dashboard kill", Harness: "fake",
	}, &doomed)
	victim := openWSAttach(t, wsCtx, base, minted.Token, doomed.Run.ID)
	victim.waitOutput(t, "agent-ready")
	dashAPI(t, base, minted.Token, protocol.MethodRunKill, protocol.RunIDParams{RunID: doomed.Run.ID}, nil)
	waitWireStatus(t, wsCtx, evConn, doomed.Run.ID, domain.RunAbandoned)

	// The patch endpoint renders the run's committed result through the
	// git engine the wiring handed over.
	var patch struct {
		RunID string `json:"run_id"`
		Base  string `json:"base"`
		Patch string `json:"patch"`
	}
	dashGET(t, base+"/api/v1/run/"+launched.Run.ID+"/patch", minted.Token, &patch)
	if patch.RunID != launched.Run.ID || patch.Base == "" {
		t.Fatalf("patch response = %+v", patch)
	}
	if !bytes.Contains([]byte(patch.Patch), []byte("result.txt")) {
		t.Fatalf("patch text is missing result.txt: %q", patch.Patch)
	}

	// The disk endpoint reads the data directory the wiring passed in.
	var disk struct {
		UsedBytes  uint64 `json:"used_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	}
	dashGET(t, base+"/api/v1/disk", minted.Token, &disk)
	if disk.TotalBytes == 0 {
		t.Fatalf("disk response = %+v", disk)
	}

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

// wsAttach is a live /ws/attach socket with its binary terminal frames
// pumped into a buffer - attachConn's shape, on the browser transport.
type wsAttach struct {
	mu  sync.Mutex
	buf bytes.Buffer
	eof bool
}

// openWSAttach opens the gateway's attach WebSocket as a read-only
// terminal mirror and checks the ack.
func openWSAttach(t *testing.T, ctx context.Context, base, token, runID string) *wsAttach {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+base[len("http"):]+"/ws/attach/"+runID+"?token="+token, nil)
	if err != nil {
		t.Fatalf("dial /ws/attach/%s: %v", runID, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, protocol.DashAttachRequest{Cols: 120, Rows: 30}); err != nil {
		t.Fatalf("write ws attach header: %v", err)
	}
	var ack protocol.AttachResponse
	if err := wsjson.Read(ctx, conn, &ack); err != nil || !ack.OK {
		t.Fatalf("ws attach ack = %+v (err %v)", ack, err)
	}
	a := &wsAttach{}
	go a.pump(ctx, conn)
	return a
}

func (a *wsAttach) pump(ctx context.Context, conn *websocket.Conn) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			a.mu.Lock()
			a.eof = true
			a.mu.Unlock()
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		a.mu.Lock()
		a.buf.Write(data)
		a.mu.Unlock()
	}
}

func (a *wsAttach) output() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.String()
}

func (a *wsAttach) waitOutput(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if strings.Contains(a.output(), substr) {
			return
		}
		a.mu.Lock()
		ended := a.eof
		a.mu.Unlock()
		if ended {
			t.Fatalf("ws attach ended before %q appeared; output %q", substr, a.output())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in ws attach output %q", substr, a.output())
}

// waitWireStatus reads wire events until run reaches status, failing on
// the context deadline.
func waitWireStatus(t *testing.T, ctx context.Context, conn *websocket.Conn, run string, status domain.RunStatus) {
	t.Helper()
	for {
		var ev protocol.Event
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			t.Fatalf("read event waiting for run %s status %q: %v", run, status, err)
		}
		if ev.RunID != run || ev.Type != "run.status" {
			continue
		}
		var p struct {
			To domain.RunStatus `json:"to"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode run.status payload %s: %v", ev.Payload, err)
		}
		if p.To == status {
			return
		}
	}
}

// dashAPI POSTs one control-channel method through the gateway's bearer
// transport, decoding the result when out is non-nil.
func dashAPI(t *testing.T, base, token, method string, params, out any) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/"+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	doJSON(t, req, method, out)
}

func dashGET(t *testing.T, url, token string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	doJSON(t, req, url, out)
}

func doJSON(t *testing.T, req *http.Request, what string, out any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: read body: %v", what, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", what, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s: decode %q: %v", what, data, err)
		}
	}
}

// waitDashboardAddr returns the gateway's dynamically bound address once
// Start has bound it. Reading the ":0" bind back replaces the old
// reserve-then-release port dance, which another process could win in the
// gap before the gateway's own listen.
func waitDashboardAddr(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range srv.services {
			gw, ok := n.svc.(*dashboard.Gateway)
			if !ok {
				continue
			}
			if addrs := gw.Addrs(); len(addrs) > 0 {
				return addrs[0].String()
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("dashboard listener never bound")
	return ""
}
