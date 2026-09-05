//go:build !windows

// The apply path past the platform gate. `update.apply` refuses outright on
// Windows, which cannot rename over a running executable, so these tests
// would only ever see that refusal there. The refusal itself is covered in
// update_apply_windows_test.go.

package localgw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

func TestUpdateApplyCarriesTheServerRestart(t *testing.T) {
	pinVersion(t)
	// The rebuild path is stubbed to "no app installed" in every apply
	// test that is not about the rebuild: with a real desktop app on the
	// developer's machine, update.apply would otherwise spawn a genuine
	// `aether gui build` and flip the response to its rebuilding shape.
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		// A single-box install: the swap replaced the server binary too.
		return []string{"/usr/local/bin/aether", "/usr/local/bin/aether-server"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Updated        []string `json:"updated"`
		RestartCommand string   `json:"restart_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updated) != 2 {
		t.Fatalf("updated = %v, want both binaries", got.Updated)
	}
	if got.RestartCommand != "sudo systemctl restart aether-server" {
		t.Fatalf("restart_command = %q, want the command aether update prints", got.RestartCommand)
	}
}

func TestUpdateApplyOmitsTheRestartWithoutAServer(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	// Assert the status before the body: an error envelope decodes into
	// this struct just as happily, leaving restart_command empty, so a
	// refusal would otherwise satisfy the assertion below.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		RestartCommand string `json:"restart_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RestartCommand != "" {
		t.Fatalf("restart_command = %q on a client-only machine", got.RestartCommand)
	}
}

func TestUpdateApplyRefusesWhenAlreadyCurrent(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = releaseTag
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("the newest release must not be downloaded over itself")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, releaseTag) {
		t.Fatalf("message = %q, want it to name the release already installed", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a refused update asked the process to exit")
	default:
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
	stubRebuild(t, "/bin/true", false)
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
	stubRebuild(t, "/bin/true", false)
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
	stubRebuild(t, "/bin/true", false)
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
