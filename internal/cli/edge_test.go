package cli

import (
	"bytes"
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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// edgeWorld is a signed-in client machine and a server it reaches: host is
// the server's host key, device the client's device key.
type edgeWorld struct {
	host   ssh.Signer
	device ssh.PublicKey
	token  string
}

// newEdgeWorld isolates the config directory and creates the device key.
func newEdgeWorld(t *testing.T) *edgeWorld {
	t.Helper()
	testhome.Isolate(t)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := edgeclient.EnsureDeviceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	return &edgeWorld{host: host, device: signer.PublicKey(), token: edgeproto.NewToken()}
}

func (w *edgeWorld) serverID() string { return edgeproto.ServerID(w.host.PublicKey()) }

// serve runs the server's side of one SSH connection: it admits only the
// device key and answers an exec with "ran: <command>", then echoes stdin
// and exits with status 3.
func (w *edgeWorld) serve(nc net.Conn) {
	conf := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), w.device.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	conf.AddHostKey(w.host)
	sc, chans, reqs, err := ssh.NewServerConn(nc, conf)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range creqs {
				var p struct{ Command string }
				if req.Type != "exec" || ssh.Unmarshal(req.Payload, &p) != nil {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				_, _ = fmt.Fprintf(ch, "ran: %s\n", p.Command)
				_, _ = io.Copy(ch, ch)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{3}))
				_ = ch.Close()
			}
		}()
	}
}

// listen serves SSH on a loopback address with the world's host key.
func (w *edgeWorld) listen(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go w.serve(nc)
		}
	}()
	return l.Addr().String()
}

// fakeEdge relays /v1/connect/<id> for a signed-in device straight into
// the world's SSH server, or refuses with refusal when it is set.
type fakeEdge struct {
	*httptest.Server
	hits atomic.Int32
}

func (w *edgeWorld) edge(t *testing.T, refusal edgeproto.Refusal) *fakeEdge {
	t.Helper()
	e := &fakeEdge{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+edgeproto.PathConnect, func(rw http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		refused := refusal
		if r.Header.Get("Authorization") != "Bearer "+w.token {
			refused = edgeproto.RefusalTokenRequired
		}
		if refused != "" {
			rw.WriteHeader(refused.Status())
			_ = json.NewEncoder(rw).Encode(edgeproto.ErrorBody{Error: string(refused)})
			return
		}
		ws, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		w.serve(websocket.NetConn(context.Background(), ws, websocket.MessageBinary))
	})
	e.Server = httptest.NewServer(mux)
	t.Cleanup(e.Close)
	return e
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestDialLinkedPinsHostKeyOnDirectPath(t *testing.T) {
	w := newEdgeWorld(t)
	addr := w.listen(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	conn, err := Dial(Config{Addr: addr, ServerID: w.serverID(), KnownHosts: knownHosts})
	if err != nil {
		t.Fatalf("Dial with the right server id: %v", err)
	}
	_ = conn.Close()
	if _, statErr := os.Stat(knownHosts); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an edge link touched known_hosts: %v", statErr)
	}

	other := edgeproto.ServerID(mustSigner(t).PublicKey())
	_, err = Dial(Config{Addr: addr, ServerID: other, KnownHosts: knownHosts})
	if err == nil || !strings.Contains(err.Error(), "not the linked server "+other) {
		t.Fatalf("Dial with another server id = %v, want the host key refused", err)
	}
}

func TestDialLinkedPinsHostKeyOnEdgePath(t *testing.T) {
	w := newEdgeWorld(t)
	edge := w.edge(t, "")
	w.signIn(t, edge.URL)
	conn, err := Dial(Config{EdgeURL: edge.URL, ServerID: w.serverID()})
	if err != nil {
		t.Fatalf("Dial through the edge: %v", err)
	}
	_ = conn.Close()

	other := edgeproto.ServerID(mustSigner(t).PublicKey())
	_, err = Dial(Config{EdgeURL: edge.URL, ServerID: other})
	if err == nil || !strings.Contains(err.Error(), "not the linked server "+other) {
		t.Fatalf("Dial through the edge with another server id = %v, want the host key refused", err)
	}
}

func TestDialLinkedTriesAddressBeforeEdge(t *testing.T) {
	w := newEdgeWorld(t)
	edge := w.edge(t, "")
	w.signIn(t, edge.URL)

	conn, err := Dial(Config{Addr: w.listen(t), EdgeURL: edge.URL, ServerID: w.serverID()})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if n := edge.hits.Load(); n != 0 {
		t.Fatalf("the edge was dialed %d times while the address answered", n)
	}

	conn, err = Dial(Config{Addr: closedAddr(t), EdgeURL: edge.URL, ServerID: w.serverID()})
	if err != nil {
		t.Fatalf("Dial with a dead address: %v", err)
	}
	_ = conn.Close()
	if n := edge.hits.Load(); n != 1 {
		t.Fatalf("the edge was dialed %d times after the address failed, want 1", n)
	}
}

func TestDialLinkedReportsBothCauses(t *testing.T) {
	w := newEdgeWorld(t)
	edge := w.edge(t, edgeproto.RefusalNotConnected)
	w.signIn(t, edge.URL)
	dead := closedAddr(t)
	_, err := Dial(Config{Addr: dead, EdgeURL: edge.URL, ServerID: w.serverID()})
	if err == nil {
		t.Fatal("Dial succeeded with both paths down")
	}
	msg := err.Error()
	for _, want := range []string{"dial " + dead, string(edgeproto.RefusalNotConnected), strings.TrimPrefix(edge.URL, "http://")} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q lacks %q", msg, want)
		}
	}
}

