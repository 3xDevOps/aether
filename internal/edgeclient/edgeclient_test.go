package edgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

const testServerID = "wqc4lsjvzdzrwq3k5dabdtajwj"

func init() { pollUnit = time.Millisecond }

// fakeEdge is an httptest edge whose handlers a test sets per path.
type fakeEdge struct {
	*httptest.Server
	mux *http.ServeMux
}

func newFakeEdge(t *testing.T) *fakeEdge {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakeEdge{Server: srv, mux: mux}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(edgeproto.HeaderVersion, "1")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestClient(t *testing.T, edge *fakeEdge) *Client {
	t.Helper()
	c, err := New(t.TempDir(), edge.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// signIn stores a session for c the way a completed login does.
func signIn(t *testing.T, c *Client) string {
	t.Helper()
	signer, err := EnsureDeviceKey(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	token := edgeproto.NewToken()
	l := &Login{key: edgeproto.DeviceKeyLine(signer.PublicKey())}
	_, err = c.store(l, edgeproto.DeviceTokenResponse{
		Token:   token,
		Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: l.key},
		Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "octo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestDeviceFlowPollsThroughPendingAndSlowDown(t *testing.T) {
	edge := newFakeEdge(t)
	var (
		mu    sync.Mutex
		polls []time.Time
		key   string
	)
	token := edgeproto.NewToken()
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Validate() != nil {
			writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: "bad start"})
			return
		}
		key = req.Key
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH",
			VerificationURI: edge.URL + "/device", ExpiresIn: 60, Interval: 10,
		})
	})
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		polls = append(polls, time.Now())
		n := len(polls)
		mu.Unlock()
		switch n {
		case 1:
			writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: edgeproto.DevicePending})
		case 2:
			writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: edgeproto.DeviceSlowDown})
		default:
			writeJSON(w, http.StatusOK, edgeproto.DeviceTokenResponse{
				Token:   token,
				Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
				Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
			})
		}
	})
	c := newTestClient(t, edge)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if l.UserCode != "ABCD-EFGH" || l.VerificationURI != edge.URL+"/device" {
		t.Fatalf("login = %+v", l)
	}
	s, err := c.Wait(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Account.Login != "octo" || s.Device.ID != "dev-1" {
		t.Fatalf("session = %+v", s)
	}
	if len(polls) != 3 {
		t.Fatalf("polled %d times, want 3", len(polls))
	}
	// slow_down adds five intervals to the ten the edge asked for.
	if gap := polls[2].Sub(polls[1]); gap < 15*pollUnit {
		t.Fatalf("poll after slow_down came after %s, want at least %s", gap, 15*pollUnit)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", s, l), token) {
		t.Fatal("the session or login value prints the device token")
	}
	for _, name := range []string{TokensFile, deviceKeyFile} {
		info, statErr := os.Stat(filepath.Join(c.dir, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
	got, err := c.session()
	if err != nil || got.Token != token {
		t.Fatalf("stored token round trip failed: %v", err)
	}
}

func TestDeviceFlowFinalStates(t *testing.T) {
	for _, tc := range []struct {
		state, want string
	}{
		{edgeproto.DeviceDenied, "was refused at"},
		{edgeproto.DeviceExpired, "expired before it was confirmed"},
		{"server exploded", "server exploded"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			edge := newFakeEdge(t)
			edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
					DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", Interval: 1,
				})
			})
			edge.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: tc.state})
			})
			c := newTestClient(t, edge)
			l, err := c.StartLogin(context.Background(), "laptop")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Wait(context.Background(), l)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Wait error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(c.dir, TokensFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a refused sign-in wrote %s: %v", TokensFile, statErr)
			}
		})
	}
}

func TestDeviceFlowRefusesForeignVerificationAddress(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: "https://phish.example/device", Interval: 1,
		})
	})
	_, err := newTestClient(t, edge).StartLogin(context.Background(), "laptop")
	if err == nil || !strings.Contains(err.Error(), "phish.example") {
		t.Fatalf("StartLogin error = %v, want the foreign address refused", err)
	}
}

