package localgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestUpdateCheckSurvivesAnUnreachableServer(t *testing.T) {
	pinVersion(t)
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{errs: map[string]*protocol.Error{
		protocol.MethodServerInfo: {Code: protocol.CodeUnavailable, Message: "server unreachable: dial tcp: connection refused"},
	}}}
	g := updateGateway(t, backend, false)

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s (a dead server must not take the CLI answer with it)", rec.Code, rec.Body)
	}
	var got struct {
		CLI           selfupdate.Check `json:"cli"`
		ServerVersion string           `json:"server_version"`
		ServerBehind  bool             `json:"server_behind"`
		ServerError   string           `json:"server_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.CLI.UpdateAvailable || got.CLI.Latest != releaseTag {
		t.Fatalf("cli check = %+v, want the CLI half answered in full", got.CLI)
	}
	if got.ServerVersion != "" || got.ServerBehind {
		t.Errorf("server = %q behind=%v, want an unknown server rather than a guess", got.ServerVersion, got.ServerBehind)
	}
	if !strings.Contains(got.ServerError, "server unreachable") {
		t.Errorf("server_error = %q, want the backend's own message", got.ServerError)
	}
}

// The dev-build refusal is answered before the platform gate, so it holds
// on every platform, unlike the tests in update_apply_test.go.
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