func TestDialLinkedRefusesWrongKeyOnBothPaths(t *testing.T) {
	w := newEdgeWorld(t)
	edge := w.edge(t, "")
	w.signIn(t, edge.URL)
	other := edgeproto.ServerID(mustSigner(t).PublicKey())
	_, err := Dial(Config{Addr: w.listen(t), EdgeURL: edge.URL, ServerID: other})
	if err == nil {
		t.Fatal("Dial accepted a host key that is not the linked server")
	}
	if n := strings.Count(err.Error(), "not the linked server "+other); n != 2 {
		t.Fatalf("error %q names the refused key %d times, want once per path", err, n)
	}
}

func TestRunRemoteWiresStdioAndExitStatus(t *testing.T) {
	w := newEdgeWorld(t)
	cfg := Config{Addr: w.listen(t), ServerID: w.serverID()}
	var stdout, stderr bytes.Buffer
	code, err := RunRemote(context.Background(), cfg, "aether", "git-upload-pack '/ws.git'",
		strings.NewReader("have abc\n"), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Fatalf("exit status = %d, want 3", code)
	}
	if want := "ran: git-upload-pack '/ws.git'\nhave abc\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestParseSSHArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want SSHArgs
	}{
		{[]string{"aether@x.edge.aether.invalid", "git-upload-pack '/w.git'"},
			SSHArgs{User: "aether", Host: "x.edge.aether.invalid", Command: "git-upload-pack '/w.git'"}},
		{[]string{"-o", "SendEnv=GIT_PROTOCOL", "-p", "2222", "git@github.com", "git-receive-pack", "'o/r.git'"},
			SSHArgs{Options: []string{"-o", "SendEnv=GIT_PROTOCOL", "-p", "2222"}, User: "git", Host: "github.com", Command: "git-receive-pack 'o/r.git'"}},
		{[]string{"-4Tp22", "host"}, SSHArgs{Options: []string{"-4Tp22"}, Host: "host"}},
		{[]string{"-G", "host"}, SSHArgs{Options: []string{"-G"}, Host: "host"}},
		{[]string{"--", "-host"}, SSHArgs{Host: "-host"}},
	} {
		got, err := ParseSSHArgs(tc.args)
		if err != nil {
			t.Fatalf("ParseSSHArgs(%q): %v", tc.args, err)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("ParseSSHArgs(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
	for _, bad := range [][]string{{"-p"}, {"-Z", "host"}, {"-v"}} {
		if _, err := ParseSSHArgs(bad); err == nil {
			t.Errorf("ParseSSHArgs(%q) succeeded", bad)
		}
	}
}

func TestGitSSHEnvOnlyForEdgeHosts(t *testing.T) {
	id := edgeproto.ServerID(mustSigner(t).PublicKey())
	env, err := GitSSHEnv(GitURL("aether", EdgeHost(id), "ws1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || !strings.HasPrefix(env[0], "GIT_SSH_COMMAND=") || !strings.HasSuffix(env[0], " edge-ssh") {
		t.Fatalf("GitSSHEnv for an edge host = %q", env)
	}
	for _, u := range []string{GitURL("aether", "host:2222", "ws1"), "git@github.com:o/r.git", "https://example.com/r.git"} {
		if env, err := GitSSHEnv(u); err != nil || env != nil {
			t.Fatalf("GitSSHEnv(%q) = %q, %v; want nothing", u, env, err)
		}
	}
}

func TestNamedEdgeLinkInheritsNoAddressOrServer(t *testing.T) {
	id := edgeproto.ServerID(mustSigner(t).PublicKey())
	cfg := Config{
		Addr: "default:2222", EdgeURL: "https://edge.example", ServerID: "aaaaaaaaaaaaaaaaaaaaaaaaaa",
		Links: []NamedLink{
			{Name: "edge", EdgeURL: "https://edge.example", ServerID: id},
			{Name: "direct", Addr: "direct:2222"},
		},
	}
	edge, _ := cfg.Named("edge")
	if edge.Addr != "" || edge.ServerID != id || edge.GitHost() != EdgeHost(id) {
		t.Fatalf("edge profile = addr %q server %q", edge.Addr, edge.ServerID)
	}
	direct, _ := cfg.Named("direct")
	if direct.ServerID != "" || direct.EdgeURL != "" || direct.GitHost() != "direct:2222" {
		t.Fatalf("direct profile inherited the default link's edge: %+v", direct)
	}
	if got, ok := cfg.ByServerID(id); !ok || got.Active != "edge" {
		t.Fatalf("ByServerID = %+v, %v", got, ok)
	}
}

func TestLoadAcceptsEdgeOnlyLink(t *testing.T) {
	useTempConfigDir(t)
	id := edgeproto.ServerID(mustSigner(t).PublicKey())
	if err := Save(Config{EdgeURL: "https://edge.example", ServerID: id}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.ServerID != id {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
}

func mustSigner(t *testing.T) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// signIn stores the device token for edgeURL, as aether login does.
func (w *edgeWorld) signIn(t *testing.T, edgeURL string) {
	t.Helper()
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"edges": map[string]any{edgeURL: map[string]any{"token": w.token}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edgeclient.TokensFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