func TestDeviceFlowPollsThroughTransientFailures(t *testing.T) {
	edge := newFakeEdge(t)
	var (
		mu    sync.Mutex
		polls int
		key   string
	)
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		key = req.Key
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", Interval: 1,
		})
	})
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		switch n {
		case 1:
			// A proxy in front of a restarting edge: no version header.
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case 2:
			writeJSON(w, http.StatusServiceUnavailable, edgeproto.ErrorBody{Error: "restarting"})
		case 3:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		default:
			writeJSON(w, http.StatusOK, edgeproto.DeviceTokenResponse{
				Token:   edgeproto.NewToken(),
				Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
				Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
			})
		}
	})
	c := newTestClient(t, edge)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Wait(context.Background(), l)
	if err != nil {
		t.Fatalf("Wait = %v, want the sign-in collected after the edge came back", err)
	}
	if s.Account.Login != "octo" || polls != 4 {
		t.Fatalf("session = %+v after %d polls, want octo after 4", s, polls)
	}
}

func TestDeviceFlowExpiryNamesTheLastFailure(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", ExpiresIn: 1, Interval: 1,
		})
	})
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	})
	c := newTestClient(t, edge)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Wait(context.Background(), l)
	if err == nil || !strings.Contains(err.Error(), "expired before it was confirmed") || !strings.Contains(err.Error(), "upstream down") {
		t.Fatalf("Wait error = %v, want the expiry and the 502 that caused it", err)
	}
}

func TestDeviceFlowRefusesTokenForAnotherKey(t *testing.T) {
	other, err := EnsureDeviceKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", Interval: 1,
		})
	})
	edge.mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.DeviceTokenResponse{
			Token:   edgeproto.NewToken(),
			Device:  edgeproto.Device{ID: "dev-1", Label: "laptop", Key: edgeproto.DeviceKeyLine(other.PublicKey())},
			Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42"},
		})
	})
	c := newTestClient(t, edge)
	l, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Wait(context.Background(), l); err == nil || !strings.Contains(err.Error(), "different device") {
		t.Fatalf("Wait error = %v, want the foreign key refused", err)
	}
}

func TestDeviceKeyIsNeverReplaced(t *testing.T) {
	dir := t.TempDir()
	first, err := EnsureDeviceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureDeviceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.PublicKey().Marshal()) != string(second.PublicKey().Marshal()) {
		t.Fatal("EnsureDeviceKey replaced an existing key")
	}
}

func TestFilesOpenToOtherUsersAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX mode bits")
	}
	c := newTestClient(t, newFakeEdge(t))
	signIn(t, c)
	for name, read := range map[string]func() error{
		deviceKeyFile: func() error { _, err := DeviceSigner(c.dir); return err },
		TokensFile:    func() error { _, err := c.Session(); return err },
	} {
		path := filepath.Join(c.dir, name)
		for _, mode := range []os.FileMode{0o640, 0o604, 0o620} {
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			err := read()
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("mode %04o", mode)) ||
				!strings.Contains(err.Error(), "run: chmod 600 "+path) {
				t.Errorf("%s at mode %04o: %v, want a refusal naming chmod 600 %s", name, mode, err, path)
			}
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := read(); err != nil {
			t.Errorf("%s at mode 0600: %v", name, err)
		}
	}
}

