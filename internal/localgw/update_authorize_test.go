//go:build !windows

// The apply path through the macOS administrator dialog, as the gateway
// sees it: the install is stubbed, the answers and their codes are what
// the banner acts on.

package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/macinstall"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// stubProbe replaces the binary-location probe for one test.
func stubProbe(t *testing.T, fn func() (selfupdate.Access, error)) {
	t.Helper()
	old := probeAccess
	t.Cleanup(func() { probeAccess = old })
	probeAccess = fn
}

func TestUpdateCheckSaysHowTheBinaryInstalls(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubProbe(t, func() (selfupdate.Access, error) {
		return selfupdate.Access{Path: "/usr/local/bin/aether", Method: selfupdate.MethodAdminPrompt}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLIPath       string `json:"cli_path"`
		InstallMethod string `json:"install_method"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CLIPath != "/usr/local/bin/aether" || got.InstallMethod != "admin-prompt" {
		t.Fatalf("got %+v, want the probe's path and method", got)
	}
}

func TestUpdateCheckOmitsTheInstallMethodWhenTheProbeFails(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubProbe(t, func() (selfupdate.Access, error) {
		return selfupdate.Access{}, errors.New("locate this binary: gone")
	})

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cli_path", "install_method"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s present after a failed probe: %s", key, got[key])
		}
	}
}

func TestUpdateApplyReportsACancelledDialog(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return nil, &wrapped{macinstall.ErrCanceled, "41:63: execution error: User canceled. (-128)"}
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 so the banner can tell a cancel from a failure: %s", rec.Code, rec.Body)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeDenied {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeDenied)
	}
	if !strings.Contains(perr.Message, "nothing was changed") || !strings.Contains(perr.Message, "(-128)") {
		t.Fatalf("message = %q, want it to say nothing changed and keep osascript's line", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a cancelled update asked the process to exit")
	default:
	}
	if g.rebuild.snapshot().Phase != "idle" {
		t.Fatal("a cancelled update started a rebuild")
	}
}

func TestUpdateApplyNamesSudoWithoutAGUISession(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return nil, &wrapped{macinstall.ErrNoSession, "execution error: No user interaction allowed. (-1713)"}
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "sudo aether update") || !strings.Contains(perr.Message, "(-1713)") {
		t.Fatalf("message = %q, want the sudo command and osascript's line", perr.Message)
	}
}

func TestUpdateApplyReportsARootSideMismatch(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return nil, errors.New("replace /usr/local/bin/aether: osascript: execution error: copied binary does not match the release checksum (65)")
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeUnavailable {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeUnavailable)
	}
	if !strings.Contains(perr.Message, "install "+releaseTag) || !strings.Contains(perr.Message, "(65)") {
		t.Fatalf("message = %q, want the install prefix and the real error", perr.Message)
	}
}

func TestUpdateApplyRunsOneInstallAtATime(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	started := make(chan struct{})
	release := make(chan struct{})
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		close(started)
		<-release
		return []string{"/usr/local/bin/aether"}, nil
	})

	first := make(chan int, 1)
	go func() {
		first <- do(g, http.MethodPost, "/local/v1/update.apply", "{}", true).Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first apply never reached the install")
	}

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeConflict {
		t.Fatalf("second apply: code = %d, want %d: %s", perr.Code, protocol.CodeConflict, rec.Body)
	}
	close(release)
	select {
	case code := <-first:
		if code != http.StatusOK {
			t.Fatalf("first apply: status = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first apply never finished")
	}

	// The guard is released with the request: a third apply is answered,
	// from the release the first one already installed.
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})
	if rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true); rec.Code != http.StatusOK {
		t.Fatalf("third apply: status = %d: %s", rec.Code, rec.Body)
	}
}

// A second click after a successful swap - another tab, a reload - must
// not download the release again or ask for the password again: the
// binary is already on disk, and the answer says what is left to do.
func TestUpdateApplyDoesNotInstallTheSameReleaseTwice(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	calls := 0
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		calls++
		return []string{"/usr/local/bin/aether"}, nil
	})

	for i := 0; i < 2; i++ {
		rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("apply %d: status = %d: %s", i+1, rec.Code, rec.Body)
		}
		var got struct {
			Updated []string `json:"updated"`
			Version string   `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Version != releaseTag || len(got.Updated) != 1 {
			t.Fatalf("apply %d = %+v, want the installed release and its path", i+1, got)
		}
	}
	if calls != 1 {
		t.Fatalf("the install ran %d times, want once", calls)
	}
}

// Once the rebuild the first click started has finished, a repeat click
// on an unsupervised gateway must not build the app a second time under
// the one the user may just have restarted.
func TestUpdateApplyDoesNotRebuildAFinishedApp(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})
	builds := 0
	stubRebuild(t, fakeBuild(t, "", "", 0), true)
	oldArgv := rebuildArgv
	t.Cleanup(func() { rebuildArgv = oldArgv })
	inner := rebuildArgv
	rebuildArgv = func(bin string, who localops.RealUser, json bool) []string {
		builds++
		return inner(bin, who, json)
	}

	first := applyForRebuild(t, g)
	if !first.Rebuilding {
		t.Fatalf("first apply = %+v, want a rebuild", first)
	}
	awaitPhase(t, g, localops.PhaseDone)

	second := applyForRebuild(t, g)
	if second.Rebuilding {
		t.Fatalf("second apply = %+v, want no second rebuild", second)
	}
	if !strings.Contains(second.Note, "restart it") {
		t.Fatalf("note = %q, want it to say the app only needs a restart", second.Note)
	}
	if builds != 1 {
		t.Fatalf("the app was built %d times, want once", builds)
	}
}

// wrapped is an error with a sentinel underneath and the text the dialog
// produced, the shape selfupdate hands back.
type wrapped struct {
	sentinel error
	detail   string
}

func (w *wrapped) Error() string { return w.sentinel.Error() + ": " + w.detail }
func (w *wrapped) Unwrap() error { return w.sentinel }
