package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// relay is a fake edge that splices every connect and claim connection
// for the world's device token into serve, and records what reached it:
// each request line and header, every byte the client sent over the
// splice, and every SSH user name an authentication callback saw.
type relay struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	wire     bytes.Buffer
	users    []string
}

func (w *edgeWorld) relay(t *testing.T, conf func(*relay) *ssh.ServerConfig) *relay {
	t.Helper()
	r := &relay{}
	splice := func(rw http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.Method+" "+req.URL.String()+" "+strings.Join(headerLines(req.Header), " "))
		r.mu.Unlock()
		if req.Header.Get("Authorization") != "Bearer "+w.token {
			http.Error(rw, "no token", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(rw, req, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		sc, chans, reqs, err := ssh.NewServerConn(&recordingConn{Conn: nc, r: r}, conf(r))
		if err != nil {
			_ = nc.Close()
			return
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			for nch := range chans {
				_ = nch.Reject(ssh.Prohibited, "test server")
			}
		}()
		_ = sc.Wait()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+edgeproto.PathConnect, splice)
	mux.HandleFunc("GET "+edgeproto.PathClaimConnect, splice)
	r.Server = httptest.NewServer(mux)
	t.Cleanup(r.Close)
	return r
}

func headerLines(h http.Header) []string {
	var out []string
	for k, vs := range h {
		out = append(out, k+": "+strings.Join(vs, ","))
	}
	return out
}

// recordingConn copies what the client sends into its relay's record.
type recordingConn struct {
	net.Conn
	r *relay
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.r.mu.Lock()
	c.r.wire.Write(p[:n])
	c.r.mu.Unlock()
	return n, err
}

// serverConfig admits the world's device key under any user name, and
// records every user name an authentication attempt carried.
func (w *edgeWorld) serverConfig(host ssh.Signer) func(*relay) *ssh.ServerConfig {
	return func(r *relay) *ssh.ServerConfig {
		conf := &ssh.ServerConfig{
			PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				r.mu.Lock()
				r.users = append(r.users, meta.User())
				r.mu.Unlock()
				if bytes.Equal(key.Marshal(), w.device.Marshal()) {
					return nil, nil
				}
				return nil, errors.New("unknown key")
			},
		}
		conf.AddHostKey(host)
		return conf
	}
}

func claimCode(t *testing.T, serverID string) (code, secret string) {
	t.Helper()
	code, err := edgeproto.NewClaimCode(serverID)
	if err != nil {
		t.Fatal(err)
	}
	return code, strings.TrimPrefix(code, serverID+"-")
}

func TestClaimOffersTheCodeOnlyToTheServerItNames(t *testing.T) {
	w := newEdgeWorld(t)
	impostor := mustSigner(t)
	r := w.relay(t, w.serverConfig(impostor))
	w.signIn(t, r.URL)
	code, secret := claimCode(t, w.serverID())

	_, err := DialClaim(Config{EdgeURL: r.URL}, code)
	if err == nil {
		t.Fatal("DialClaim accepted a server whose host key does not derive the id in the code")
	}
	for _, want := range []string{ssh.FingerprintSHA256(impostor.PublicKey()), edgeproto.ServerID(impostor.PublicKey()), w.serverID()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q shows the claim secret", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.users) != 0 {
		t.Fatalf("the impostor saw authentication as %q", r.users)
	}
	if bytes.Contains(r.wire.Bytes(), []byte(secret)) {
		t.Fatal("the claim secret crossed the relay")
	}
	for _, req := range r.requests {
		if strings.Contains(req, secret) {
			t.Fatalf("the edge request %q carries the claim secret", req)
		}
		if !strings.Contains(req, edgeproto.ClaimConnectPath(w.serverID())) {
			t.Fatalf("the claim went to %q, not the claim path", req)
		}
	}
}

func TestClaimPresentsTheCodeAsTheSSHUser(t *testing.T) {
	w := newEdgeWorld(t)
	r := w.relay(t, w.serverConfig(w.host))
	w.signIn(t, r.URL)
	code, secret := claimCode(t, w.serverID())

	conn, err := DialClaim(Config{EdgeURL: r.URL, Addr: closedAddr(t)}, " "+strings.ToUpper(code)+"\n")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if conn.cfg.ServerID != w.serverID() {
		t.Fatalf("claim connection config names server %q, want %q", conn.cfg.ServerID, w.serverID())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if want := "claim:" + code + ":github:1001"; len(r.users) == 0 || r.users[0] != want {
		t.Fatalf("the server saw users %q, want %q", r.users, want)
	}
	// Encrypted by then: the code is in the user name and nowhere else.
	if bytes.Contains(r.wire.Bytes(), []byte(secret)) {
		t.Fatal("the claim secret crossed the relay in the clear")
	}
}

func TestHostKeyFingerprintStopsBeforeAuthentication(t *testing.T) {
	w := newEdgeWorld(t)
	r := w.relay(t, w.serverConfig(w.host))
	w.signIn(t, r.URL)
	got, err := HostKeyFingerprint(context.Background(), r.URL, w.serverID())
	if err != nil {
		t.Fatal(err)
	}
	if want := ssh.FingerprintSHA256(w.host.PublicKey()); got != want {
		t.Fatalf("fingerprint = %s, want %s", got, want)
	}
	other := edgeproto.ServerID(mustSigner(t).PublicKey())
	if _, err := HostKeyFingerprint(context.Background(), r.URL, other); err == nil || !strings.Contains(err.Error(), "not the listed server "+other) {
		t.Fatalf("HostKeyFingerprint for another id = %v, want the key refused", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.users) != 0 {
		t.Fatalf("reading the host key authenticated as %q", r.users)
	}
}

// A device the server does not admit yet fails with the server's own
// words, commands included, line by line and with control characters
// replaced. Git and the sync daemon dial through DialLinked too.
func TestEdgeRefusalCarriesTheServersMessage(t *testing.T) {
	w := newEdgeWorld(t)
	banner := "device \"laptop\" is waiting for approval. From a device this account already uses, or as an admin, run:\n" +
		"  aether device approve ABCD-EFGH\nor on the server:\n  sudo aether-server device approve ABCD-EFGH\x1b[2J\n"
	r := w.relay(t, func(*relay) *ssh.ServerConfig {
		conf := &ssh.ServerConfig{
			PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, &ssh.BannerError{Err: errors.New("pending"), Message: banner}
			},
		}
		conf.AddHostKey(w.host)
		return conf
	})
	w.signIn(t, r.URL)
	_, err := DialLinked(context.Background(), Config{EdgeURL: r.URL, ServerID: w.serverID()}, "aether")
	if err == nil {
		t.Fatal("DialLinked succeeded for a pending device")
	}
	msg := err.Error()
	for _, want := range []string{
		"server said:",
		"\n      aether device approve ABCD-EFGH\n",
		"\n      sudo aether-server device approve ABCD-EFGH?[2J",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.ContainsRune(msg, '\x1b') {
		t.Errorf("error %q passes an escape character through", msg)
	}
}
