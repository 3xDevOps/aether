package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localgw"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/testhome"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/3xDevOps/Aether/internal/webgate"
)

func TestParseClientBanner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		banner, version string
		legacy          bool
	}{
		{"SSH-2.0-Aether v0.8.0", "v0.8.0", false},
		{"SSH-2.0-Aether v0.8.0-alpha.3-5-gabc1234-dirty", "v0.8.0-alpha.3-5-gabc1234-dirty", false},
		{"SSH-2.0-Aether dev", "dev", false},
		{"SSH-2.0-Go", "", true},
		{"SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13", "", true},
		{"SSH-2.0-Aether", "", true},
		{"SSH-2.0-AetherX v0.8.0", "", true},
	}
	for _, c := range cases {
		v, legacy := parseClientBanner(c.banner)
		if v != c.version || legacy != c.legacy {
			t.Errorf("parseClientBanner(%q) = %q, %v; want %q, %v", c.banner, v, legacy, c.version, c.legacy)
		}
	}
}

// gateRelease is a server built past a tag: the floor it gates on is the
// tag, a release clients can actually install.
const (
	gateRelease = "v0.8.0-alpha.3-5-gabc1234"
	gateFloor   = "v0.8.0-alpha.3"
)

func dialBanner(t *testing.T, e *testEnv, banner string) *ssh.Client {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            "aether",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(e.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		ClientVersion:   banner,
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", e.addr, cfg)
	if err != nil {
		t.Fatalf("dial as %q: %v", banner, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func controlCall(t *testing.T, client *ssh.Client, method string, result any) error {
	t.Helper()
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	defer func() { _ = pipe.Close() }()
	return protocol.NewClient(pipe).Call(method, struct{}{}, result)
}

func wantUpgradeRequired(t *testing.T, err error, clientVersion string) {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUpgradeRequired {
		t.Fatalf("error = %v, want code %d", err, protocol.CodeUpgradeRequired)
	}
	client := clientVersion
	if client == "" {
		client = "older than " + gateFloor
	}
	wantMessage := "this aether is " + client + " and the server runs " + gateRelease +
		"; update this client first: run `aether update` (desktop: Update in the app; Android: install the latest aether-android.apk), then retry"
	if perr.Message != wantMessage {
		t.Errorf("message = %q\nwant      %q", perr.Message, wantMessage)
	}
	var data protocol.UpgradeRequiredData
	if err := json.Unmarshal(perr.Data, &data); err != nil {
		t.Fatalf("data %s: %v", perr.Data, err)
	}
	want := protocol.UpgradeRequiredData{
		ClientVersion: clientVersion, ServerVersion: gateRelease, MinClientVersion: gateFloor, Command: "aether update",
	}
	if data != want {
		t.Errorf("data = %+v, want %+v", data, want)
	}
}

func TestClientVersionGateControl(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.release = gateRelease })

	for _, c := range []struct{ name, banner, version string }{
		{"legacy", "SSH-2.0-Go", ""},
		{"older", "SSH-2.0-Aether v0.8.0-alpha.2", "v0.8.0-alpha.2"},
		{"older past its tag", "SSH-2.0-Aether v0.7.9-3-gdeadbee", "v0.7.9-3-gdeadbee"},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := dialBanner(t, e, c.banner)
			var info protocol.ServerInfoResult
			if err := controlCall(t, client, protocol.MethodServerInfo, &info); err != nil {
				t.Fatalf("server.info: %v", err)
			}
			if info.ServerVersion != gateRelease || info.MinClientVersion != gateFloor {
				t.Fatalf("server.info = %q min %q, want %q min %q", info.ServerVersion, info.MinClientVersion, gateRelease, gateFloor)
			}
			err := controlCall(t, client, protocol.MethodServerUpdateStatus, nil)
			if protocol.IsUpgradeRequired(err) {
				t.Fatalf("server.update_status gated: %v", err)
			}
			wantUpgradeRequired(t, controlCall(t, client, protocol.MethodWorkspaceList, nil), c.version)
			// A method this server never had is refused for the version,
			// not reported missing.
			wantUpgradeRequired(t, controlCall(t, client, "workspace.removed_long_ago", nil), c.version)
		})
	}

	for _, banner := range []string{
		"SSH-2.0-Aether " + gateFloor,
		"SSH-2.0-Aether " + gateRelease,
		"SSH-2.0-Aether v0.8.0-alpha.3-1-g0000000",
		"SSH-2.0-Aether v0.8.0",
		"SSH-2.0-Aether v0.9.0",
		"SSH-2.0-Aether dev",
		"SSH-2.0-Aether (devel)",
		"SSH-2.0-Aether 1a2b3c4",
	} {
		t.Run(banner, func(t *testing.T) {
			var out protocol.WorkspaceListResult
			if err := controlCall(t, dialBanner(t, e, banner), protocol.MethodWorkspaceList, &out); err != nil {
				t.Fatalf("workspace.list: %v", err)
			}
		})
	}

	t.Run("server gateway", func(t *testing.T) {
		local := e.srv.Local(e.member.ID)
		g, err := webgate.New(webgate.Config{Authorize: func(*http.Request, bool) (webgate.Backend, *webgate.Refusal) {
			return localBackend{local}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Close() })
		req := httptest.NewRequest(http.MethodPost, "/api/v1/"+protocol.MethodWorkspaceList, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("workspace.list status = %d: %s", rec.Code, rec.Body)
		}
		events, err := local.Events(t.Context(), protocol.SubscribeRequest{})
		if err != nil {
			t.Fatalf("Local events: %v", err)
		}
		_ = events.Close()
	})
}

// localBackend serves a webgate from Local the way the server gateway does.
type localBackend struct{ local *Local }

func (b localBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	return b.local.Call(ctx, method, params)
}

func (b localBackend) Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	return b.local.Events(ctx, req)
}

