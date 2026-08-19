package dashboard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// env is a gateway wired to a real store, a real event bus, and the real
// control-channel method registry, listening on a real port: the tests
// drive it exactly as a browser would.
type env struct {
	t          *testing.T
	db         *store.DB
	bus        *events.InProc
	gw         *Gateway
	pty        *fakePTY
	runs       *fakeRuns
	base       string
	invitesDir string
	sess       *domain.Session
	run        *domain.Run
	admin      domain.MemberID
	viewer     domain.MemberID
	// collab owns no run but may steer the admin's, until the run is
	// protected or the session restricts steering.
	collab domain.MemberID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log, err := events.OpenSQLiteLog(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(context.Background(), log)
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	ctx := t.Context()
	e := &env{t: t, db: db, bus: bus, pty: newFakePTY(), runs: &fakeRuns{}}
	admin := &domain.Member{DisplayName: "admin", Role: domain.RoleAdmin, PublicKey: testKey(t)}
	viewer := &domain.Member{DisplayName: "viewer", Role: domain.RoleViewer, PublicKey: testKey(t)}
	collab := &domain.Member{DisplayName: "collab", Role: domain.RoleCollaborator, PublicKey: testKey(t)}
	for _, m := range []*domain.Member{admin, viewer, collab} {
		if err = db.CreateMember(ctx, m); err != nil {
			t.Fatalf("create member: %v", err)
		}
	}
	e.admin, e.viewer, e.collab = admin.ID, viewer.ID, collab.ID
	ws := &domain.Workspace{Name: "app", Image: "img"}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	e.sess = &domain.Session{WorkspaceID: ws.ID, Name: "main", BaseBranch: "main"}
	if err = db.CreateSession(ctx, e.sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	e.run = &domain.Run{
		SessionID: e.sess.ID,
		MemberID:  admin.ID,
		Task:      "ship it",
		Harness:   "claude",
		Mode:      domain.LaunchTUI,
		Status:    domain.RunRunning,
		Branch:    "aether/run-1",
	}
	if err = db.CreateRun(ctx, e.run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Invites are configured so the allowlist test proves the HTTP refusal
	// is what stops member.invite, not a disabled feature.
	e.invitesDir = filepath.Join(dir, "invites")
	sshCfg := &sshd.Config{Store: db, Bus: bus, PTY: e.pty, Runs: e.runs, InvitesDir: e.invitesDir}
	gw, err := New(Config{
		Addr:       "127.0.0.1:0",
		RPC:        sshd.NewBridge(sshCfg),
		Bus:        bus,
		PTY:        e.pty,
		Static:     fstest.MapFS{"index.html": {Data: []byte("<!doctype html>spa")}},
		revalidate: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	sshCfg.Services.Dashboard = gw.Tokens()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	e.gw = gw
	e.base = "http://" + gw.Addrs()[0].String()
	return e
}

// mint issues a token the way a client does: over the control channel.
func (e *env) mint(member domain.MemberID) string {
	e.t.Helper()
	resp := e.gw.cfg.RPC.Call(e.t.Context(), member, protocol.MethodDashTokenMint, nil)
	if resp.Error != nil {
		e.t.Fatalf("dash.token.mint: %v", resp.Error)
	}
	var out protocol.DashTokenMintResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		e.t.Fatalf("decode mint result: %v", err)
	}
	return out.Token
}

func (e *env) post(token, method string, params any) (int, []byte) {
	e.t.Helper()
	var body io.Reader
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			e.t.Fatalf("encode params: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPost, e.base+"/api/v1/"+method, body)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("post %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

// errorCode extracts the wire error code from a failure body.
func (e *env) errorCode(body []byte) int {
	e.t.Helper()
	var out errorBody
	if err := json.Unmarshal(body, &out); err != nil || out.Error == nil {
		e.t.Fatalf("not an error body: %s", body)
	}
	return out.Error.Code
}

// TestAPIRunsTheSameHandlersAsSSH is the "one service layer, two
// transports" contract: the HTTP call reaches the registered method, and
// the capability gate the SSH transport applies denies the viewer here
// too - the request never reaches the run controller.
func TestAPIRunsTheSameHandlersAsSSH(t *testing.T) {
	e := newEnv(t)
	adminToken, viewerToken := e.mint(e.admin), e.mint(e.viewer)

	status, body := e.post(adminToken, protocol.MethodRunList, protocol.RunListParams{})
	if status != http.StatusOK {
		t.Fatalf("run.list status = %d, want 200 (%s)", status, body)
	}
	var runs protocol.RunListResult
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("decode run.list: %v", err)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].ID != string(e.run.ID) {
		t.Fatalf("run.list = %+v, want the one seeded run", runs.Runs)
	}

	// The token, not the request, carries the identity.
	status, body = e.post(viewerToken, protocol.MethodServerInfo, nil)
	if status != http.StatusOK {
		t.Fatalf("server.info status = %d, want 200 (%s)", status, body)
	}
	var info protocol.ServerInfoResult
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode server.info: %v", err)
	}
	if info.Member.ID != string(e.viewer) {
		t.Fatalf("server.info member = %q, want the viewer %q", info.Member.ID, e.viewer)
	}

	status, body = e.post(viewerToken, protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)})
	if status != http.StatusForbidden || e.errorCode(body) != protocol.CodeDenied {
		t.Fatalf("viewer run.kill = %d %s, want 403 and code %d", status, body, protocol.CodeDenied)
	}
	if e.runs.killed() != 0 {
		t.Fatal("viewer run.kill reached the run controller")
	}

	status, _ = e.post(adminToken, protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)})
	if status != http.StatusOK {
		t.Fatalf("admin run.kill status = %d, want 200", status)
	}
	if e.runs.killed() != 1 {
		t.Fatalf("run controller saw %d kills, want 1", e.runs.killed())
	}

	status, body = e.post(adminToken, "no.such.method", nil)
	if status != http.StatusForbidden || e.errorCode(body) != protocol.CodeDenied {
		t.Fatalf("unknown method = %d %s, want 403 and code %d", status, body, protocol.CodeDenied)
	}
}

