package localgw

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// fakeEdge answers the edge's device sign-in, server list and logout. A
// sign-in is approved on its first poll unless hold is set.
type fakeEdge struct {
	*httptest.Server
	mu       sync.Mutex
	hold     bool
	starts   int
	requests int
	keys     map[string]string // device code -> device key
	token    string
	loggedIn string // bearer token presented to logout
}

func newFakeEdge(t *testing.T) *fakeEdge {
	t.Helper()
	e := &fakeEdge{keys: map[string]string{}, token: edgeproto.NewToken()}
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set(edgeproto.HeaderVersion, "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET "+edgeproto.PathEdgeInfo, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, edgeproto.EdgeInfo{
			SigninOrigin: e.URL, Key: key, Fingerprint: edgeproto.EdgeKeyFingerprint(key),
			Version: edgeproto.Version, MinVersion: edgeproto.MinVersion,
		})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Validate() != nil {
			reply(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: "bad start"})
			return
		}
		e.mu.Lock()
		e.starts++
		code := fmt.Sprintf("secret-device-code-%d", e.starts)
		user := fmt.Sprintf("USER-%04d", e.starts)
		e.keys[code] = req.Key
		e.mu.Unlock()
		reply(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: code, UserCode: user, VerificationURI: e.URL + "/device", ExpiresIn: 60, Interval: 1,
		})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		e.mu.Lock()
		key, hold := e.keys[req.DeviceCode], e.hold
		e.mu.Unlock()
		if hold {
			reply(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: edgeproto.DevicePending})
			return
		}
		reply(w, http.StatusOK, edgeproto.DeviceTokenResponse{
			Token:  e.token,
			Device: edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
			Account: edgeproto.AccountInfo{
				ID:      edgeproto.NewAccountID(),
				Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
			},
		})
	})
	mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{
			{ID: "wqc4lsjvzdzrwq3k5dabdtajwj", Name: "build-box", Online: true, Role: "admin"},
		}})
	})
	mux.HandleFunc("POST "+edgeproto.PathLogout, func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.loggedIn = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.mu.Unlock()
		w.Header().Set(edgeproto.HeaderVersion, "1")
		w.WriteHeader(http.StatusNoContent)
	})
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests++
		e.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(e.Close)
	return e
}

func (e *fakeEdge) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests
}

// edgeGateway is a gateway with a scratch config directory; tests close
// it themselves so the sign-in goroutine is gone before that directory is.
func edgeGateway(t *testing.T) *Gateway {
	t.Helper()
	testhome.Isolate(t)
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	t.Cleanup(func() { _ = g.Close() })
	return g
}

type edgeStatus struct {
	Edges []signedInEdge `json:"edges"`
	Login *loginView     `json:"login"`
}

