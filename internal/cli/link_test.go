package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestNormalizeAddr(t *testing.T) {
	for input, want := range map[string]string{
		"host":       "host:2222",
		"host:2200":  "host:2200",
		"127.0.0.1":  "127.0.0.1:2222",
		"[::1]":      "[::1]:2222",
		"[::1]:2200": "[::1]:2200",
		"::1":        "[::1]:2222",
	} {
		if got := normalizeAddr(input); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSavedKeyCarriesForward(t *testing.T) {
	prev := Config{
		Addr: "old:2222",
		Key:  "/keys/default",
		Links: []NamedLink{
			{Name: "prod", Addr: "prod:2222", Key: "/keys/prod"},
			{Name: "staging", Addr: "staging:2222"},
		},
	}
	cases := []struct {
		name string
		want string
	}{
		{name: "", want: "/keys/default"},
		{name: "prod", want: "/keys/prod"},
		{name: "staging", want: "/keys/default"},
		{name: "new", want: "/keys/default"},
	}
	for _, tc := range cases {
		if got := savedKey(prev, tc.name); got != tc.want {
			t.Errorf("savedKey(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := savedKey(Config{}, "prod"); got != "" {
		t.Errorf("savedKey on first link = %q, want empty", got)
	}
}

func TestLinkConfig(t *testing.T) {
	prev := Config{Addr: "old:2222", Links: []NamedLink{{Name: "prod", Addr: "old:2222"}}}
	fresh := Config{Addr: "new:2222", User: "aether", Repo: "/src/repo"}
	got := linkConfig(fresh, prev, "")
	if got.Addr != "new:2222" || len(got.Links) != 1 || got.Links[0].Addr != "old:2222" {
		t.Fatalf("no-name linkConfig = %+v", got)
	}
	got = linkConfig(fresh, prev, "staging")
	want := NamedLink{Name: "staging", Addr: "new:2222", User: "aether", Repo: "/src/repo", AutoKey: true}
	if len(got.Links) != 2 || got.Links[1] != want {
		t.Fatalf("snapshot = %+v, want %+v", got.Links, want)
	}
	got = linkConfig(fresh, prev, "prod")
	if len(got.Links) != 1 || got.Links[0].Addr != "new:2222" {
		t.Fatalf("re-link prod links = %+v", got.Links)
	}
}

// A repo's edge remote names only its server id, so a link that replaces
// an edge default must keep that server reachable by id.
func TestLinkConfigKeepsReplacedEdgeDefault(t *testing.T) {
	a := Config{Addr: "a:2222", User: "aether", Repo: "/src/a", EdgeURL: "https://edge.example", ServerID: "aaaa"}
	for _, next := range []struct {
		cfg  Config
		name string
	}{
		{Config{User: "aether", EdgeURL: "https://edge.example", ServerID: "bbbb"}, "b"},
		{Config{User: "aether", EdgeURL: "https://edge.example", ServerID: "bbbb"}, ""},
		{Config{Addr: "host:2222", User: "aether"}, ""},
	} {
		got := linkConfig(next.cfg, a, next.name)
		kept, ok := got.ByServerID("aaaa")
		if !ok || kept.EdgeURL != a.EdgeURL || kept.Addr != a.Addr || kept.Repo != a.Repo {
			t.Fatalf("after linking %+v as %q, server aaaa = %+v, %v", next.cfg, next.name, kept, ok)
		}
		if again := linkConfig(next.cfg, got, next.name); len(again.Links) != len(got.Links) {
			t.Fatalf("linking %+v again grew links from %+v to %+v", next.cfg, got.Links, again.Links)
		}
	}
	if got := linkConfig(Config{User: "aether", EdgeURL: "https://other.example", ServerID: "aaaa"}, a, ""); len(got.Links) != 0 {
		t.Fatalf("relinking the same server kept its old link: %+v", got.Links)
	}
}

func TestLinkKeyPersistsAndRelinks(t *testing.T) {
	home := testhome.Isolate(t)
	key := testhome.Ed25519Key(t)
	private := filepath.Join(home, "work_key")
	signer := testhome.WriteSSHKey(t, private, key, "")
	public := private + ".pub"
	if err := os.WriteFile(public, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	got, err := linkKey("work_key", Config{}, "")
	if err != nil {
		t.Fatalf("linkKey(work_key): %v", err)
	}
	if got != private {
		t.Fatalf("linkKey = %q, want %q", got, private)
	}
	cfg := linkConfig(Config{Addr: "h:2222", User: "aether", Key: private}, Config{}, "prod")
	if cfg.Key != private || len(cfg.Links) != 1 || cfg.Links[0].Key != private || cfg.Links[0].AutoKey {
		t.Fatalf("saved config = %+v", cfg)
	}
	if got, _ = linkKey("", cfg, "prod"); got != private {
		t.Errorf("prod relink key = %q, want %q", got, private)
	}
	if got, err = linkKey("auto", cfg, "prod"); err != nil || got != "" {
		t.Errorf("linkKey(auto) = %q, %v", got, err)
	}
	cleared := linkConfig(Config{Addr: "h:2222", User: "aether"}, cfg, "prod")
	if cleared.Key != "" || len(cleared.Links) != 1 || cleared.Links[0].Key != "" || !cleared.Links[0].AutoKey {
		t.Fatalf("config after auto = %+v", cleared)
	}
	if _, err := linkKey(public, Config{}, ""); err == nil || !strings.Contains(err.Error(), "is a public key") {
		t.Errorf("linkKey(public) = %v, want public-key rejection", err)
	}
	if _, err := linkKey(filepath.Join(home, "missing"), Config{}, ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("linkKey(missing) = %v, want not found", err)
	}
}

func TestClaimOfTheCurrentServerKeepsItsLink(t *testing.T) {
	host := mustSigner(t)
	id := edgeproto.ServerID(host.PublicKey())
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(knownhosts.Line([]string{"srv:2222", "10.0.0.1:2222"}, host.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := Config{Addr: "srv:2222", User: "aether", Repo: "/src/repo", KnownHosts: knownHosts}
	if other, err := prev.pins(edgeproto.ServerID(mustSigner(t).PublicKey())); err != nil || other {
		t.Fatalf("pins(another server) = %v, %v", other, err)
	}
	same, err := prev.pins(id)
	if err != nil || !same {
		t.Fatalf("pins(%s) = %v, %v", id, same, err)
	}
	line := knownhosts.Line([]string{knownhosts.HashHostname("[srv]:2222")}, host.PublicKey())
	for body, want := range map[string]bool{
		line + "\n":                        true,
		line + "\n@revoked " + line + "\n": false,
		knownhosts.Line([]string{knownhosts.HashHostname("[other]:2222")}, host.PublicKey()) + "\n": false,
	} {
		if err := os.WriteFile(knownHosts, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if same, err := prev.pins(id); err != nil || same != want {
			t.Fatalf("pins with known_hosts %q = %v, %v; want %v", body, same, err, want)
		}
	}
	prev.ServerID = id
	got := linkConfig(Config{User: "aether", EdgeURL: "https://edge.example", ServerID: id}, prev, "")
	if got.Repo != "/src/repo" || got.Addr != "srv:2222" || got.KnownHosts != knownHosts || got.EdgeURL != "https://edge.example" || got.ServerID != id {
		t.Fatalf("claim of the current server saved %+v", got)
	}
}

func TestClaimFailsOnUnreadableKnownHostsBeforeTheCodeIsSpent(t *testing.T) {
	w := newEdgeWorld(t)
	r := w.relay(t, w.serverConfig(w.host))
	w.signIn(t, r.URL)
	code, _ := claimCode(t, w.serverID())
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("srv:2222 ssh-ed25519 not-base64!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := Config{Addr: "srv:2222", User: "aether", KnownHosts: knownHosts}

	res, err := Link(context.Background(), LinkOptions{Claim: code, EdgeURL: r.URL}, prev)
	if err == nil {
		_ = res.Conn.Close()
		t.Fatal("Link claimed past a known_hosts it could not read")
	}
	if !strings.Contains(err.Error(), "known_hosts "+knownHosts) {
		t.Fatalf("Link = %v, want the known_hosts error", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) != 0 || len(r.users) != 0 {
		t.Fatalf("the claim dialed before the check: requests %q, users %q", r.requests, r.users)
	}
}

func TestClaimOfTheCurrentServerMovesItsGitRemote(t *testing.T) {
	id := edgeproto.ServerID(mustSigner(t).PublicKey())
	prev := Config{Addr: "srv:2222", User: "aether", Repo: "/src/repo"}
	claimed := prev
	claimed.ServerID = id
	got := linkConfig(Config{User: "aether", EdgeURL: "https://edge.example", ServerID: id}, claimed, "")
	remote := GitURL(prev.User, prev.GitHost(), "ws_1")
	want := "ssh://aether@" + EdgeHost(id) + "/ws_1.git"
	if moved, ok := got.MovedGitURL(prev, remote); !ok || moved != want {
		t.Fatalf("MovedGitURL(%s) = %q, %v; want %q", remote, moved, ok, want)
	}
	if moved, ok := got.MovedGitURL(prev, "ssh://aether@elsewhere:2222/ws_1.git"); ok {
		t.Fatalf("a remote of another host moved to %q", moved)
	}
	if moved, ok := got.MovedGitURL(got, want); ok {
		t.Fatalf("a remote already on the edge host moved to %q", moved)
	}
}

// A server that completes the handshake and never answers server.info,
// as one whose network went away just then does, must not hold the link
// past its context.
func TestLinkEndsWhenServerInfoIsNeverAnswered(t *testing.T) {
	testhome.Isolate(t)
	disableAgent(t)
	signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		raw, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		t.Cleanup(func() { _ = raw.Close() })
		cfg := &ssh.ServerConfig{NoClientAuth: true}
		cfg.AddHostKey(signer)
		_, channels, requests, handshakeErr := ssh.NewServerConn(raw, cfg)
		if handshakeErr != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, acceptErr := incoming.Accept()
			if acceptErr != nil {
				return
			}
			t.Cleanup(func() { _ = channel.Close() })
			go func() {
				for request := range requests {
					_ = request.Reply(true, nil)
				}
			}()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Link(ctx, LinkOptions{Addr: listener.Addr().String()}, Config{})
	if err == nil || !strings.Contains(err.Error(), "server.info: context deadline exceeded") {
		t.Fatalf("Link = %v, want server.info ended by the context", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Link took %v", elapsed)
	}
}