// TestAPIRefusesCredentialMethods is the allowlist's reason for existing: a
// dashboard token travels in a URL, so it must not reach anything that
// mints a durable credential, administers members, or replaces what is
// mounted into run containers. Those stay on SSH, where the key is the
// credential.
func TestAPIRefusesCredentialMethods(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)

	for _, method := range []string{
		protocol.MethodMemberInvite,
		protocol.MethodMemberApprove,
		protocol.MethodMemberRemove,
		protocol.MethodProfilePush,
		protocol.MethodDashTokenMint,
		protocol.MethodWorkspaceAdd,
		protocol.MethodSessionSettings,
	} {
		status, body := e.post(token, method, nil)
		if status != http.StatusForbidden || e.errorCode(body) != protocol.CodeDenied {
			t.Errorf("admin %s over HTTP = %d %s, want 403 and code %d", method, status, body, protocol.CodeDenied)
		}
	}
	// No invite may have been written by any of that, though the same
	// member minting the same invite over SSH still works.
	if _, err := os.Stat(e.invitesDir); !os.IsNotExist(err) {
		t.Fatalf("member.invite over HTTP created %s", e.invitesDir)
	}
	if resp := e.gw.cfg.RPC.Call(t.Context(), e.admin, protocol.MethodMemberInvite, nil); resp.Error != nil {
		t.Fatalf("member.invite over SSH: %v", resp.Error)
	}
}

// TestAPIMethodsAreRegistered keeps the allowlist honest: a typo or a
// method that disappeared would otherwise sit there answering 403 forever.
func TestAPIMethodsAreRegistered(t *testing.T) {
	e := newEnv(t)
	for method := range apiMethods {
		resp := e.gw.cfg.RPC.Call(t.Context(), e.admin, method, nil)
		if resp.Error != nil && resp.Error.Code == protocol.CodeMethodNotFound {
			t.Errorf("allowlisted %s is not a registered control-channel method", method)
		}
	}
}

