//go:build !windows

// The desktop-app rebuild update.apply starts. The fixtures are shell
// scripts standing in for `aether gui build --json`, so this file is unix
// only; update.apply itself refuses on Windows before it gets here.

package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
)

// fakeBuild writes a script that stands in for `aether gui build --json`:
// body goes on stdout, noise on stderr, and it exits with code.
func fakeBuild(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gui-build")
	script := "#!/bin/sh\n" +
		"printf '%s' " + shellQuote(stderr) + " >&2\n" +
		"printf '%s' " + shellQuote(stdout) + "\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// stubRebuild points the rebuild at script and claims an app is installed,
// so no test builds a real Electron app.
func stubRebuild(t *testing.T, script string, installed bool) {
	t.Helper()
	oldArgv, oldApp, oldUser := rebuildArgv, installedDesktopApp, lookupRealUser
	t.Cleanup(func() {
		rebuildArgv, installedDesktopApp, lookupRealUser = oldArgv, oldApp, oldUser
	})
	rebuildArgv = func(string, localops.RealUser, bool) []string { return []string{script} }
	installedDesktopApp = func(string, localops.RealUser) (string, bool) {
		return "/home/u/.local/share/aether/desktop", installed
	}
	cacheHome(t)
}

// cacheHome points os.UserCacheDir at a temporary directory so the build
// error record never touches the developer's real cache.
func cacheHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", dir)
		return
	}
	t.Setenv("XDG_CACHE_HOME", dir)
}

// applyForRebuild runs update.apply against a gateway whose binary swap is
// stubbed, and returns the decoded answer.
func applyForRebuild(t *testing.T, g *Gateway) struct {
	Restarting bool   `json:"restarting"`
	Rebuilding bool   `json:"rebuilding"`
	Note       string `json:"note"`
} {
	t.Helper()
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})
	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Restarting bool   `json:"restarting"`
		Rebuilding bool   `json:"rebuilding"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// status reads update.status.
func status(t *testing.T, g *Gateway) rebuildStatus {
	t.Helper()
	rec := do(g, http.MethodPost, "/local/v1/update.status", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got rebuildStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// waitForExit blocks until the gateway asks the process to stop.
func waitForExit(t *testing.T, g *Gateway) {
	t.Helper()
	select {
	case <-g.Exit():
	case <-time.After(10 * time.Second):
		t.Fatalf("the gateway never asked to exit; update.status = %+v", status(t, g))
	}
}

func TestUpdateStatusIsIdleBeforeAnyRebuild(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	if got := status(t, g); got.Phase != phaseIdle {
		t.Fatalf("phase = %q, want %q", got.Phase, phaseIdle)
	}
}

func TestUpdateApplyRebuildsThenAsksForARelaunch(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, fakeBuild(t,
		`{"phase":"unpacking"}
{"phase":"installing dependencies"}
{"phase":"packaging"}
{"phase":"installing"}
{"phase":"done","path":"/home/u/.local/share/aether/desktop"}
`, "npm warn deprecated glob@7\n", 0), true)

	got := applyForRebuild(t, g)
	if !got.Rebuilding || !got.Restarting {
		t.Fatalf("apply = %+v, want a rebuild on a supervised gateway", got)
	}
	waitForExit(t, g)
	if g.ExitCode() != ExitRelaunch {
		t.Fatalf("exit code = %d, want %d so the shell relaunches", g.ExitCode(), ExitRelaunch)
	}
	final := status(t, g)
	if final.Phase != localops.PhaseDone {
		t.Fatalf("phase = %q, want %q", final.Phase, localops.PhaseDone)
	}
	// The build's own output rides along so the banner can show progress.
	if len(final.LinesTail) == 0 || !strings.Contains(strings.Join(final.LinesTail, "\n"), "npm warn") {
		t.Fatalf("lines_tail = %v, want the build's output", final.LinesTail)
	}
	if got := localops.LastDesktopBuildError(); got != "" {
		t.Fatalf("a build that worked recorded %q", got)
	}
}

func TestUpdateApplyRecordsAFailedRebuildAndRespawns(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, fakeBuild(t,
		`{"phase":"unpacking"}
{"phase":"installing dependencies"}
{"phase":"error","error":"localops: npm install --no-audit --no-fund: exit status 1"}
`, "npm error code ENOENT\n", 1), true)

	if got := applyForRebuild(t, g); !got.Rebuilding {
		t.Fatalf("apply = %+v, want a rebuild", got)
	}
	waitForExit(t, g)
	// A failed rebuild leaves the CLI updated, so the shell respawns the
	// sidecar rather than relaunching onto an app that was never replaced.
	if g.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0 so the shell respawns", g.ExitCode())
	}
	final := status(t, g)
	if final.Phase != localops.PhaseError {
		t.Fatalf("phase = %q, want %q", final.Phase, localops.PhaseError)
	}
	if !strings.Contains(final.Error, "npm install") {
		t.Fatalf("error = %q, want the build's own message", final.Error)
	}
	// The gateway that saw the failure is exiting, so the next one reads it
	// off disk and the banner can still say what went wrong.
	if got := localops.LastDesktopBuildError(); !strings.Contains(got, "npm install") {
		t.Fatalf("recorded error = %q", got)
	}
	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	var check struct {
		ShellBuildError string `json:"shell_build_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(check.ShellBuildError, "npm install") {
		t.Fatalf("update.check shell_build_error = %q", check.ShellBuildError)
	}
}