func TestConcurrentSignInsKeepEveryToken(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	keys := make(chan string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			c, err := New(dir, fmt.Sprintf("https://edge%d.example.test", i))
			if err != nil {
				t.Error(err)
				return
			}
			signer, err := EnsureDeviceKey(dir)
			if err != nil {
				t.Error(err)
				return
			}
			key := edgeproto.DeviceKeyLine(signer.PublicKey())
			keys <- key
			if _, err := c.store(&Login{key: key}, edgeproto.DeviceTokenResponse{
				Token:   edgeproto.NewToken(),
				Device:  edgeproto.Device{ID: fmt.Sprint("dev-", i), Label: "laptop", Key: key},
				Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "octo"},
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(keys)
	first := <-keys
	for key := range keys {
		if key != first {
			t.Fatal("concurrent sign-ins created different device keys")
		}
	}
	if origins, err := SignedIn(dir); err != nil || len(origins) != n {
		t.Fatalf("after %d concurrent sign-ins %d tokens are stored (%v)", n, len(origins), err)
	}
}

func TestDialRefusalCarriesEdgeMessageAndHost(t *testing.T) {
	for _, refusal := range []edgeproto.Refusal{
		edgeproto.RefusalTokenRevoked, edgeproto.RefusalNotMember,
		edgeproto.RefusalUnknownServer, edgeproto.RefusalNotConnected, edgeproto.RefusalNotAttached,
	} {
		t.Run(string(refusal), func(t *testing.T) {
			edge := newFakeEdge(t)
			edge.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, refusal.Status(), edgeproto.ErrorBody{Error: string(refusal)})
			})
			c := newTestClient(t, edge)
			signIn(t, c)
			_, err := c.Dial(context.Background(), testServerID)
			if !errors.Is(err, refusal) {
				t.Fatalf("Dial error = %v, want %q", err, refusal)
			}
			msg := err.Error()
			if !strings.Contains(msg, string(refusal)) || !strings.Contains(msg, c.Host()) || !strings.Contains(msg, testServerID) {
				t.Fatalf("Dial error %q lacks the edge's words, the edge host or the server id", msg)
			}
			if refusal.Status() == http.StatusUnauthorized && !strings.Contains(msg, "aether login --edge "+edge.URL) {
				t.Fatalf("Dial error %q lacks the sign-in command", msg)
			}
		})
	}
}

func TestRefusalTextCannotDriveTheTerminal(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, edgeproto.ErrorBody{Error: "nope\x1b]0;owned\x07"})
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	_, _, err := c.Servers(context.Background())
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Fatalf("Servers error = %q, want control characters replaced", err)
	}
}

func TestDialSendsTokenAndReportsRealAddresses(t *testing.T) {
	edge := newFakeEdge(t)
	var gotAuth string
	edge.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
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
	nc, err := c.Dial(context.Background(), testServerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nc.Close() }()
	if gotAuth != "Bearer "+token {
		t.Fatal("the edge did not receive the device token as the bearer token")
	}
	for _, addr := range []net.Addr{nc.LocalAddr(), nc.RemoteAddr()} {
		if _, _, err := net.SplitHostPort(addr.String()); err != nil {
			t.Fatalf("address %q does not parse as host:port: %v", addr, err)
		}
	}
	if nc.RemoteAddr().String() != edge.Listener.Addr().String() {
		t.Fatalf("RemoteAddr = %s, want the edge %s", nc.RemoteAddr(), edge.Listener.Addr())
	}
	if _, err := nc.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(nc, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}

func TestDialOutlivesItsOpeningContext(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		defer func() { _ = nc.Close() }()
		_, _ = io.Copy(nc, nc)
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	nc, err := c.Dial(ctx, testServerID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nc.Close() }()
	if _, err := nc.Write([]byte("ok")); err != nil {
		t.Fatalf("write after the opening context ended: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(nc, buf); err != nil {
		t.Fatalf("read after the opening context ended: %v", err)
	}
}

func TestRedirectDoesNotCarryTheToken(t *testing.T) {
	var leaked bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
	}))
	t.Cleanup(elsewhere.Close)
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	if _, _, err := c.Servers(context.Background()); err == nil {
		t.Fatal("Servers followed a redirect")
	}
	if _, err := c.Dial(context.Background(), testServerID); err == nil {
		t.Fatal("Dial followed a redirect")
	}
	if leaked {
		t.Fatal("a redirect reached another host")
	}
}