func TestAPIRejectsBadTokens(t *testing.T) {
	e := newEnv(t)
	token := e.mint(e.admin)

	for name, tok := range map[string]string{"missing": "", "unknown": "not-a-token"} {
		if status, _ := e.post(tok, protocol.MethodRunList, nil); status != http.StatusUnauthorized {
			t.Fatalf("%s token: status = %d, want 401", name, status)
		}
	}

	// A member may not revoke someone else's token, and a revoked token
	// stops working immediately.
	resp := e.gw.cfg.RPC.Call(t.Context(), e.viewer, protocol.MethodDashTokenRevoke,
		mustJSON(t, protocol.DashTokenRevokeParams{Token: token}))
	if resp.Error == nil {
		t.Fatal("viewer revoked the admin's token")
	}
	if status, _ := e.post(token, protocol.MethodRunList, nil); status != http.StatusOK {
		t.Fatalf("token after a foreign revoke: status = %d, want 200", status)
	}
	resp = e.gw.cfg.RPC.Call(t.Context(), e.admin, protocol.MethodDashTokenRevoke,
		mustJSON(t, protocol.DashTokenRevokeParams{Token: token}))
	if resp.Error != nil {
		t.Fatalf("dash.token.revoke: %v", resp.Error)
	}
	if status, _ := e.post(token, protocol.MethodRunList, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoked token: status = %d, want 401", status)
	}

	// A token cannot mint its own successor: that path stays on SSH.
	if status, body := e.post(e.mint(e.admin), protocol.MethodDashTokenMint, nil); status != http.StatusForbidden {
		t.Fatalf("dash.token.mint over HTTP = %d %s, want 403", status, body)
	}

	expired, _, err := e.gw.Tokens().Mint(e.admin, time.Nanosecond)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if status, _ := e.post(expired, protocol.MethodRunList, nil); status != http.StatusUnauthorized {
		t.Fatalf("expired token: status = %d, want 401", status)
	}
}

func TestStaticServesSPAWithFallback(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html>spa")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := staticHandler(spa)
	for path, want := range map[string]string{
		"/":              "<!doctype html>spa",
		"/runs/abc":      "<!doctype html>spa",
		"/assets/app.js": "console.log(1)",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}

	rec := httptest.NewRecorder()
	staticHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unbuilt dashboard = %d, want 503", rec.Code)
	}
}

// TestWrongMethodIsAnErrorNotTheSPA: an /api or /ws request with the
// wrong verb misses the method-qualified mux patterns and falls into the
// catch-all, which must answer an error rather than 200 index.html - a
// silent 200 makes a client's method mistake look like success.
func TestWrongMethodIsAnErrorNotTheSPA(t *testing.T) {
	e := newEnv(t)
	for method, path := range map[string]string{
		http.MethodGet:  "/api/v1/run.list",
		http.MethodPost: "/ws/events",
	} {
		req, err := http.NewRequestWithContext(t.Context(), method, e.base+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d %q, want 405", method, path, resp.StatusCode, body)
		}
	}
}

func testKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

// fakeRuns stands in for the scheduler: the tests assert on whether a
// request reached it, not on what it did.
type fakeRuns struct {
	mu    sync.Mutex
	kills int
}

func (f *fakeRuns) killed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kills
}

func (f *fakeRuns) Kill(context.Context, domain.RunID, domain.MemberID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills++
	return nil
}

func (f *fakeRuns) Launch(context.Context, domain.SessionID, domain.MemberID, string, string, domain.LaunchMode) (*domain.Run, error) {
	return &domain.Run{}, nil
}
func (f *fakeRuns) Pause(context.Context, domain.RunID, domain.MemberID) error  { return nil }
func (f *fakeRuns) Resume(context.Context, domain.RunID, domain.MemberID) error { return nil }
func (f *fakeRuns) Inject(context.Context, domain.RunID, domain.MemberID, string) error {
	return nil
}
func (f *fakeRuns) CloseRun(context.Context, domain.RunID, domain.MemberID, domain.RunStatus) error {
	return nil
}
func (f *fakeRuns) Relaunch(context.Context, domain.RunID, domain.MemberID) (*domain.Run, error) {
	return &domain.Run{}, nil
}
func (f *fakeRuns) SetupLogin(context.Context, domain.MemberID, string, string, uint, uint, io.ReadWriter, <-chan [2]uint) error {
	return nil
}