// A server box, or a client that never ran `aether gui build`: nothing is
// built and nothing is downloaded, and the shell restarts as it always did.
func TestUpdateApplySkipsTheRebuildWithoutAnInstalledApp(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, fakeBuild(t, "", "", 0), false)
	rebuildArgv = func(string, localops.RealUser, bool) []string {
		t.Fatal("no app is installed; nothing may be built")
		return nil
	}

	got := applyForRebuild(t, g)
	if got.Rebuilding {
		t.Fatalf("apply = %+v, want no rebuild", got)
	}
	waitForExit(t, g)
	if g.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want the plain respawn", g.ExitCode())
	}
}

// A gateway nobody supervises - `aether gui` in a browser tab - must never
// exit under the user. It still rebuilds the app and says to restart it.
func TestUpdateApplyNeverExitsAnUnsupervisedGateway(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubRebuild(t, fakeBuild(t, `{"phase":"done","path":"/home/u/.local/share/aether/desktop"}`+"\n", "", 0), true)

	got := applyForRebuild(t, g)
	if !got.Rebuilding {
		t.Fatalf("apply = %+v, want a rebuild", got)
	}
	if !strings.Contains(got.Note, "restart") {
		t.Fatalf("note = %q, want it to name the restart the user has to do", got.Note)
	}
	deadline := time.Now().Add(10 * time.Second)
	for status(t, g).Phase != localops.PhaseDone {
		if time.Now().After(deadline) {
			t.Fatalf("the rebuild never finished: %+v", status(t, g))
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-g.Exit():
		t.Fatal("an unsupervised gateway exited under the browser tab that asked")
	default:
	}
}

// scriptBuild writes a script that stands in for `aether gui build --json`
// with a body of its own, for the cases fakeBuild's fixed output cannot
// express - a very long line, or a child that never returns.
func scriptBuild(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gui-build")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// longLine is shell that writes one stderr line of n bytes. Building it
// with head and tr rather than a loop keeps the fixture fast enough that a
// wedged reader, not the fixture, is what a timeout points at.
func longLine(n int) string {
	return "head -c " + strconv.Itoa(n) + " /dev/zero | tr '\\0' x >&2\necho >&2\n"
}

// awaitPhase blocks until update.status reports phase, or fails.
func awaitPhase(t *testing.T, g *Gateway, phase string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := status(t, g)
		if got.Phase == phase {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase = %q, want %q", got.Phase, phase)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// npm and electron-builder draw progress bars that can run past the 64 KiB
// a default bufio.Scanner accepts. A scanner that stopped there would
// leave the child blocked on a full pipe and the rebuild wedged with the
// Update button disabled forever.
func TestUpdateApplySurvivesAVeryLongBuildLine(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, scriptBuild(t,
		// One 100 KiB stderr line, then a normal one and the done phase.
		longLine(102400)+
			"echo 'installed' >&2\n"+
			`printf '{"phase":"done","path":"/home/u/.local/share/aether/desktop"}\n'`+"\n"), true)

	if got := applyForRebuild(t, g); !got.Rebuilding {
		t.Fatalf("apply = %+v, want a rebuild", got)
	}
	waitForExit(t, g)
	if g.ExitCode() != ExitRelaunch {
		t.Fatalf("exit code = %d, want %d", g.ExitCode(), ExitRelaunch)
	}
	final := status(t, g)
	if final.Phase != localops.PhaseDone {
		t.Fatalf("phase = %q, want %q", final.Phase, localops.PhaseDone)
	}
	joined := strings.Join(final.LinesTail, "\n")
	if !strings.Contains(joined, "installed") {
		t.Fatalf("lines_tail = %v, want the lines after the long one", final.LinesTail)
	}
	if len(final.LinesTail) == 0 || len(final.LinesTail[0]) < 100000 {
		t.Fatalf("the long line was dropped: first line is %d bytes", len(final.LinesTail[0]))
	}
}

// Past the cap the reader gives up on the line, but it must still drain
// the pipe: a child blocked writing into a pipe nobody reads would hang
// the rebuild exactly as the unbounded scanner did.
func TestUpdateApplyDrainsBuildOutputPastTheCap(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, scriptBuild(t,
		longLine(2<<20)+
			`printf '{"phase":"done","path":"/home/u/.local/share/aether/desktop"}\n'`+"\n"), true)

	if got := applyForRebuild(t, g); !got.Rebuilding {
		t.Fatalf("apply = %+v, want a rebuild", got)
	}
	waitForExit(t, g)
	if g.ExitCode() != ExitRelaunch {
		t.Fatalf("exit code = %d, want %d", g.ExitCode(), ExitRelaunch)
	}
	if got := strings.Join(status(t, g).LinesTail, "\n"); !strings.Contains(got, "build output not fully read") {
		t.Fatalf("lines_tail = %q, want the truncation note", got)
	}
}

// Quitting the app mid-rebuild must not leave a build running: it would
// still be downloading Node and still swapping the directory of an app the
// user just closed.
func TestClosingTheGatewayStopsTheRebuild(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, scriptBuild(t,
		`printf '{"phase":"packaging"}\n'`+"\nexec sleep 120\n"), true)

	if got := applyForRebuild(t, g); !got.Rebuilding {
		t.Fatalf("apply = %+v, want a rebuild", got)
	}
	awaitPhase(t, g, localops.PhasePackaging)

	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The killed child reports a failure, which is how the rebuild ends.
	awaitPhase(t, g, localops.PhaseError)
	waitForExit(t, g)
	if g.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0: no app was installed over", g.ExitCode())
	}
}

