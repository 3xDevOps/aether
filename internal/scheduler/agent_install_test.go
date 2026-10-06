package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestInstallAgentHoldsTheGuardPastADroppedRequest(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	started, release := make(chan struct{}), make(chan struct{})
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if argv[len(argv)-1] == "install-it" {
			close(started)
			<-release
		}
		return 0, "installed\n", nil
	}
	request, drop := context.WithCancel(t.Context())
	type result struct {
		tail string
		err  error
	}
	first := make(chan result, 1)
	go func() {
		tail, _, err := e.sched.InstallAgent(request, e.member.ID, "install-it")
		first <- result{tail, err}
	}()
	<-started
	drop()

	if _, _, err := e.sched.InstallAgent(t.Context(), e.member.ID, "install-again"); !errors.Is(err, ErrAgentInstallRunning) {
		t.Fatalf("second install while the first runs: error = %v, want %v", err, ErrAgentInstallRunning)
	}
	close(release)
	got := <-first
	if got.err != nil || got.tail != "installed" {
		t.Fatalf("first install = (%q, %v), want its output after the request dropped", got.tail, got.err)
	}
	if _, _, err := e.sched.InstallAgent(t.Context(), e.member.ID, "install-again"); err != nil {
		t.Fatalf("install after the first finished: %v", err)
	}
}

func installHomeEnv(t *testing.T) (e *testEnv, rt *updateRuntime, installStarted, releaseInstall chan struct{}) {
	t.Helper()
	e, rt, _ = newUpdateEnv(t, func(cfg *Config) { cfg.harnessUpdateWait = 50 * time.Millisecond })
	installInHome(t, e, "claude")
	installStarted, releaseInstall = make(chan struct{}, 1), make(chan struct{})
	update := rt.execHandler
	rt.execHandler = func(id runtime.ID, argv []string) (int, string, error) {
		switch {
		case argv[len(argv)-1] == "install-it":
			installStarted <- struct{}{}
			<-releaseInstall
			return 0, "installed\n", nil
		case len(argv) == 3 && argv[0] == "/bin/sh", len(argv) == 2 && argv[1] == "--version":
			return update(id, argv)
		}
		return 0, "", nil
	}
	return e, rt, installStarted, releaseInstall
}

func TestInstallAgentWaitsForTheHarnessUpdateInItsHome(t *testing.T) {
	t.Parallel()
	e, rt, installStarted, releaseInstall := installHomeEnv(t)
	close(releaseInstall)
	rt.release = make(chan struct{})
	launch := launchAsync(t.Context(), e)
	<-rt.started

	installed := make(chan error, 1)
	go func() {
		_, _, err := e.sched.InstallAgent(t.Context(), e.member.ID, "install-it")
		installed <- err
	}()
	select {
	case <-installStarted:
		t.Fatal("agent install ran while the harness update wrote into the same home")
	case <-time.After(200 * time.Millisecond):
	}
	rt.releaseUpdate()
	if err := <-installed; err != nil {
		t.Fatalf("install after the update finished: %v", err)
	}
	if got := awaitLaunch(t, launch); got.err != nil {
		t.Fatalf("Launch: %v", got.err)
	}
}

func TestHarnessUpdateSkipsAHomeAnAgentInstallHolds(t *testing.T) {
	t.Parallel()
	e, rt, installStarted, releaseInstall := installHomeEnv(t)
	installed := make(chan error, 1)
	go func() {
		_, _, err := e.sched.InstallAgent(t.Context(), e.member.ID, "install-it")
		installed <- err
	}()
	<-installStarted

	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 0 {
		t.Fatalf("update containers while an agent install held the home = %d, want 0", n)
	}
	close(releaseInstall)
	if err := <-installed; err != nil {
		t.Fatalf("install: %v", err)
	}
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 1 {
		t.Fatalf("update containers after the install finished = %d, want 1", n)
	}
}

func TestLogTailStartsAtALineAndIsValidUTF8(t *testing.T) {
	t.Parallel()
	whole := strings.Repeat("added 1 package\n", maxInstallLog/16+1)
	cut := "\x98\x80 partial line\n" + whole[:maxInstallLog-len("\x98\x80 partial line\n")]
	if got := logTail(cut); !strings.HasPrefix(got, "added 1 package\n") {
		t.Errorf("logTail kept the partial first line: %q", got[:32])
	}
	oneLine := "\x98\x80" + strings.Repeat("x", maxInstallLog-2)
	if got := logTail(oneLine); !utf8.ValidString(got) || len(got) != maxInstallLog-2 {
		t.Errorf("logTail of one cut line = %d bytes, valid %v; want the line without the broken character", len(got), utf8.ValidString(got))
	}
	short := "\x98\x80partial\nnpm error code E404\n"
	if got := logTail(short); got != "partial\nnpm error code E404\n" {
		t.Errorf("logTail of a whole log = %q, want every line kept", got)
	}
}
