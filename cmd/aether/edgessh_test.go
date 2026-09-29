package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

const testServerID = "wqc4lsjvzdzrwq3k5dabdtajwj"

func testAccount() edgeproto.AccountInfo {
	return edgeproto.AccountInfo{
		ID:      edgeproto.NewAccountID(),
		Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
	}
}

// serveEdgeInfo answers the edge's metadata on mux, naming origin() as
// the sign-in origin: the edge serves both from one process.
func serveEdgeInfo(t *testing.T, mux *http.ServeMux, origin func() string) {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET "+edgeproto.PathEdgeInfo, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.EdgeInfo{
			SigninOrigin: origin(), Key: key, Fingerprint: edgeproto.EdgeKeyFingerprint(key),
			Version: edgeproto.Version, MinVersion: edgeproto.MinVersion,
		})
	})
}

// stubSystemSSH records the arguments edge-ssh hands to the system ssh.
func stubSystemSSH(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	saved := systemSSH
	systemSSH = func(args []string, _ io.Reader, _, _ io.Writer) int {
		calls = append(calls, args)
		return 7
	}
	t.Cleanup(func() { systemSSH = saved })
	return &calls
}

func TestEdgeSSHPassesOtherHostsToSSHUnchanged(t *testing.T) {
	for _, args := range [][]string{
		{"git@github.com", "git-upload-pack 'o/r.git'"},
		{"-o", "SendEnv=GIT_PROTOCOL", "-p", "2222", "aether@host", "git-receive-pack '/ws.git'"},
		{"-G", "aether@host"},
		// A command line edge-ssh cannot parse is ssh's to judge.
		{"-Z", "whatever"},
		// A host that only ends like the logical domain is not one.
		{"aether@" + testServerID + ".edge.aether.invalid.example.com", "git-upload-pack '/ws.git'"},
	} {
		calls := stubSystemSSH(t)
		var stderr bytes.Buffer
		code := edgeSSH(args, strings.NewReader(""), io.Discard, &stderr)
		if code != 7 || len(*calls) != 1 || !slices.Equal((*calls)[0], args) {
			t.Fatalf("edgeSSH(%q) = %d, ssh got %q; want the arguments passed through unchanged", args, code, *calls)
		}
	}
}

func TestEdgeSSHHandlesEdgeHostsItself(t *testing.T) {
	testhome.Isolate(t)
	host := "aether@" + cli.EdgeHost(testServerID)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-p", "22", host, "git-upload-pack '/ws.git'"}, "ssh options are not supported"},
		{[]string{"-G", host}, "ssh options are not supported"},
		{[]string{host}, "no command was given"},
		{[]string{host, "git-upload-pack '/ws.git'"}, "not linked"},
	} {
		calls := stubSystemSSH(t)
		var stderr bytes.Buffer
		code := edgeSSH(tc.args, strings.NewReader(""), io.Discard, &stderr)
		if code != 255 || len(*calls) != 0 || !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("edgeSSH(%q) = %d, stderr %q, ssh calls %q; want 255 and %q", tc.args, code, stderr.String(), *calls, tc.want)
		}
	}
}

