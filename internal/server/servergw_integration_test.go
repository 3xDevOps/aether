//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/servergw"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// TestIntegrationServerGateway drives the server-hosted dashboard gateway
// the way a phone on the tailnet does: identified by WhoIs alone, it reads
// the capabilities, calls a method, launches a run, follows the event
// stream and types into the run's PTY over the WebSocket - all served
// in-process by the same handlers the SSH transport uses. Then a tagged
// node and a failing resolver are refused, and the HTTPS listener is
// started against a stand-in tailscaled that issues the certificate.
func TestIntegrationServerGateway(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, _, verifyNoLeaks := pickRuntime(t)
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

	gw, err := servergw.New(servergw.Config{SSH: srv.ssh})
	if err != nil {
		t.Fatalf("servergw.New: %v", err)
	}
	defer func() { _ = gw.Close() }()
	web := httptest.NewServer(gw)
	defer web.Close()

	// First contact over HTTP registers the admin, exactly as over SSH.
	var caps protocol.GatewayCapabilities
	if status := getJSON(t, web.URL+"/api/v1/capabilities", &caps); status != http.StatusOK {
		t.Fatalf("capabilities status = %d", status)
	}
	if caps.Gateway != "server" || strings.Join(caps.Methods, ",") != "*" ||
		strings.Join(caps.WS, ",") != "events,attach,terminal" || caps.Local != nil {
		t.Fatalf("capabilities = %+v", caps)
	}
	var info protocol.ServerInfoResult
	if status := postJSON(t, web.URL+"/api/v1/server.info", "", &info); status != http.StatusOK {
		t.Fatalf("server.info status = %d", status)
	}
	if info.Member.Role != string(domain.RoleAdmin) || info.Member.Pending || !info.TailnetIdentityAuth {
		t.Fatalf("server.info = %+v, want the first tailnet contact as an approved admin", info)
	}

	// Seed the workspace over the SSH git transport; the "none" auth
	// resolves the push to the same member.
	ws := &domain.Workspace{Name: "phone", Environment: domain.WorkspaceEnvironment{}, BaseBranch: domain.DefaultBaseBranch}
	if err := srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "README.md"), "# phone seed\n")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	// Follow the workspace's events before launching so nothing is missed.
	events := dialWS(t, ctx, web.URL, "/ws/events")
	defer func() { _ = events.CloseNow() }()
	if err := wsjson.Write(ctx, events, protocol.SubscribeRequest{WorkspaceID: string(ws.ID)}); err != nil {
		t.Fatal(err)
	}
	var subAck protocol.SubscribeResponse
	if err := wsjson.Read(ctx, events, &subAck); err != nil || !subAck.OK {
		t.Fatalf("subscribe ack = %+v (%v)", subAck, err)
	}

	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	var launched protocol.RunResult
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: "phone e2e", Harness: "fake"})
	if status := postJSON(t, web.URL+"/api/v1/run.launch", string(params), &launched); status != http.StatusOK {
		t.Fatalf("run.launch status = %d", status)
	}
	waitWireEvent(t, ctx, events, "run.status running", func(ev protocol.Event) bool {
		var p struct{ To string }
		return ev.Type == "run.status" && ev.RunID == launched.Run.ID &&
			json.Unmarshal(ev.Payload, &p) == nil && p.To == string(domain.RunRunning)
	})

	// Attach with write and talk to the agent through the socket.
	attach := dialWS(t, ctx, web.URL, "/ws/attach/"+launched.Run.ID)
	defer func() { _ = attach.CloseNow() }()
	if err := wsjson.Write(ctx, attach, protocol.DashAttachRequest{Write: true, Cols: 120, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	var attachAck protocol.AttachResponse
	if err := wsjson.Read(ctx, attach, &attachAck); err != nil || !attachAck.OK {
		t.Fatalf("attach ack = %+v (%v)", attachAck, err)
	}
	var output bytes.Buffer
	waitTerminalOutput(t, ctx, attach, &output, "agent-ready")
	if err := wsjson.Write(ctx, attach, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "ping-gw\r"}); err != nil {
		t.Fatal(err)
	}
	waitTerminalOutput(t, ctx, attach, &output, "got:ping-gw")
	// The agent exits after the echo: the socket closes with the
	// session-ended reason and the run completes.
	if code, reason := waitClose(t, ctx, attach); code != websocket.StatusNormalClosure || reason != "session ended" {
		t.Fatalf("attach close = %d %q, want 1000 session ended", code, reason)
	}
	waitWireEvent(t, ctx, events, "run.status completed", func(ev protocol.Event) bool {
		var p struct{ To string }
		return ev.Type == "run.status" && ev.RunID == launched.Run.ID &&
			json.Unmarshal(ev.Payload, &p) == nil && p.To == string(domain.RunCompleted)
	})

	// Identity is resolved per request: a tagged node and a resolver that
	// cannot answer are refused with the documented errors.
	whois.set(sshd.WhoIsIdentity{NodeID: "node-ci", Tagged: true}, nil)
	var refusal webgate.ErrorBody
	if status := getJSON(t, web.URL+"/api/v1/capabilities", &refusal); status != http.StatusForbidden ||
		refusal.Error == nil || refusal.Error.Code != protocol.CodeDenied ||
		!strings.HasPrefix(refusal.Error.Message, "tagged tailnet node") {
		t.Fatalf("tagged node: status %d body %+v", status, refusal.Error)
	}
	whois.set(sshd.WhoIsIdentity{}, errors.New("tailscaled is down"))
	refusal = webgate.ErrorBody{}
	if status := getJSON(t, web.URL+"/api/v1/capabilities", &refusal); status != http.StatusServiceUnavailable ||
		refusal.Error == nil || refusal.Error.Code != protocol.CodeUnavailable ||
		!strings.HasPrefix(refusal.Error.Message, "tailnet identity unavailable: ") ||
		!strings.Contains(refusal.Error.Message, "tailscaled is down") {
		t.Fatalf("resolver failure: status %d body %+v", status, refusal.Error)
	}
	whois.set(sshd.WhoIsIdentity{Login: "ada@example.com", NodeID: "node-ada"}, nil)

	// HTTPS on the tailnet: the certificate comes from tailscaled's
	// LocalAPI for the node's MagicDNS name, and a tailnet without HTTPS
	// certificates refuses to start.
	certPEM, keyPEM := selfSignedPair(t, "aether.test")
	issuing := fakeTailscaled(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/cert/aether.test" || r.URL.Query().Get("type") != "pair" {
			http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)
			return
		}
		_, _ = w.Write(certPEM)
		_, _ = w.Write(keyPEM)
	})
	port := freePort(t)
	loopback := reachability.Node{DNSName: "aether.test", Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	if err := gw.Start(ctx, servergw.Tailnet{Node: loopback, Port: port, Certs: reachability.NewTailscale(issuing)}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	tlsClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "aether.test", MinVersion: tls.VersionTLS12},
	}}
	resp, err := tlsClient.Get(fmt.Sprintf("https://127.0.0.1:%d/api/v1/capabilities", port))
	if err != nil {
		t.Fatalf("https capabilities: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil || resp.TLS.PeerCertificates[0].DNSNames[0] != "aether.test" {
		t.Fatalf("https capabilities = %d over %+v", resp.StatusCode, resp.TLS)
	}

	refusing := fakeTailscaled(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no certificate for this domain: HTTPS certificates are not enabled", http.StatusInternalServerError)
	})
	denied, err := servergw.New(servergw.Config{SSH: srv.ssh})
	if err != nil {
		t.Fatal(err)
	}
	err = denied.Start(ctx, servergw.Tailnet{Node: loopback, Port: freePort(t), Certs: reachability.NewTailscale(refusing)})
	if err == nil || !strings.Contains(err.Error(), "HTTPS certificates are not enabled") ||
		!strings.Contains(err.Error(), "enable MagicDNS and HTTPS certificates") {
		t.Fatalf("Start without a certificate = %v, want the refusal naming what to enable", err)
	}
	_ = denied.Close()

	_ = events.CloseNow()
	_ = gw.Close()
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

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
	return resp.StatusCode
}

func postJSON(t *testing.T, url, body string, v any) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("POST %s: decode %s: %v", url, raw, err)
	}
	return resp.StatusCode
}

func dialWS(t *testing.T, ctx context.Context, base, path string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+path, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	conn.SetReadLimit(1 << 20)
	return conn
}

func waitWireEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, desc string, pred func(protocol.Event) bool) {
	t.Helper()
	for {
		var ev protocol.Event
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			t.Fatalf("event stream ended waiting for %s: %v", desc, err)
		}
		if pred(ev) {
			return
		}
	}
}

func waitTerminalOutput(t *testing.T, ctx context.Context, conn *websocket.Conn, output *bytes.Buffer, substr string) {
	t.Helper()
	for !strings.Contains(output.String(), substr) {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("attach ended before %q: %v (output %q)", substr, err, output.String())
		}
		if typ == websocket.MessageBinary {
			output.Write(data)
		}
	}
}

func waitClose(t *testing.T, ctx context.Context, conn *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("read ended without a close frame: %v", err)
		}
		return ce.Code, ce.Reason
	}
}

// fakeTailscaled serves handler on a unix socket the way tailscaled's
// LocalAPI does, returning the socket path.
func fakeTailscaled(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func selfSignedPair(t *testing.T, name string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
