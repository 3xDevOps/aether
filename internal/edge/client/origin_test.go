package edgeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// recorder is an origin that answers nothing and records the
// Authorization header of every request that reaches it.
type recorder struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.auths = append(r.auths, req.Method+" "+req.URL.Path+" "+req.Header.Get("Authorization"))
		r.mu.Unlock()
		writeJSON(w, http.StatusNotFound, edgeproto.ErrorBody{Error: "nothing here"})
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auths...)
}

// signinOrigin serves the device flow and the servers list as a sign-in
// origin apart from the relay, and records the bearer tokens it receives.
type signinOrigin struct {
	*recorder
	mux *http.ServeMux
}

func newSigninOrigin(t *testing.T) *signinOrigin {
	t.Helper()
	s := &signinOrigin{recorder: &recorder{}, mux: http.NewServeMux()}
	var key string
	s.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		key = req.Key
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: s.URL + "/device", Interval: 1,
		})
	})
	s.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.DeviceTokenResponse{
			Token:   edgeproto.NewToken(),
			Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
			Account: testAccount("42", "octo"),
		})
	})
	s.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{}})
	})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		s.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestSignInHappensAtTheSigninOriginTheRelayNames(t *testing.T) {
	relay := newFakeEdge(t)
	signin := newSigninOrigin(t)
	relay.setSignin(signin.URL)
	c := newTestClient(t, relay)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if l.SigninOrigin != signin.URL || !strings.HasPrefix(l.VerificationURI, signin.URL+"/") {
		t.Fatalf("login = %+v, want the verification page on %s", l, signin.URL)
	}
	s, err := c.Wait(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if s.SigninOrigin != signin.URL || !edgeproto.ValidAccountID(s.Account.ID) {
		t.Fatalf("session = %+v", s)
	}
	if _, err = c.Servers(context.Background()); err != nil {
		t.Fatalf("Servers at the sign-in origin: %v", err)
	}
	stored, err := c.session()
	if err != nil {
		t.Fatal(err)
	}
	if got := signin.seen(); got[len(got)-1] != "GET "+edgeproto.PathServers+" Bearer "+stored.Token {
		t.Fatalf("the sign-in origin saw %q last, want the servers list with the device token", got)
	}
}

func TestMetadataIsCheckedBeforeSignIn(t *testing.T) {
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	good := edgeproto.EdgeInfo{
		SigninOrigin: "https://auth.example.test", Key: key, Fingerprint: edgeproto.EdgeKeyFingerprint(key),
		Version: edgeproto.Version, MinVersion: edgeproto.MinVersion,
	}
	for name, change := range map[string]func(*edgeproto.EdgeInfo){
		"a plain http sign-in origin":  func(i *edgeproto.EdgeInfo) { i.SigninOrigin = "http://auth.example.test" },
		"a sign-in origin with a path": func(i *edgeproto.EdgeInfo) { i.SigninOrigin = "https://auth.example.test/x" },
		"a fingerprint of another key": func(i *edgeproto.EdgeInfo) { i.Fingerprint = "SHA256:other" },
		"a newer minimum version":      func(i *edgeproto.EdgeInfo) { i.MinVersion, i.Version = edgeproto.Version+1, edgeproto.Version+1 },
	} {
		t.Run(name, func(t *testing.T) {
			info := good
			change(&info)
			var started bool
			mux := http.NewServeMux()
			mux.HandleFunc("GET "+edgeproto.PathEdgeInfo, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, info)
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { started = true })
			edge := httptest.NewServer(mux)
			t.Cleanup(edge.Close)
			c, err := New(t.TempDir(), edge.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.StartLogin(context.Background(), "laptop"); err == nil || started {
				t.Fatalf("StartLogin = %v, started %v; want the metadata refused before any sign-in request", err, started)
			}
		})
	}
}

// The device token leaves this machine for the relay origin it is stored
// under and the sign-in origin that issued it. Neither a metadata change
// nor a redirect from either origin sends it anywhere else.
func TestTokenReachesOnlyTheTwoOriginsThatIssuedIt(t *testing.T) {
	relay := newFakeEdge(t)
	signin := newSigninOrigin(t)
	relay.setSignin(signin.URL)
	elsewhere := newRecorder(t)
	relay.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	signin.mux.HandleFunc("POST "+edgeproto.PathLogout, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	c := newTestClient(t, relay)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Wait(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	relay.setSignin(elsewhere.URL)
	if _, err := c.Servers(context.Background()); err != nil {
		t.Fatalf("Servers after the metadata changed: %v", err)
	}
	if _, err := c.Dial(context.Background(), testServerID); err == nil {
		t.Fatal("Dial followed a redirect")
	}
	if err := c.Logout(context.Background()); err == nil {
		t.Fatal("Logout followed a redirect")
	}
	if got := elsewhere.seen(); len(got) != 0 {
		t.Fatalf("another origin received %q", got)
	}
	// A new sign-in follows the new metadata, with nothing to send.
	if _, err := c.StartLogin(context.Background(), "laptop"); err == nil {
		t.Fatal("StartLogin succeeded against an origin that serves no sign-in")
	}
	for _, seen := range elsewhere.seen() {
		if strings.Contains(seen, "Bearer") {
			t.Fatalf("a sign-in at the new origin carried a token: %q", seen)
		}
	}
}

func TestServersParsesPolicyAndKind(t *testing.T) {
	edge := newFakeEdge(t)
	var servers []edgeproto.ServerInfo
	edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.ServersResponse{Servers: servers})
	})
	c := newTestClient(t, edge)
	signIn(t, c)

	servers = []edgeproto.ServerInfo{
		{ID: testServerID, Name: "a", Role: "admin"},
		{ID: testServerID, Name: "b", Role: "member", AccessPolicy: edgeproto.PolicyAccount, Kind: edgeproto.ServerHosted},
	}
	got, err := c.Servers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].AccessPolicy != edgeproto.PolicyApprovedDevices || got[0].Kind != edgeproto.ServerSelfHosted {
		t.Fatalf("a server that names neither = %+v, want approved-devices and self-hosted", got[0])
	}
	if got[1].AccessPolicy != edgeproto.PolicyAccount || got[1].Kind != edgeproto.ServerHosted {
		t.Fatalf("server b = %+v", got[1])
	}

	for _, bad := range []edgeproto.ServerInfo{
		{ID: testServerID, Name: "a", Role: "admin", AccessPolicy: "open"},
		{ID: testServerID, Name: "a", Role: "admin", Kind: "cloud"},
	} {
		servers = []edgeproto.ServerInfo{bad}
		if _, err := c.Servers(context.Background()); err == nil || !strings.Contains(err.Error(), testServerID) {
			t.Errorf("Servers with %+v = %v, want it refused naming the server", bad, err)
		}
	}
}

