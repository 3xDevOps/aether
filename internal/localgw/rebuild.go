package localgw

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/localops"
)

// ExitRelaunch is the exit status a supervised gateway uses to tell the
// desktop shell that the app on disk changed under it: relaunch the shell
// instead of respawning the sidecar into the old window. 75 is EX_TEMPFAIL
// from sysexits.h; no other path out of `aether gui` uses it.
const ExitRelaunch = 75

// rebuildTailLines is how much of the build's own output update.status
// carries. Enough to show the failing npm or electron-builder line, small
// enough to poll once a second without thought.
const rebuildTailLines = 20

// Both indirected so a test can drive the rebuild with a stub script and a
// temporary install directory instead of a real Electron build.
var (
	rebuildArgv         = localops.RebuildAppArgv
	installedDesktopApp = localops.InstalledDesktopApp
)

// rebuildState tracks the desktop-app build update.apply started, for the
// update.status the dashboard banner polls.
type rebuildState struct {
	mu    sync.Mutex
	phase string
	err   string
	tail  []string
	// running guards against a second update.apply starting a second build
	// over the first one's build directory.
	running bool
}

// rebuildStatus is the update.status answer: which phase the build is on,
// the tail of its output, and the real error when it failed.
type rebuildStatus struct {
	Phase     string   `json:"phase"`
	LinesTail []string `json:"lines_tail,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// phaseIdle is the answer before any rebuild has run in this process. A
// gateway that just respawned after a rebuild reports it too: the build
// belonged to the process that exited.
const phaseIdle = "idle"

func newRebuildState() *rebuildState { return &rebuildState{phase: phaseIdle} }

func (s *rebuildState) snapshot() rebuildStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rebuildStatus{Phase: s.phase, LinesTail: append([]string(nil), s.tail...), Error: s.err}
}

func (s *rebuildState) setPhase(phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
}

func (s *rebuildState) fail(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = localops.PhaseError
	s.err = msg
}

func (s *rebuildState) addLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, line)
	if len(s.tail) > rebuildTailLines {
		s.tail = s.tail[len(s.tail)-rebuildTailLines:]
	}
}

// claim takes the one-build-at-a-time slot, false when a build is already
// running.
func (s *rebuildState) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	s.phase = localops.PhaseUnpacking
	s.err = ""
	s.tail = nil
	return true
}

func (s *rebuildState) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

// startAppRebuild rebuilds an installed desktop app with the CLI that
// update.apply just installed at bin, in the background. It reports
// whether a build started: a machine with no app installed builds nothing,
// which is what a browser-only or server install always answers.
//
// The build runs as a child of `<bin> gui build --json` rather than in
// this process, because that binary carries the shell sources the new app
// has to be built from - this one is the version being replaced.
func (g *Gateway) startAppRebuild(bin string) bool {
	who, err := localops.LookupRealUser()
	if err != nil {
		g.rebuild.fail("find the account to build for: " + err.Error())
		return false
	}
	if _, ok := installedDesktopApp(runtime.GOOS, who); !ok {
		return false
	}
	if !g.rebuild.claim() {
		return false
	}
	argv := rebuildArgv(bin, who, true)
	go func() {
		defer g.rebuild.release()
		err := g.runRebuild(argv)
		g.finishRebuild(err)
	}()
	return true
}

// runRebuild runs the build to completion, feeding update.status from its
// two streams: one JSON phase line per step on stdout, the build's own
// output on stderr.
func (g *Gateway) runRebuild(argv []string) error {
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The phase the build reported last. `gui build` reports its own
	// failure as an error phase carrying the real message, which beats the
	// child's exit status ("exit status 1") as an explanation.
	reported := ""
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		reported = g.readPhases(stdout)
	}()
	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(stderr)
		for scan.Scan() {
			g.rebuild.addLine(scan.Text())
		}
	}()
	wg.Wait()
	waitErr := cmd.Wait()
	if reported != "" {
		return errRebuild(reported)
	}
	return waitErr
}

// readPhases applies the build's JSON phase lines to update.status and
// returns the message from an error phase, empty when there was none.
func (g *Gateway) readPhases(r io.Reader) string {
	failure := ""
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		var event struct {
			Phase string `json:"phase"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil || event.Phase == "" {
			// Not a phase line. `gui build --json` keeps everything else
			// on stderr, so this is a stray warning worth keeping beside
			// the build output rather than dropping.
			g.rebuild.addLine(scan.Text())
			continue
		}
		if event.Phase == localops.PhaseError {
			failure = event.Error
			continue
		}
		g.rebuild.setPhase(event.Phase)
	}
	return failure
}

// errRebuild is the build's own error text as an error value.
type errRebuild string

func (e errRebuild) Error() string { return string(e) }

// finishRebuild records the outcome and, on a supervised gateway, ends the
// process the way the desktop shell needs: exit 75 says the app on disk is
// new, so relaunch the shell; a plain exit 0 leaves the shell respawning
// the sidecar, which is right after a failure because the CLI half of the
// update did land.
func (g *Gateway) finishRebuild(err error) {
	if err == nil {
		g.rebuild.setPhase(localops.PhaseDone)
		if g.cfg.Supervised {
			g.requestExit(ExitRelaunch)
		}
		return
	}
	msg := strings.TrimSpace(err.Error())
	g.rebuild.fail(msg)
	// The gateway that reports this to the dashboard is the next one, so
	// the failure has to outlive this process.
	if writeErr := localops.RecordDesktopBuildError(msg); writeErr != nil {
		g.rebuild.addLine("could not record the build failure: " + writeErr.Error())
	}
	if g.cfg.Supervised {
		g.requestExit(0)
	}
}