func TestNotSignedIn(t *testing.T) {
	edge := newFakeEdge(t)
	c := newTestClient(t, edge)
	if _, err := c.Dial(context.Background(), testServerID); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Dial error = %v, want ErrNotSignedIn", err)
	}
	if _, _, err := c.Servers(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Servers error = %v, want ErrNotSignedIn", err)
	}
}

func TestLogoutRevokesThenForgets(t *testing.T) {
	edge := newFakeEdge(t)
	var revoked string
	edge.mux.HandleFunc("POST "+edgeproto.PathLogout, func(w http.ResponseWriter, r *http.Request) {
		revoked = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})
	c := newTestClient(t, edge)
	token := signIn(t, c)
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revoked != "Bearer "+token {
		t.Fatal("logout did not present the token for revocation")
	}
	if _, err := c.session(); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Session after logout = %v, want ErrNotSignedIn", err)
	}
	raw, err := os.ReadFile(filepath.Join(c.dir, TokensFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("the token file still holds the revoked token")
	}
}

func TestLogoutKeepsTokenWhenEdgeIsUnreachable(t *testing.T) {
	edge := newFakeEdge(t)
	c := newTestClient(t, edge)
	token := signIn(t, c)
	edge.Close()
	err := c.Logout(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still valid") {
		t.Fatalf("Logout error = %v, want it to say the token is still valid", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("the logout error prints the token")
	}
	if _, err := c.session(); err != nil {
		t.Fatalf("Session after a failed logout = %v, want the token kept", err)
	}
}

func TestClaimRefusesServerTheCodeDoesNotName(t *testing.T) {
	edge := newFakeEdge(t)
	// A host key ground to share all but the last character of the id.
	edge.mux.HandleFunc("POST "+edgeproto.PathClaim, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, edgeproto.ClaimResponse{ServerID: testServerID[:edgeproto.ServerIDLength-1] + "a", Name: "prod"})
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	code, err := edgeproto.NewClaimCode(testServerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Claim(context.Background(), code); err == nil {
		t.Fatal("Claim accepted a server id the code does not name")
	}
}

func TestClaimSendsNormalizedCode(t *testing.T) {
	edge := newFakeEdge(t)
	var got string
	edge.mux.HandleFunc("POST "+edgeproto.PathClaim, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.ClaimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Code
		writeJSON(w, http.StatusOK, edgeproto.ClaimResponse{ServerID: testServerID, Name: "prod"})
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	code, err := edgeproto.NewClaimCode(testServerID)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Claim(context.Background(), "  "+strings.ToUpper(code)+" ")
	if err != nil {
		t.Fatal(err)
	}
	if got != code || resp.ServerID != testServerID {
		t.Fatalf("claim sent %q and got %+v", got, resp)
	}
}

func TestServersRefusesMalformedEntries(t *testing.T) {
	for what, resp := range map[string]edgeproto.ServersResponse{
		"a name with control characters": {Servers: []edgeproto.ServerInfo{
			{ID: testServerID, Name: "prod\x1b[2J", Role: "admin"},
		}},
		// aether servers prints https://<id>.<domain>/ for each server.
		"a server domain that is not a DNS name": {ServerDomain: "example.test/@attacker.test", Servers: []edgeproto.ServerInfo{
			{ID: testServerID, Name: "prod", Role: "admin"},
		}},
	} {
		edge := newFakeEdge(t)
		edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, resp)
		})
		c := newTestClient(t, edge)
		signIn(t, c)
		if _, _, err := c.Servers(context.Background()); err == nil {
			t.Errorf("Servers accepted %s", what)
		}
	}
}

func TestOldEdgeIsRefused(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edgeproto.HeaderVersion, "0")
		_, _ = w.Write([]byte(`{"servers":[]}`))
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	if _, _, err := c.Servers(context.Background()); err == nil || !strings.Contains(err.Error(), "upgrade required") {
		t.Fatalf("Servers error = %v, want upgrade required", err)
	}
}