// A second apply - another tab, or the app beside a browser tab - must not
// exit a supervised gateway while the first build is still swapping the
// app directory. It says a rebuild is running and leaves it alone.
func TestSecondUpdateApplyDoesNotExitDuringARebuild(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	t.Cleanup(func() { _ = g.Close() })
	stubRebuild(t, scriptBuild(t,
		`printf '{"phase":"packaging"}\n'`+"\nexec sleep 120\n"), true)

	if got := applyForRebuild(t, g); !got.Rebuilding {
		t.Fatalf("first apply = %+v, want a rebuild", got)
	}
	awaitPhase(t, g, localops.PhasePackaging)

	second := applyForRebuild(t, g)
	if !second.Rebuilding {
		t.Fatalf("second apply = %+v, want it to report the running rebuild", second)
	}
	if !strings.Contains(second.Note, "already running") {
		t.Fatalf("note = %q, want it to say a rebuild is already running", second.Note)
	}
	select {
	case <-g.Exit():
		t.Fatal("the gateway exited while a rebuild was still running")
	case <-time.After(200 * time.Millisecond):
	}
}

// A rebuild that cannot even start leaves nothing for update.status to be
// polled for, so the reason has to reach the next gateway through the
// record on disk - or the stale-app banner has no explanation at all.
func TestUpdateApplyRecordsARebuildThatCannotStart(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubRebuild(t, fakeBuild(t, "", "", 0), true)
	rebuildArgv = func(string, localops.RealUser, bool) []string {
		t.Fatal("the account could not be resolved; nothing may be built")
		return nil
	}
	lookupRealUser = func() (localops.RealUser, error) {
		return localops.RealUser{}, errors.New("look up SUDO_USER \"ghost\": unknown user")
	}

	if got := applyForRebuild(t, g); got.Rebuilding {
		t.Fatalf("apply = %+v, want no rebuild", got)
	}
	if recorded := localops.LastDesktopBuildError(); !strings.Contains(recorded, "ghost") {
		t.Fatalf("recorded error = %q, want the lookup failure", recorded)
	}
}
