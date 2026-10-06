package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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
