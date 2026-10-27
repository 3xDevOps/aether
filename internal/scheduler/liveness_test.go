package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// TestEnhancedTurnWithoutProgressParksAsAPossibleStall: a turn the agent
// accepted and then went silent on, or answered only with usage and command
// updates, parks after the first-event grace as a stall: waiting, never
// disconnected or needing a person. A streaming reply or a running tool is
// not flagged at the short graces.
func TestEnhancedTurnWithoutProgressParksAsAPossibleStall(t *testing.T) {
	t.Parallel()
	const grace = 300 * time.Millisecond
	for _, tc := range []struct {
		prompt, phase string
	}{
		{acpmock.PromptWait, "awaiting first event"},
		{acpmock.PromptChatter, "awaiting first event"},
		{acpmock.PromptThink, ""},
		{acpmock.PromptLongTool, ""},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			t.Parallel()
			e, _ := newACPEnv(t, func(cfg *Config) {
				cfg.StallThreshold = time.Hour
				cfg.FirstEventGrace, cfg.StreamQuietGrace = grace, grace
				cfg.PollInterval = 20 * time.Millisecond
			})
			sub := e.subscribe(t)
			e.startStalls(t)
			run := e.launchACP(t, tc.prompt)
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			if tc.phase == "" {
				time.Sleep(4 * grace)
				r, err := e.db.GetRun(t.Context(), run.ID)
				if err != nil || r.Status != domain.RunRunning {
					t.Fatalf("run = %+v, %v; want running past the short graces", r, err)
				}
				return
			}
			ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
			p := ev.Payload.(events.RunStatusPayload)
			want := ", " + tc.phase + ", transport open"
			if !strings.HasPrefix(p.Reason, "stalled: possibly — no progress for ") || !strings.HasSuffix(p.Reason, want) ||
				p.Activity != domain.ActivityWaiting || p.Cause != events.CauseStall {
				t.Fatalf("parked with %q, %q by %q; want a possible stall ending %q, waiting by stall", p.Reason, p.Activity, p.Cause, want)
			}
			r, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.NeedsAttention(false) {
				t.Fatalf("a possible stall needs you: %+v", r)
			}
			l, ok := e.sched.Liveness(run.ID)
			if !ok || l.Phase != domain.PhaseFirstEvent || l.Transport != domain.TransportOpen || !l.LastProgress.IsZero() {
				t.Fatalf("Liveness = %+v, %v; want awaiting first event, open, no progress", l, ok)
			}
		})
	}
}

// TestReportReminderTurnKeepsTheStallThreshold: the turn the report gate
// starts to ask for a missing report is not judged as a new turn of work.
func TestReportReminderTurnKeepsTheStallThreshold(t *testing.T) {
	t.Parallel()
	const grace = 300 * time.Millisecond
	e, _ := newACPEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.FirstEventGrace, cfg.StreamQuietGrace = grace, grace
		cfg.PollInterval = 20 * time.Millisecond
	})
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	e.sched.acp.mu.Lock()
	e.sched.acp.runs[run.ID].asked = &askedTurn{}
	e.sched.acp.mu.Unlock()
	e.startStalls(t)
	time.Sleep(4 * grace)
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want the reminder turn running past the first-event grace", r, err)
	}
}

// TestEnhancedAdapterExitIsDisconnected: losing the agent process is the
// one thing that reads disconnected.
func TestEnhancedAdapterExitIsDisconnected(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.FirstEventGrace = time.Hour
		cfg.PollInterval = 20 * time.Millisecond
	})
	e.startStalls(t)
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	execs := rt.all()
	if _, err := execs[len(execs)-1].Stop(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to read disconnected", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Activity == domain.ActivityDisconnected
	})
}

// TestResumeRestartsTheStallClock: a run paused for longer than the stall
// threshold does not park on the first poll after it resumes.
func TestResumeRestartsTheStallClock(t *testing.T) {
	t.Parallel()
	const threshold = 400 * time.Millisecond
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = threshold
		cfg.PollInterval = 20 * time.Millisecond
	})
	e.startStalls(t)
	run, _ := e.launchFake(t, "task")
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * threshold)
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(threshold / 2)
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want running right after the resume", r, err)
	}
	waitFor(t, "the resumed run to stall", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && strings.HasPrefix(r.Reason, "stalled: ")
	})
}

// TestRestartRestartsTheStallClock: a server restart reattaching a run that
// started long ago does not park it on the first poll.
func TestRestartRestartsTheStallClock(t *testing.T) {
	t.Parallel()
	const threshold = 400 * time.Millisecond
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = threshold
		cfg.PollInterval = 20 * time.Millisecond
	})
	run, _ := e.launchFake(t, "task")
	time.Sleep(2 * threshold)
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	time.Sleep(threshold / 2)
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want running right after the restart", r, err)
	}
	waitFor(t, "the reattached run to stall", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && strings.HasPrefix(r.Reason, "stalled: ")
	})
}
