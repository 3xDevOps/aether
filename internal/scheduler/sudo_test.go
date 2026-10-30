package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type sudoRuntime struct {
	*fakeRuntime
	setup atomic.Bool
	mu    sync.Mutex
	execs []runtime.AttachedExec
}

func (r *sudoRuntime) SettingUp(runtime.ID) bool { return r.setup.Load() }

func (r *sudoRuntime) ExecAttached(_ context.Context, _ runtime.ID, spec runtime.AttachedExec) (int, error) {
	r.mu.Lock()
	r.execs = append(r.execs, spec)
	r.mu.Unlock()
	if _, err := io.WriteString(spec.Stdout, "ran\n"); err != nil {
		return 0, err
	}
	return 7, nil
}

func (r *sudoRuntime) ran() []runtime.AttachedExec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.execs)
}

func newSudoEnv(t *testing.T) (*testEnv, *sudoRuntime, *domain.Run) {
	t.Helper()
	var rt *sudoRuntime
	e := newTestEnv(t, func(cfg *Config) {
		rt = &sudoRuntime{fakeRuntime: cfg.Runtime.(*fakeRuntime)}
		cfg.Runtime = rt
	})
	run, _ := e.launchFake(t, "install jq")
	return e, rt, run
}

type sudoOutcome struct {
	code int
	err  error
	out  string
}

// startSudo runs the command the way the coordination socket does and
// returns once its request shows.
func startSudo(t *testing.T, e *testEnv, ctx context.Context, run domain.RunID, p protocol.RunSudoParams) (domain.RunInputRequest, <-chan sudoOutcome) {
	t.Helper()
	done := make(chan sudoOutcome, 1)
	go func() {
		var out bytes.Buffer
		code, err := e.sched.Sudo(ctx, run, p, []byte("piped input"), &out, io.Discard)
		done <- sudoOutcome{code, err, out.String()}
	}()
	var request domain.RunInputRequest
	waitFor(t, "the sudo request", func() bool {
		inputs := e.sched.PendingInputs(run)
		if len(inputs) == 1 {
			request = inputs[0]
		}
		return len(inputs) == 1
	})
	return request, done
}

func refusal(t *testing.T, err error) string {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return perr.Message
}

func TestSudoAsksAPersonAndRunsAsRootOnceAllowed(t *testing.T) {
	t.Parallel()
	e, rt, run := newSudoEnv(t)
	sub := e.subscribe(t)
	request, done := startSudo(t, e, t.Context(), run.ID, protocol.RunSudoParams{Argv: []string{"apt-get", "install", "-y", "jq"}, Dir: "/workspace/src"})
	if request.SessionID != domain.SudoSessionID || request.Kind != "permission" || !strings.HasPrefix(request.ID, domain.SudoRequestPrefix) ||
		request.Title != "Run as root in /workspace/src: apt-get install -y jq" {
		t.Fatalf("request = %+v", request)
	}
	if got, err := e.db.GetRun(t.Context(), run.ID); err != nil || got.Activity != domain.ActivityNeedsYou {
		t.Fatalf("run while asking = %+v, %v; want needs-you", got, err)
	}
	if len(rt.ran()) != 0 {
		t.Fatal("the command ran before anyone allowed it")
	}

	if err := e.sched.DecideSudo(t.Context(), run.ID, request.ID, true, e.member.ID); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.code != 7 || got.out != "ran\n" {
		t.Fatalf("Sudo = %+v", got)
	}
	execs := rt.ran()
	if len(execs) != 1 || execs[0].User != "0:0" || execs[0].WorkingDir != "/workspace/src" ||
		!slices.Equal(execs[0].Argv, []string{"apt-get", "install", "-y", "jq"}) || string(execs[0].Stdin) != "piped input" {
		t.Fatalf("execs = %+v", execs)
	}
	for {
		ev := waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
		if ev.Payload.(events.TimelinePayload).Message == "allowed sudo as root in /workspace/src: apt-get install -y jq" {
			if ev.ActorID != e.member.ID {
				t.Fatalf("timeline actor = %q, want %q", ev.ActorID, e.member.ID)
			}
			break
		}
	}
	if inputs := e.sched.PendingInputs(run.ID); len(inputs) != 0 {
		t.Fatalf("pending after the decision = %+v", inputs)
	}
	if err := e.sched.DecideSudo(t.Context(), run.ID, request.ID, false, e.member.ID); !errors.Is(err, ErrSudoNotPending) {
		t.Fatalf("second decision = %v, want ErrSudoNotPending", err)
	}
}

