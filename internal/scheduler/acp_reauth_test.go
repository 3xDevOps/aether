package scheduler

import (
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// An agent that lost its sign-in recovers by itself: the restarted agent
// reads the login again and answers the message sent after the refusal.
func TestEnhancedAuthRefusalRestartsTheAgentAndResendsTheTurn(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	rt.fixture.RefuseNext = new(atomic.Int32)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	before := len(rt.all())

	rt.fixture.RefuseNext.Store(1)
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err == nil {
		t.Fatal("Inject of a refused prompt succeeded")
	}
	waitItems(t, e.sched, run.ID, "the sign-in notice", noticeTitled("Agent signed out"))
	waitFor(t, "the restarted agent", func() bool { return len(rt.all()) == before+1 && e.sched.acp.session(run.ID) != nil })
	waitFor(t, "the refusal shown after the restart", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Status == domain.RunNeedsAttention && strings.Contains(row.Reason, "send the message again")
	})
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatalf("message after the restart: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the answered turn", assistantSaid("pong"))
	// The answered turn ends without a report, so the run may already wait
	// on the member; what matters is that the refusal no longer shows.
	waitFor(t, "the refusal cleared", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && !strings.HasPrefix(row.Reason, acpTurnFailedReason)
	})
}

// When the restarted agent still finds no sign-in, the failure shows with
// the agent's error and the agent is not restarted again.
func TestEnhancedAuthRefusalAfterARestartShowsTheFailure(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	rt.fixture.RefusePrompts = true
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	before := len(rt.all())

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "first"}, false, nil); err == nil {
		t.Fatal("Inject of a refused prompt succeeded")
	}
	waitFor(t, "the restarted agent", func() bool { return len(rt.all()) == before+1 && e.sched.acp.session(run.ID) != nil })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "second"}, false, nil); err == nil {
		t.Fatal("Inject of a prompt refused again succeeded")
	}
	waitFor(t, "the failure shown", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Status == domain.RunNeedsAttention &&
			strings.HasPrefix(row.Reason, acpTurnFailedReason) && strings.Contains(row.Reason, "Authentication required") &&
			strings.Contains(row.Reason, "start a new session")
	})
	time.Sleep(200 * time.Millisecond)
	if n := len(rt.all()); n != before+1 {
		t.Fatalf("agent processes = %d, want exactly one restart after %d", n, before)
	}
}

// A root command in another session that takes the shared home leaves the
// agent unable to read its login; the restart gives the home back and says
// what it found.
func TestEnhancedAuthRefusalRestoresATakenHome(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	rt.fixture.RefuseNext = new(atomic.Int32)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	home, err := e.sched.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	// Only root can take the home as a container's root does.
	if os.Geteuid() == 0 {
		if err = os.Lchown(home, 0, 0); err != nil {
			t.Fatal(err)
		}
	}

	rt.fixture.RefuseNext.Store(1)
	if _, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err == nil {
		t.Fatal("Inject of a refused prompt succeeded")
	}
	waitItems(t, e.sched, run.ID, "the restore notice", noticeTitled("Home restored"))
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("home mode = %04o, want 0700", got)
	}
	got, want := info.Sys().(*syscall.Stat_t), before.Sys().(*syscall.Stat_t)
	if got.Uid != want.Uid || got.Gid != want.Gid {
		t.Fatalf("home owner = %d:%d, want %d:%d", got.Uid, got.Gid, want.Uid, want.Gid)
	}
	waitFor(t, "the restarted agent", func() bool { return e.sched.acp.session(run.ID) != nil })
	if _, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatalf("message after the restart: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the answered turn", assistantSaid("pong"))
}