func awaitLogin(t *testing.T, g *Gateway, state string) (edgeStatus, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := do(g, http.MethodPost, "/local/v1/edge.status", "{}", true)
		var st edgeStatus
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
			t.Fatalf("edge.status = %d: %s", rec.Code, rec.Body)
		}
		if st.Login != nil && st.Login.State == state {
			return st, rec.Body.String()
		}
		if time.Now().After(deadline) {
			t.Fatalf("edge.status never reached %s: %s", state, rec.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEdgeLoginNeverAnswersTheTokenOrDeviceCode(t *testing.T) {
	edge := newFakeEdge(t)
	g := edgeGateway(t)

	rec := do(g, http.MethodPost, "/local/v1/edge.login", `{"edge":"`+edge.URL+`","label":"laptop"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("edge.login = %d: %s", rec.Code, rec.Body)
	}
	var started loginView
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.State != loginPending || started.UserCode != "USER-0001" || started.VerificationURI != edge.URL+"/device" ||
		started.Edge != edge.URL || started.SigninOrigin != edge.URL {
		t.Fatalf("edge.login = %+v", started)
	}
	st, body := awaitLogin(t, g, loginSignedIn)
	if st.Login.Account == nil || st.Login.Account.Login != "octo" {
		t.Fatalf("signed-in login = %+v", st.Login)
	}
	if len(st.Edges) != 1 || st.Edges[0].Edge != edge.URL || st.Edges[0].SigninOrigin != edge.URL ||
		st.Edges[0].Account == nil || st.Edges[0].Device.ID != "dev-1" {
		t.Fatalf("edges = %+v", st.Edges)
	}
	for _, answer := range []string{rec.Body.String(), body} {
		if strings.Contains(answer, edge.token) || strings.Contains(answer, "secret-device-code") {
			t.Fatalf("an answer carries a device secret: %s", answer)
		}
	}
}

func TestEdgeLoginReplacesTheOneInProgress(t *testing.T) {
	edge := newFakeEdge(t)
	edge.hold = true
	g := edgeGateway(t)
	for range 2 {
		if rec := do(g, http.MethodPost, "/local/v1/edge.login", `{"edge":"`+edge.URL+`","label":"laptop"}`, true); rec.Code != http.StatusOK {
			t.Fatalf("edge.login = %d: %s", rec.Code, rec.Body)
		}
	}
	st, _ := awaitLogin(t, g, loginPending)
	if st.Login.UserCode != "USER-0002" {
		t.Fatalf("login = %+v, want the second sign-in", st.Login)
	}
	closed := make(chan struct{})
	go func() {
		_ = g.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the pending sign-in")
	}
}

func TestEdgeLogoutRevokesAndForgets(t *testing.T) {
	edge := newFakeEdge(t)
	g := edgeGateway(t)
	do(g, http.MethodPost, "/local/v1/edge.login", `{"edge":"`+edge.URL+`","label":"laptop"}`, true)
	awaitLogin(t, g, loginSignedIn)

	rec := do(g, http.MethodPost, "/local/v1/edge.servers", "{}", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"build-box"`) {
		t.Fatalf("edge.servers = %d: %s", rec.Code, rec.Body)
	}
	if rec = do(g, http.MethodPost, "/local/v1/edge.logout", "{}", true); rec.Code != http.StatusOK {
		t.Fatalf("edge.logout = %d: %s", rec.Code, rec.Body)
	}
	edge.mu.Lock()
	presented := edge.loggedIn
	edge.mu.Unlock()
	if presented != edge.token {
		t.Fatal("logout did not present the device token")
	}
	rec = do(g, http.MethodPost, "/local/v1/edge.status", "{}", true)
	if rec.Body.String() != "{\"edges\":[]}\n" {
		t.Fatalf("edge.status after logout = %s", rec.Body)
	}
	rec = do(g, http.MethodPost, "/local/v1/edge.servers", `{"edge":"`+edge.URL+`"}`, true)
	if rec.Code != http.StatusConflict || !strings.Contains(decodeError(t, rec.Body.Bytes()).Message, "not signed in") {
		t.Fatalf("edge.servers signed out = %d: %s", rec.Code, rec.Body)
	}
}

// Every edge verb is refused without the gateway token, and one with a
// malformed argument is refused before anything reaches the edge.
func TestEdgeVerbsRefuseBeforeReachingTheEdge(t *testing.T) {
	edge := newFakeEdge(t)
	g := edgeGateway(t)
	for _, verb := range []string{"edge.claim", "edge.hostkey", "edge.join", "edge.link", "edge.login", "edge.logout", "edge.servers", "edge.status"} {
		if rec := do(g, http.MethodPost, "/local/v1/"+verb, `{"edge":"`+edge.URL+`"}`, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without the token = %d, want 401", verb, rec.Code)
		}
	}
	for _, tc := range []struct{ verb, body string }{
		{"edge.link", `{"edge":"` + edge.URL + `","server_id":"not-an-id"}`},
		{"edge.link", `{"edge":"` + edge.URL + `"}`},
		{"edge.claim", `{"edge":"` + edge.URL + `","code":"wrong"}`},
		{"edge.join", `{"link":"` + edge.URL + `/join/x/y#z"}`},
		{"edge.hostkey", `{"edge":"` + edge.URL + `","server_id":"not-an-id"}`},
		{"edge.login", `{"edge":"http://edge.example.test","label":"laptop"}`},
		{"edge.servers", `{"edge":"https://edge.example.test/path"}`},
	} {
		if rec := do(g, http.MethodPost, "/local/v1/"+tc.verb, tc.body, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400: %s", tc.verb, tc.body, rec.Code, rec.Body)
		}
	}
	if n := edge.count(); n != 0 {
		t.Fatalf("the edge received %d requests", n)
	}
}

func TestEdgeLinkOfAnAddressOnlyNamedProfileIsAddedBesideIt(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr: "default:2222",
		User: "alice",
		Links: []cli.NamedLink{
			{Name: "work", Addr: "work:2222"},
		},
	}
	saveConfigAt(t, initial, time.Unix(1_700_000_000, 0))
	selected, ok := initial.Named("work")
	if !ok {
		t.Fatal("initial named link missing")
	}
	g := newVerbGateway(t, workspaceListBackend(`{"id":"ws_1","name":"app","base_branch":"main","created_at":""}`), selected)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	connected := selected
	connected.EdgeURL, connected.ServerID = "https://edge.example", edgeproto.ServerID(hostKey)
	if perr := g.adoptLink(cli.LinkResult{Config: connected}, ""); perr != nil {
		t.Fatalf("adoptLink: %v", perr)
	}

	reloaded, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Addr != initial.Addr || reloaded.EdgeURL != "" || reloaded.ServerID != "" {
		t.Fatalf("default link changed: %+v", reloaded)
	}
	if work, ok := reloaded.Named("work"); !ok || work.Addr != "work:2222" || work.EdgeURL != "" || work.ServerID != "" {
		t.Fatalf("named link work = %+v, present %v; want it unchanged", work, ok)
	}
	if edge, ok := reloaded.ByServerID(connected.ServerID); !ok || edge.EdgeURL != connected.EdgeURL || edge.Addr != "work:2222" {
		t.Fatalf("edge link = %+v, present %v", edge, ok)
	}
	repo := t.TempDir()
	localGit(t, repo, "init")
	rec := do(g, http.MethodPost, "/local/v1/link.repo", `{"repo":`+strconv.Quote(repo)+`,"workspace_id":"ws_1"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("link.repo = %d: %s", rec.Code, rec.Body)
	}
}

func TestEdgeLinkUnderAnotherNameOnANamedProfileSavesBoth(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr:  "default:2222",
		User:  "alice",
		Links: []cli.NamedLink{{Name: "work", Addr: "work:2222", User: "bob"}},
	}
	saveConfigAt(t, initial, time.Unix(1_700_000_000, 0))
	selected, ok := initial.Named("work")
	if !ok {
		t.Fatal("initial named link missing")
	}
	g := newVerbGateway(t, workspaceListBackend(""), selected)
	connected := selected
	connected.EdgeURL, connected.ServerID = "https://edge.example", "aaaa"
	requested := cli.NamedLink{Name: "laptop", Addr: "work:2222", User: "bob", AutoKey: true, EdgeURL: connected.EdgeURL, ServerID: connected.ServerID}
	if perr := g.adoptLink(cli.LinkResult{Config: cli.UpsertLink(connected, requested)}, "laptop"); perr != nil {
		t.Fatalf("adoptLink: %v", perr)
	}

	reloaded, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Addr != initial.Addr || reloaded.ServerID != "" {
		t.Fatalf("default link changed: %+v", reloaded)
	}
	want := []cli.NamedLink{initial.Links[0], requested}
	if !slices.Equal(reloaded.Links, want) {
		t.Fatalf("saved links = %+v, want %+v", reloaded.Links, want)
	}
}

func TestLinkingAnotherServerFromANamedProfileAddsALink(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr: "default:2222",
		User: "alice",
		Links: []cli.NamedLink{
			{Name: "work", Addr: "work:2222", User: "bob"},
			{Name: "ab", Addr: "old:2222", ServerID: "other"},
		},
	}
	saveConfigAt(t, initial, time.Unix(1_700_000_000, 0))
	selected, ok := initial.Named("work")
	if !ok {
		t.Fatal("initial named link missing")
	}
	backend := &switchBackend{}
	g := newVerbGateway(t, backend, selected)
	linked := cli.Config{Addr: "new:2222", User: "carol", EdgeURL: "https://edge.example", ServerID: "ab"}
	if perr := g.adoptLink(cli.LinkResult{Config: linked}, ""); perr != nil {
		t.Fatalf("adoptLink: %v", perr)
	}

	reloaded, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Addr != initial.Addr || reloaded.User != initial.User || reloaded.GUIServer != "ab-2" {
		t.Fatalf("default link = %+v, want it unchanged and gui_server ab-2", reloaded)
	}
	want := append(slices.Clone(initial.Links), cli.NamedLink{Name: "ab-2", Addr: "new:2222", User: "carol", AutoKey: true, EdgeURL: linked.EdgeURL, ServerID: "ab"})
	if !slices.Equal(reloaded.Links, want) {
		t.Fatalf("saved links = %+v, want %+v", reloaded.Links, want)
	}
	if got := g.local.snapshot(); got.Active != "ab-2" || got.Addr != "new:2222" {
		t.Fatalf("cached link = %+v, want ab-2 active", got)
	}
	if len(backend.relinked) != 1 || backend.relinked[0].Active != "ab-2" {
		t.Fatalf("relinked = %+v, want the new link", backend.relinked)
	}
}

func TestLinkingANewServerIDAtTheSameAddressAddsALink(t *testing.T) {
	useTempConfigDir(t)
	initial := cli.Config{
		Addr:  "default:2222",
		Links: []cli.NamedLink{{Name: "work", Addr: "work:2222", EdgeURL: "https://edge.example", ServerID: "old"}},
	}
	saveConfigAt(t, initial, time.Unix(1_700_000_000, 0))
	selected, _ := initial.Named("work")
	g := newVerbGateway(t, &switchBackend{}, selected)
	linked := cli.Config{Addr: "work:2222", EdgeURL: "https://edge.example", ServerID: "new"}
	if perr := g.adoptLink(cli.LinkResult{Config: linked}, ""); perr != nil {
		t.Fatalf("adoptLink: %v", perr)
	}
	reloaded, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Addr != "default:2222" || reloaded.ServerID != "" || len(reloaded.Links) != 2 || reloaded.Links[0] != initial.Links[0] || reloaded.Links[1].ServerID != "new" {
		t.Fatalf("saved config = %+v, want the default and work kept and new added", reloaded)
	}
}