func TestSudoEndsWithoutRunningWhenDeniedUnansweredOrAbandoned(t *testing.T) {
	t.Parallel()
	e, rt, run := newSudoEnv(t)
	p := protocol.RunSudoParams{Argv: []string{"psql", "-c", "drop table users"}, User: "postgres", Dir: "/"}

	request, done := startSudo(t, e, t.Context(), run.ID, p)
	if request.Title != "Run as postgres in /: psql -c 'drop table users'" {
		t.Fatalf("title = %q", request.Title)
	}
	if err := e.sched.DecideSudo(t.Context(), run.ID, request.ID, false, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if got := <-done; refusal(t, got.err) != "Ada denied this command" {
		t.Fatalf("denied = %+v", got)
	}

	e.sched.sudoWait = 20 * time.Millisecond
	_, done = startSudo(t, e, t.Context(), run.ID, p)
	if got := <-done; refusal(t, got.err) != "nobody allowed this command within 10 minutes" {
		t.Fatalf("unanswered = %+v", got)
	}
	e.sched.sudoWait = time.Hour

	ctx, hangUp := context.WithCancel(t.Context())
	_, done = startSudo(t, e, ctx, run.ID, p)
	hangUp()
	if got := <-done; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("abandoned = %+v", got)
	}

	p.NonInteractive = true
	if _, err := e.sched.Sudo(t.Context(), run.ID, p, nil, io.Discard, io.Discard); refusal(t, err) != "a person must allow this command; -n cannot wait for one" {
		t.Fatalf("-n = %v", err)
	}
	if inputs := e.sched.PendingInputs(run.ID); len(inputs) != 0 {
		t.Fatalf("pending = %+v, want none", inputs)
	}
	if got, err := e.db.GetRun(t.Context(), run.ID); err != nil || got.Activity == domain.ActivityNeedsYou {
		t.Fatalf("run = %+v, %v; want it no longer waiting on anyone", got, err)
	}
	if len(rt.ran()) != 0 {
		t.Fatalf("execs = %+v, want none", rt.ran())
	}
}

func TestSudoRunsWithoutAskingWhileTheSetupScriptRuns(t *testing.T) {
	t.Parallel()
	e, rt, run := newSudoEnv(t)
	rt.setup.Store(true)
	var out bytes.Buffer
	code, err := e.sched.Sudo(t.Context(), run.ID, protocol.RunSudoParams{Argv: []string{"apt-get", "update"}, Dir: "/", NonInteractive: true}, nil, &out, io.Discard)
	if err != nil || code != 7 || out.String() != "ran\n" || len(rt.ran()) != 1 {
		t.Fatalf("Sudo = %d, %v, %q, execs %+v", code, err, out.String(), rt.ran())
	}
}

func TestSudoRefusesAnIsolatedRun(t *testing.T) {
	t.Parallel()
	e, rt, _ := newSudoEnv(t)
	isolated := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunRunning, Permission: domain.PermissionIsolated}
	if err := e.db.CreateRun(t.Context(), isolated); err != nil {
		t.Fatal(err)
	}
	_, err := e.sched.Sudo(t.Context(), isolated.ID, protocol.RunSudoParams{Argv: []string{"id"}, Dir: "/"}, nil, io.Discard, io.Discard)
	if refusal(t, err) != "an isolated run cannot run commands as root" || len(rt.ran()) != 0 || len(e.sched.PendingInputs(isolated.ID)) != 0 {
		t.Fatalf("Sudo = %v, execs %+v", err, rt.ran())
	}
}
