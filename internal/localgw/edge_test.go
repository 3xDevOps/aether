package localgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/edgeproto"
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
			Token:   e.token,
			Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
			Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
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
	if started.State != loginPending || started.UserCode != "USER-0001" || started.VerificationURI != edge.URL+"/device" || started.Edge != edge.URL {
		t.Fatalf("edge.login = %+v", started)
	}
	st, body := awaitLogin(t, g, loginSignedIn)
	if st.Login.Account == nil || st.Login.Account.Login != "octo" {
		t.Fatalf("signed-in login = %+v", st.Login)
	}
	if len(st.Edges) != 1 || st.Edges[0].Edge != edge.URL || st.Edges[0].Account == nil || st.Edges[0].Device.ID != "dev-1" {
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
	for _, verb := range []string{"edge.claim", "edge.link", "edge.login", "edge.logout", "edge.servers", "edge.status"} {
		if rec := do(g, http.MethodPost, "/local/v1/"+verb, `{"edge":"`+edge.URL+`"}`, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without the token = %d, want 401", verb, rec.Code)
		}
	}
	for _, tc := range []struct{ verb, body string }{
		{"edge.link", `{"edge":"` + edge.URL + `","server_id":"not-an-id"}`},
		{"edge.link", `{"edge":"` + edge.URL + `"}`},
		{"edge.claim", `{"edge":"` + edge.URL + `","code":"wrong"}`},
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