func TestParseLinkArgsEdgeForms(t *testing.T) {
	opts, err := parseLinkArgs([]string{"--claim", testServerID + "-abcdefghijklmnop", "--addr", "tailnet-host"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.claim == "" || opts.addr != "" || opts.direct != "tailnet-host" {
		t.Fatalf("claim options = %+v", opts)
	}
	for _, bad := range [][]string{
		{"--claim", "code", "host"},
		{"--claim", "code", "--invite", "x"},
		{},
	} {
		if _, err := parseLinkArgs(bad); err == nil {
			t.Errorf("parseLinkArgs(%q) succeeded", bad)
		}
	}
}

func TestLinkResolvesOnlyServerIDsOnTheEdge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("openBrowser calls ShellExecute, which PATH cannot stub")
	}
	testhome.Isolate(t)
	t.Setenv("PATH", t.TempDir())
	mux := http.NewServeMux()
	edge := httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	serveEdgeInfo(t, mux, func() string { return edge.URL })
	var key string
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		key = req.Key
		_ = json.NewEncoder(w).Encode(edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "WXYZ-1234", VerificationURI: edge.URL + "/device", Interval: 1,
		})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.DeviceTokenResponse{
			Token:   edgeproto.NewToken(),
			Device:  edgeproto.Device{ID: "dev-1", Label: "ci", Key: key},
			Account: testAccount(),
		})
	})
	// A stranger's server, named like the person's tailnet host, that
	// the account is only invited to.
	mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{
			{ID: testServerID, Name: "devbox", Online: true, Role: "member"},
		}})
	})
	if _, err := captureStdout(t, func() error { return runLogin([]string{"--edge", edge.URL, "--label", "ci"}) }); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"devbox", "localhost"} {
		got, err := edgeLinkOptions(linkOptions{addr: addr})
		if err != nil || got != (cli.LinkOptions{}) {
			t.Fatalf("edgeLinkOptions(%s) = %+v, %v; want the SSH address", addr, got, err)
		}
	}
	got, err := edgeLinkOptions(linkOptions{addr: testServerID, direct: "devbox"})
	want := cli.LinkOptions{Addr: "devbox", EdgeURL: edge.URL, ServerID: testServerID}
	if err != nil || got != want {
		t.Fatalf("edgeLinkOptions(id) = %+v, %v; want %+v", got, err, want)
	}
	code := testServerID + "-abcdefghijklmnop"
	got, err = edgeLinkOptions(linkOptions{claim: code, direct: "devbox"})
	want = cli.LinkOptions{Addr: "devbox", EdgeURL: edge.URL, Claim: code}
	if err != nil || got != want {
		t.Fatalf("edgeLinkOptions(claim) = %+v, %v; want %+v", got, err, want)
	}
	edge.Close()
	if _, err := edgeLinkOptions(linkOptions{claim: "not-a-code"}); err == nil || !strings.Contains(err.Error(), "link --claim") {
		t.Fatalf("edgeLinkOptions with a malformed code = %v, want it refused before the edge", err)
	}
	if got, err := edgeLinkOptions(linkOptions{addr: "devbox"}); err != nil || got != (cli.LinkOptions{}) {
		t.Fatalf("edgeLinkOptions(devbox) with the edge down = %+v, %v; want the SSH address", got, err)
	}
}

func TestLoginNeverPrintsTheToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("openBrowser calls ShellExecute, which PATH cannot stub")
	}
	testhome.Isolate(t)
	// Keep openBrowser from finding a real opener.
	t.Setenv("PATH", t.TempDir())
	token := edgeproto.NewToken()
	mux := http.NewServeMux()
	edge := httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	serveEdgeInfo(t, mux, func() string { return edge.URL })
	var key string
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		key = req.Key
		_ = json.NewEncoder(w).Encode(edgeproto.DeviceStartResponse{
			DeviceCode: "device-code", UserCode: "WXYZ-1234", VerificationURI: edge.URL + "/device", Interval: 1,
		})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.DeviceTokenResponse{
			Token:   token,
			Device:  edgeproto.Device{ID: "dev-1", Label: "ci", Key: key},
			Account: testAccount(),
		})
	})
	mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(edgeproto.ErrorBody{Error: string(edgeproto.RefusalTokenRevoked)})
			return
		}
		_ = json.NewEncoder(w).Encode(edgeproto.ServersResponse{Servers: []edgeproto.ServerInfo{
			{ID: testServerID, Name: "prod", Online: true, Role: "admin"},
			{ID: testServerID, Name: "solo", Online: true, Role: "admin", AccessPolicy: edgeproto.PolicyAccount},
		}})
	})

	out, err := captureStdout(t, func() error { return runLogin([]string{"--edge", edge.URL, "--label", "ci"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WXYZ-1234", edge.URL + "/device", "octo", "does not expire and is not refreshed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("login output %q lacks %q", out, want)
		}
	}
	// With one edge signed in, later commands need no --edge.
	listed, err := captureStdout(t, func() error { return runServers(nil) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		testServerID, "prod", "yes", "self-hosted",
		"approved-devices: a new device waits for approval", "account: signing in is enough",
		"aether link <id>", "aether link --from-edge <id>",
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("servers output %q lacks %q", listed, want)
		}
	}
	if strings.Contains(out+listed, token) {
		t.Fatal("a command printed the device token")
	}
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, edgeclient.TokensFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want 0600", edgeclient.TokensFile, info.Mode().Perm())
	}
}
