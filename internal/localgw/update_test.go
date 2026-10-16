package localgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
// GitHub latest-release redirect, so no test dials the network. The
// returned setter publishes a different tag mid-test, which is how a
// release shipping while the app is open is reproduced.
func releaseChecker(t *testing.T) (*selfupdate.Checker, func(string)) {
	t.Helper()
	var mu sync.Mutex
	tag := releaseTag
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		latest := tag
		mu.Unlock()
		http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return selfupdate.NewChecker(srv.URL, time.Hour), func(next string) {
		mu.Lock()
		defer mu.Unlock()
		tag = next
	}
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
	g, _ := releasingGateway(t, backend, supervised)
	return g
}

// releasingGateway is updateGateway plus the setter that publishes a new
// tag from its stub, for the tests where a release ships mid-run.
func releasingGateway(t *testing.T, backend Backend, supervised bool) (*Gateway, func(string)) {
	t.Helper()
	checker, setTag := releaseChecker(t)
	g, err := New(Config{
		Backend:    backend,
		CLI:        cli.Config{},
		Update:     checker,
		Supervised: supervised,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, setTag
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

// TestUpdateApplyRefusesPackagedBuild covers the Android app's sidecar: the
// APK is the update unit, so the gateway refuses before any release lookup,
// even with a newer release published and GitHub unreachable.
func TestUpdateApplyRefusesPackagedBuild(t *testing.T) {
	pinVersion(t)
	t.Setenv(selfupdate.PackagedEnv, "Android app")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a packaged build must not look up a release to install")
	}))
	t.Cleanup(srv.Close)
	g, err := New(Config{
		Backend:  &verbStubBackend{},
		Update:   selfupdate.NewChecker(srv.URL, time.Hour),
		Packaged: "Android app",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("a packaged build must not reach the binary swap")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	want := "this aether is part of the Android app, which installs its updates; update the Android app instead"
	if perr.Code != protocol.CodeInvalidState || perr.Message != want {
		t.Fatalf("error = %d %q, want %d %q", perr.Code, perr.Message, protocol.CodeInvalidState, want)
	}
}

// TestUpdateCheckReportsReleaseToPackagedBuild is what the Android app's
// update notice reads: the newest release and its page, with no asset to
// download and no self-update.
func TestUpdateCheckReportsReleaseToPackagedBuild(t *testing.T) {
	pinVersion(t)
	t.Setenv(selfupdate.PackagedEnv, "Android app")
	checker, _ := releaseChecker(t)
	g, err := New(Config{Backend: &verbStubBackend{}, Update: checker, Packaged: "Android app"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLI selfupdate.Check `json:"cli"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	cli := got.CLI
	if cli.Packaged != "Android app" || !cli.UpdateAvailable || cli.Latest != releaseTag ||
		cli.CanSelfUpdate || cli.Asset != "" || !strings.HasSuffix(cli.ReleaseURL, "/releases/tag/"+releaseTag) {
		t.Fatalf("cli = %+v, want %s available from its release page with no asset and no self-update", cli, releaseTag)
	}
}

// TestUpdateCheckRefreshBypassesTheCache covers the read behind the Update
// button: it names the tag that is about to be installed, so it may not be
// served from an answer that is up to an hour old.
func TestUpdateCheckRefreshBypassesTheCache(t *testing.T) {
	pinVersion(t)
	g, setTag := releasingGateway(t, &verbStubBackend{}, false)

	latest := func(params string) string {
		t.Helper()
		rec := do(g, http.MethodPost, "/local/v1/update.check", params, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var got struct {
			CLI selfupdate.Check `json:"cli"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.CLI.Latest
	}

	if tag := latest("{}"); tag != releaseTag {
		t.Fatalf("latest = %q, want %q", tag, releaseTag)
	}
	setTag("v1.4.0")
	if tag := latest("{}"); tag != releaseTag {
		t.Fatalf("cached latest = %q, want the cached %q", tag, releaseTag)
	}
	if tag := latest(`{"refresh":true}`); tag != "v1.4.0" {
		t.Fatalf("refreshed latest = %q, want v1.4.0", tag)
	}
}

// TestUpdateCheckAnswersWithoutGitHub: a release lookup that fails costs
// the CLI half only, so a server refusing this client still says so.
func TestUpdateCheckAnswersWithoutGitHub(t *testing.T) {
	pinVersion(t)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(github.Close)
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
		protocol.MethodServerInfo: json.RawMessage(`{"server_version":"v1.3.0","min_client_version":"v1.3.0"}`),
	}}}
	g, err := New(Config{Backend: backend, Update: selfupdate.NewChecker(github.URL, time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(g, http.MethodPost, "/local/v1/update.check", `{"refresh":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLI                  selfupdate.Check `json:"cli"`
		CLIError             string           `json:"cli_error"`
		ClientUpdateRequired bool             `json:"client_update_required"`
		MinClientVersion     string           `json:"min_client_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.CLIError, "check for releases: ") || got.CLI.Version != runningTag {
		t.Fatalf("cli = %+v, cli_error = %q", got.CLI, got.CLIError)
	}
	if !got.ClientUpdateRequired || got.MinClientVersion != "v1.3.0" {
		t.Fatalf("client_update_required = %v, min_client_version = %q", got.ClientUpdateRequired, got.MinClientVersion)
	}
}
