package main

import (
	"bytes"
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
	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

const testServerID = "wqc4lsjvzdzrwq3k5dabdtajwj"

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
	opts, err := parseLinkArgs([]string{"--claim", "wqc4lsjv-abcdefghijklmnop", "--addr", "tailnet-host"})
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

func TestPickServer(t *testing.T) {
	servers := []edgeproto.ServerInfo{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "prod", Role: "admin"},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "box", Role: "member"},
		{ID: "cccccccccccccccccccccccccc", Name: "box", Role: "viewer"},
	}
	if id, err := pickServer(servers, "prod"); err != nil || id != servers[0].ID {
		t.Fatalf("pickServer(prod) = %q, %v", id, err)
	}
	if id, err := pickServer(servers, servers[2].ID); err != nil || id != servers[2].ID {
		t.Fatalf("pickServer(id) = %q, %v", id, err)
	}
	if id, err := pickServer(servers, "laptop"); err != nil || id != "" {
		t.Fatalf("pickServer(laptop) = %q, %v; want no match", id, err)
	}
	_, err := pickServer(servers, "box")
	if err == nil || !strings.Contains(err.Error(), servers[1].ID) || !strings.Contains(err.Error(), servers[2].ID) {
		t.Fatalf("pickServer(box) = %v, want both ids listed", err)
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
			Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"},
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
		}})
	})

	out, err := captureStdout(t, func() error { return runLogin([]string{"--edge", edge.URL, "--label", "ci"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "WXYZ-1234") || !strings.Contains(out, edge.URL+"/device") || !strings.Contains(out, "octo") {
		t.Fatalf("login output %q lacks the code, the address or the account", out)
	}
	// With one edge signed in, later commands need no --edge.
	listed, err := captureStdout(t, func() error { return runServers(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, testServerID) || !strings.Contains(listed, "prod") || !strings.Contains(listed, "yes") {
		t.Fatalf("servers output %q", listed)
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
