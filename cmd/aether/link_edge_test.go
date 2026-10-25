package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// listingEdge is a signed-in edge that lists one server, named prod, and
// relays connections to it into an SSH server holding host.
func listingEdge(t *testing.T) (*edgeclient.Client, ssh.Signer) {
	t.Helper()
	testhome.Isolate(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	id := edgeproto.ServerID(host.PublicKey())
	token := edgeproto.NewToken()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{
			{ID: id, Name: "prod", Online: true, Role: "member"},
		}})
	})
	mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		ws, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			return
		}
		conf := &ssh.ServerConfig{
			PublicKeyCallback: func(meta ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
				t.Errorf("reading the host key authenticated as %q", meta.User())
				return nil, errors.New("no")
			},
		}
		conf.AddHostKey(host)
		_, _, _, _ = ssh.NewServerConn(websocket.NetConn(context.Background(), ws, websocket.MessageBinary), conf)
	})
	edge := httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	// Any Ed25519 key serves as the device key: the server never sees it.
	if err = os.WriteFile(filepath.Join(dir, "edge-device-key"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"edges": map[string]any{
		edge.URL: map[string]any{"token": token, "signin_origin": edge.URL},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, edgeclient.TokensFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := edgeclient.New(dir, edge.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client, host
}

func TestLinkFromEdgeShowsWhatItPinsAndAsks(t *testing.T) {
	client, host := listingEdge(t)
	id := edgeproto.ServerID(host.PublicKey())
	ctx := context.Background()

	var out bytes.Buffer
	if err := confirmFromEdge(ctx, client, id, false, true, strings.NewReader("y\n"), &out); err != nil {
		t.Fatalf("confirmed link: %v", err)
	}
	for _, want := range []string{
		`server "prod"`, id, ssh.FingerprintSHA256(host.PublicKey()),
		"comes from " + client.Host(), "aether-server edge status", "link and pin this server? [y/N]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}

	if err := confirmFromEdge(ctx, client, id, false, true, strings.NewReader("\n"), &out); !errors.Is(err, errNotConfirmed) {
		t.Fatalf("link answered with Enter = %v, want it refused", err)
	}
	err := confirmFromEdge(ctx, client, id, false, false, strings.NewReader("y\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "aether link "+id) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("link without a terminal = %v, want a refusal naming aether link <id> and --yes", err)
	}
	if err := confirmFromEdge(ctx, client, id, true, false, strings.NewReader(""), &out); err != nil {
		t.Fatalf("link with --yes: %v", err)
	}
	if err := confirmFromEdge(ctx, client, testServerID, true, false, strings.NewReader(""), &out); err == nil ||
		!strings.Contains(err.Error(), "lists no server "+testServerID) {
		t.Fatalf("link of an id the edge does not list = %v", err)
	}
}

func TestParseLinkArgsFromEdge(t *testing.T) {
	opts, err := parseLinkArgs([]string{"--from-edge", testServerID, "--yes", "--addr", "tailnet-host"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.fromEdge || !opts.yes || opts.addr != testServerID || opts.direct != "tailnet-host" {
		t.Fatalf("options = %+v", opts)
	}
	for _, bad := range [][]string{
		{"--from-edge", "devbox"},
		{"--from-edge", testServerID, testServerID},
		{testServerID, "--yes"},
		{"--from-edge", testServerID, "--key", "k"},
		{"--from-edge", testServerID, "--claim", testServerID + "-abcdefghijklmnop"},
	} {
		if _, err := parseLinkArgs(bad); err == nil {
			t.Errorf("parseLinkArgs(%q) succeeded", bad)
		}
	}
}

func TestParseLinkArgsClaimLink(t *testing.T) {
	code := testServerID + "-abcdefghijklmnop"
	link := "aether://claim?edge=https%3A%2F%2Fedge.example&server=" + testServerID + "#abcdefghijklmnop"
	for _, args := range [][]string{
		{"--claim", link},
		{"--claim", link, "--edge", "https://EDGE.example/"},
	} {
		opts, err := parseLinkArgs(args)
		if err != nil || opts.claim != code || opts.edge != "https://edge.example" {
			t.Errorf("parseLinkArgs(%q) = %+v, %v; want the code and the link's edge", args, opts, err)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--claim", link, "--edge", "https://other.example"}, "the claim link names the edge https://edge.example; drop --edge"},
		{[]string{"--claim", "aether://claim?server=" + testServerID + "#abcdefghijklmnop"}, "a claim link looks like aether://claim?edge="},
		{[]string{link}, "a claim link goes after --claim: aether link --claim '<claim link>'"},
		{[]string{code}, "that is a claim code, which goes after --claim: aether link --claim <code>"},
		{[]string{"https://edge.example/join/" + testServerID}, "a join link looks like https://<edge host>/join/"},
		{[]string{"aether://nothing"}, "an address looks like host[:port], and a claim link goes after --claim"},
	} {
		_, err := parseLinkArgs(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseLinkArgs(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "abcdefghijklmnop") {
			t.Errorf("parseLinkArgs(%q) error carries the claim secret: %v", tc.args, err)
		}
	}
}
