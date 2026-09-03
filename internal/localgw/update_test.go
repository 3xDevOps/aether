package localgw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// The update verbs run against a build one release behind the stub
// checker's newest tag.
const (
	runningTag = "v1.2.9"
	releaseTag = "v1.3.0"
)

// releaseChecker points the update verbs at a stub that answers the
// GitHub latest-release redirect, so no test dials the network.
func releaseChecker(t *testing.T) *selfupdate.Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+releaseTag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return selfupdate.NewChecker(srv.URL, time.Hour)
}

// pinVersion makes this a release build for one test; the check reads the
// version to decide whether an update exists.
func pinVersion(t *testing.T) {
	t.Helper()
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = runningTag
}

// stubApply replaces the binary-swapping call for one test.
func stubApply(t *testing.T, fn func(context.Context, string, string) ([]string, error)) {
	t.Helper()
	old := applyUpdate
	t.Cleanup(func() { applyUpdate = old })
	applyUpdate = fn
}

// updateGateway builds a gateway whose release check is stubbed.
func updateGateway(t *testing.T, backend Backend, supervised bool) *Gateway {
	t.Helper()
	g, err := New(Config{
		Backend:    backend,
		CLI:        cli.Config{},
		Update:     releaseChecker(t),
		Supervised: supervised,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestUpdateCheckReportsBehindServer(t *testing.T) {
	pinVersion(t)
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
		protocol.MethodServerInfo: json.RawMessage(`{"server_version":"v1.2.9","protocol_version":"1"}`),
	}}}
	g := updateGateway(t, backend, true)

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLI           selfupdate.Check `json:"cli"`
		ServerVersion string           `json:"server_version"`
		ServerBehind  bool             `json:"server_behind"`
		Supervised    bool             `json:"supervised"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.CLI.UpdateAvailable || got.CLI.Latest != releaseTag || got.CLI.Version != runningTag {
		t.Fatalf("cli check = %+v", got.CLI)
	}
	if got.ServerVersion != "v1.2.9" || !got.ServerBehind {
		t.Fatalf("server = %q behind=%v, want v1.2.9 behind", got.ServerVersion, got.ServerBehind)
	}
	if !got.Supervised {
		t.Error("supervised = false on a shell-spawned gateway")
	}
}

func TestUpdateCheckReportsDevBuild(t *testing.T) {
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
		protocol.MethodServerInfo: json.RawMessage(`{"server_version":"v1.3.0"}`),
	}}}
	g := updateGateway(t, backend, false)

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLI          selfupdate.Check `json:"cli"`
		ServerBehind bool             `json:"server_behind"`
		Supervised   bool             `json:"supervised"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.CLI.Dev || got.CLI.UpdateAvailable {
		t.Fatalf("cli check = %+v, want a dev build with no update", got.CLI)
	}
	if got.ServerBehind || got.Supervised {
		t.Errorf("server_behind=%v supervised=%v, want both false", got.ServerBehind, got.Supervised)
	}
}

func TestUpdateApplyRefusesDevBuild(t *testing.T) {
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("a dev build must not reach the binary swap")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "dev build") {
		t.Fatalf("message = %q, want it to name the dev build", perr.Message)
	}
}

func TestUpdateApplyRefusesUnwritableBinary(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return nil, fmt.Errorf("/usr/local/bin is not writable by this user, re-run as: sudo aether update: %w", os.ErrPermission)
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "sudo aether update") {
		t.Fatalf("message = %q, want it to name sudo aether update", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a refused update asked the process to exit")
	default:
	}
}

func TestUpdateApplySupervisedExits(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(_ context.Context, baseURL, tag string) ([]string, error) {
		if tag != releaseTag {
			t.Errorf("tag = %q, want %s", tag, releaseTag)
		}
		if baseURL != g.cfg.Update.BaseURL() {
			t.Errorf("baseURL = %q, want the checker's %q", baseURL, g.cfg.Update.BaseURL())
		}
		return []string{"/usr/local/bin/aether", "/usr/local/bin/aether-server"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Updated    []string `json:"updated"`
		Version    string   `json:"version"`
		Restarting bool     `json:"restarting"`
		Note       string   `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updated) != 2 || got.Version != releaseTag || !got.Restarting {
		t.Fatalf("apply = %+v", got)
	}
	select {
	case <-g.Exit():
	default:
		t.Fatal("a supervised apply left the exit channel open")
	}
}

func TestUpdateApplyUnsupervisedKeepsServing(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Restarting bool   `json:"restarting"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Restarting {
		t.Error("restarting = true without a supervising shell")
	}
	if !strings.Contains(got.Note, "aether gui") {
		t.Errorf("note = %q, want it to name aether gui", got.Note)
	}
	select {
	case <-g.Exit():
		t.Fatal("an unsupervised apply asked the process to exit")
	default:
	}
}

// TestUpdateApplySupervisedResponseSurvivesShutdown drives the real
// listener the way the desktop shell does: the process waits on Exit and
// closes the gateway the moment update.apply asks it to. The client must
// still receive the whole response, which is what Close's Shutdown drain
// buys.
func TestUpdateApplySupervisedResponseSurvivesShutdown(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})
	if err := g.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	closed := make(chan error, 1)
	go func() {
		<-g.Exit()
		closed <- g.Close()
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+g.Addr()+"/local/v1/update.apply", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+g.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Updated    []string `json:"updated"`
		Restarting bool     `json:"restarting"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response after shutdown: %v", err)
	}
	if len(got.Updated) != 1 || !got.Restarting {
		t.Fatalf("apply = %+v", got)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(closeTimeout + time.Second):
		t.Fatal("gateway never shut down")
	}
}