func (b localBackend) Attach(ctx context.Context, req protocol.AttachRequest) (webgate.Terminal, protocol.AttachResponse, error) {
	return nil, protocol.AttachResponse{}, errors.New("attach not served in this test")
}

func (b localBackend) Terminal(ctx context.Context, req protocol.TerminalRequest) (webgate.Terminal, protocol.TerminalResponse, error) {
	return nil, protocol.TerminalResponse{}, errors.New("terminal not served in this test")
}

func TestClientVersionGateDevServer(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.release = "dev" })
	if err := controlCall(t, dialBanner(t, e, "SSH-2.0-Go"), protocol.MethodWorkspaceList, nil); err != nil {
		t.Fatalf("dev server gated a legacy client: %v", err)
	}
}

func TestClientVersionGateStreams(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.release = gateRelease })
	client := dialBanner(t, e, "SSH-2.0-Aether v0.8.0-alpha.2")

	for _, name := range []string{
		protocol.SubsystemEvents, protocol.SubsystemAttach, protocol.SubsystemTerminal, protocol.SubsystemSync,
		protocol.SubsystemACP, protocol.SubsystemAssistant, protocol.SubsystemRunSSH,
		protocol.SubsystemDevBrowser, protocol.SubsystemDevArtifact,
	} {
		t.Run(name, func(t *testing.T) {
			ch, reqs, err := client.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ch.Close() }()
			exit := make(chan uint32, 1)
			go func() {
				for req := range reqs {
					var status struct{ Status uint32 }
					if req.Type == "exit-status" && ssh.Unmarshal(req.Payload, &status) == nil {
						exit <- status.Status
					}
				}
			}()
			if ok, rerr := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{name})); rerr != nil || !ok {
				t.Fatalf("subsystem %s: ok=%v err=%v", name, ok, rerr)
			}
			if _, err = ch.Write([]byte(`{"run_id":"` + string(e.run.ID) + `"}` + "\n")); err != nil {
				t.Fatalf("write header: %v", err)
			}
			line, err := bufio.NewReader(ch).ReadBytes('\n')
			if err != nil {
				t.Fatalf("read ack: %v", err)
			}
			var ack struct {
				OK    bool            `json:"ok"`
				Code  int             `json:"code"`
				Error string          `json:"error"`
				Data  json.RawMessage `json:"data"`
			}
			if err = json.Unmarshal(line, &ack); err != nil {
				t.Fatalf("ack %s: %v", line, err)
			}
			if ack.OK {
				t.Fatalf("ack = %s", line)
			}
			wantUpgradeRequired(t, &protocol.Error{Code: ack.Code, Message: ack.Error, Data: ack.Data}, "v0.8.0-alpha.2")
			stderr, err := io.ReadAll(ch.Stderr())
			if err != nil || !strings.Contains(string(stderr), ack.Error) {
				t.Fatalf("stderr = %q (%v), want the refusal", stderr, err)
			}
			select {
			case status := <-exit:
				if status != 1 {
					t.Fatalf("exit status = %d, want 1", status)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no exit status")
			}
		})
	}

	t.Run("direct-tcpip", func(t *testing.T) {
		_, _, err := client.OpenChannel("direct-tcpip", ssh.Marshal(forwardTestPayload{
			DestHost: "run:" + string(e.run.ID), DestPort: 1, OrigHost: "127.0.0.1",
		}))
		var openErr *ssh.OpenChannelError
		if !errors.As(err, &openErr) || openErr.Reason != ssh.Prohibited || !strings.Contains(openErr.Message, "run `aether update`") {
			t.Fatalf("direct-tcpip open = %v, want the upgrade refusal", err)
		}
	})

	t.Run("git", func(t *testing.T) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sess.Close() }()
		if err := sess.Run("git-upload-pack '/" + string(e.ws.ID) + ".git'"); err != nil {
			t.Fatalf("git-upload-pack: %v", err)
		}
		if calls := e.git.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "upload-pack") {
			t.Fatalf("git calls = %v", calls)
		}
	})
}