func TestDialClaimUsesTheClaimPath(t *testing.T) {
	edge := newFakeEdge(t)
	var gotAuth string
	edge.mux.HandleFunc("GET "+edgeproto.PathClaimConnect, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.PathValue("server_id") != testServerID {
			writeJSON(w, http.StatusNotFound, edgeproto.ErrorBody{Error: string(edgeproto.RefusalUnknownServer)})
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		defer func() { _ = nc.Close() }()
		_, _ = io.Copy(nc, nc)
	})
	c := newTestClient(t, edge)
	token := signIn(t, c)
	nc, err := c.DialClaim(context.Background(), testServerID)
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.Close()
	if gotAuth != "Bearer "+token {
		t.Fatal("the claim path did not receive the device token")
	}

	claimed := newFakeEdge(t)
	claimed.mux.HandleFunc("GET "+edgeproto.PathClaimConnect, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, edgeproto.RefusalClaimed.Status(), edgeproto.ErrorBody{Error: string(edgeproto.RefusalClaimed)})
	})
	c = newTestClient(t, claimed)
	signIn(t, c)
	if _, err := c.DialClaim(context.Background(), testServerID); !errors.Is(err, edgeproto.RefusalClaimed) ||
		!strings.Contains(err.Error(), "claim server "+testServerID) {
		t.Fatalf("DialClaim on an owned server = %v, want %q", err, edgeproto.RefusalClaimed)
	}
}