// TestClientVersionGateGateway drives the gateway a gated CLI serves the
// dashboard from: server calls answer 426 with the refusal, and the update
// verbs, which never need the server, keep working. Not parallel: the
// client's version is version.Version.
func TestClientVersionGateGateway(t *testing.T) {
	home := testhome.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	signer := testhome.WriteSSHKey(t, keyPath, testhome.Ed25519Key(t), "")
	e := newTestEnvWithSigner(t, func(c *Config) { c.release = gateRelease }, signer)

	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = "v0.8.0-alpha.2"

	releases := releasetest.Serve(t, gateFloor, map[string][]byte{"aether-linux-amd64": []byte("binary")})
	cliCfg := cli.Config{Addr: e.addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")}
	conn, err := cli.Dial(cliCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _, err = conn.ACPStream(protocol.ACPStreamRequest{RunID: string(e.run.ID)})
	wantUpgradeRequired(t, err, "v0.8.0-alpha.2")
	_, _, err = conn.AssistantStream(protocol.AssistantStreamRequest{})
	wantUpgradeRequired(t, err, "v0.8.0-alpha.2")
	_, _, err = conn.RunSSH(string(e.run.ID))
	wantUpgradeRequired(t, err, "v0.8.0-alpha.2")
	_, err = conn.Sync(string(e.run.ID), false)
	wantUpgradeRequired(t, err, "v0.8.0-alpha.2")

	backend := localgw.NewSSHBackend(cliCfg)
	g, err := localgw.New(localgw.Config{Backend: backend, Update: selfupdate.NewChecker(releases.URL, time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })

	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+g.Token())
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/api/v1/workspace.list")
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("workspace.list status = %d: %s", rec.Code, rec.Body)
	}
	var body webgate.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("error body %s: %v", rec.Body, err)
	}
	wantUpgradeRequired(t, body.Error, "v0.8.0-alpha.2")

	rec = post("/local/v1/update.check")
	if rec.Code != http.StatusOK {
		t.Fatalf("update.check status = %d: %s", rec.Code, rec.Body)
	}
	var check struct {
		CLI                  selfupdate.Check `json:"cli"`
		ServerVersion        string           `json:"server_version"`
		ClientUpdateRequired bool             `json:"client_update_required"`
		MinClientVersion     string           `json:"min_client_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	if !check.ClientUpdateRequired || check.MinClientVersion != gateFloor || check.ServerVersion != gateRelease {
		t.Fatalf("update.check = %s", rec.Body)
	}
	if !check.CLI.UpdateAvailable || check.CLI.Latest != gateFloor {
		t.Fatalf("update.check cli = %+v, want %s available", check.CLI, gateFloor)
	}

	if rec = post("/local/v1/update.status"); rec.Code != http.StatusOK {
		t.Fatalf("update.status status = %d: %s", rec.Code, rec.Body)
	}
}
