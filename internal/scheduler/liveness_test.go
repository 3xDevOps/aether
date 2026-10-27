package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestEnhancedTurnWithoutProgressParksAsAPossibleStall(t *testing.T) {
	t.Parallel()
	const grace = 300 * time.Millisecond
	for _, tc := range []struct {
		prompt, phase string
	}{
		{acpmock.PromptWait, "awaiting first event"},
		{acpmock.PromptChatter, "awaiting first event"},
		{acpmock.PromptGoQuiet, "streaming"},
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
			time.Sleep(10 * 20 * time.Millisecond)
			r, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != domain.RunNeedsAttention || r.StatusSeq != ev.Payload.(events.RunStatusPayload).StatusSeq {
				t.Fatalf("the possible stall did not hold: %+v", r)
			}
			if r.NeedsAttention(false) {
				t.Fatalf("a possible stall needs you: %+v", r)
			}
			l, ok := e.sched.Liveness(run.ID)
			if !ok || string(l.Phase) != strings.ReplaceAll(tc.phase, " ", "-") || l.Transport != domain.TransportOpen ||
				l.LastProgress.IsZero() != (tc.prompt != acpmock.PromptGoQuiet) {
				t.Fatalf("Liveness = %+v, %v; want %s, open", l, ok, tc.phase)
			}
		})
	}
}

// The session queues a turn's start notice behind its other callbacks, so on
// a loaded host it can reach the scheduler after a possible stall parked that
// same turn. It says nothing new about the turn and must not release it.
func TestPossibleStallHoldsAgainstItsTurnsLateStartNotice(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.FirstEventGrace = 300 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run := e.launchACP(t, acpmock.PromptWait)
	parked := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention).Payload.(events.RunStatusPayload)
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	turnAt := e.sched.acp.session(run.ID).State().TurnStartedAt
	e.sched.acp.turnState(entry, false, acphost.TurnState{Working: true, TurnStartedAt: turnAt})
	time.Sleep(10 * 20 * time.Millisecond)
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != domain.RunNeedsAttention || r.StatusSeq != parked.StatusSeq {
		t.Fatalf("the turn's late start notice released its possible stall: %+v", r)
	}
}

// A new turn is judged from its own start, not by the silence before it,
// even while its start notice is still queued.
func TestPossibleStallJudgesANewTurnFromItsStart(t *testing.T) {
	t.Parallel()
	const grace = 2 * time.Second
	e, _ := newACPEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.FirstEventGrace = grace
	})
	run := e.launchACP(t, "hello")
	working := func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].agentReport.State == agentstatus.Working
	}
	waitFor(t, "the first turn to end", func() bool { return !working() })
	time.Sleep(grace + grace/4)
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptWait}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second turn to start", working)
	// Undo what the start notice did, as if it were still queued.
	e.sched.mu.Lock()
	e.sched.runs[run.ID].lastWorking = time.Time{}
	e.sched.mu.Unlock()
	e.sched.checkStalls(t.Context())
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want running within the new turn's first-event grace", r, err)
	}
}

func TestReportReminderTurnKeepsTheStallThreshold(t *testing.T) {
	t.Parallel()
	const grace = 300 * time.Millisecond
	for _, tc := range []struct {
		name  string
		start func(current int64) int64
		parks bool
	}{
		{"the reminder's turn", func(current int64) int64 { return current }, false},
		{"a later turn", func(current int64) int64 { return current - 1 }, true},
		{"start unknown", func(int64) int64 { return 0 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _ := newACPEnv(t, func(cfg *Config) {
				cfg.StallThreshold = time.Hour
				cfg.FirstEventGrace, cfg.StreamQuietGrace = grace, grace
				cfg.PollInterval = 20 * time.Millisecond
			})
			run := e.launchACP(t, acpmock.PromptWait)
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			e.sched.acp.mu.Lock()
			r := e.sched.acp.runs[run.ID]
			r.asked = &askedTurn{start: tc.start(r.session.Log().TurnStartSeq())}
			e.sched.acp.mu.Unlock()
			e.startStalls(t)
			time.Sleep(4 * grace)
			got, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if parked := got.Status == domain.RunNeedsAttention; parked != tc.parks {
				t.Fatalf("run = %+v; parked %v, want %v past the first-event grace", got, parked, tc.parks)
			}
		})
	}
}

func TestEnhancedFailedFirstPromptShowsItsError(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptRefuse)
	waitFor(t, "the refused task to fail the turn", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Activity == domain.ActivityFailed &&
			strings.HasPrefix(r.Reason, acpTurnFailedReason) && strings.Contains(r.Reason, "Authentication required")
	})
}

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
	if l, ok := e.sched.Liveness(run.ID); !ok || l.Transport != domain.TransportClosed {
		t.Fatalf("Liveness = %+v, %v; want closed", l, ok)
	}
	e.sched.mu.Lock()
	e.sched.runs[run.ID].status = domain.RunProvisioning
	e.sched.mu.Unlock()
	if l, ok := e.sched.Liveness(run.ID); !ok || l.Phase != domain.PhaseSetup {
		t.Fatalf("Liveness = %+v, %v; want setup while provisioning", l, ok)
	}
}

func TestResumeRestartsTheStallClock(t *testing.T) {
	t.Parallel()
	const threshold = 400 * time.Millisecond
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = threshold
		cfg.PollInterval = 20 * time.Millisecond
	})
	run, _ := e.launchFake(t, "task")
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.startStalls(t)
	time.Sleep(2 * threshold)
	resumed := time.Now()
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	waitQuietFrom(t, e.sched, run.ID, resumed)
	waitStalledAfter(t, e, run.ID, resumed, threshold)
}

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
	restarted := time.Now()
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitQuietFrom(t, s2, run.ID, restarted)
	waitStalledAfter(t, e, run.ID, restarted, threshold)
}

// waitQuietFrom waits for s to supervise the run and fails unless its stall
// clock starts no earlier than from. Unlike the stall's timing, this holds
// however slowly s starts.
func waitQuietFrom(t *testing.T, s *Scheduler, id domain.RunID, from time.Time) {
	t.Helper()
	var quiet time.Time
	waitFor(t, "the scheduler to supervise the run", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry := s.runs[id]
		if entry != nil {
			quiet = entry.quietFrom
		}
		return entry != nil
	})
	if quiet.Before(from) {
		t.Fatalf("the stall clock starts at %s, before %s", quiet, from)
	}
}

// waitStalledAfter waits for the run to park as stalled and fails if it
// parked sooner than threshold after from: a clock that did not restart at
// from would park it on the first poll, as the run was already idle longer.
// It bounds the park from below only, so a slow machine cannot fail it.
func waitStalledAfter(t *testing.T, e *testEnv, id domain.RunID, from time.Time, threshold time.Duration) {
	t.Helper()
	var r *domain.Run
	waitFor(t, "the run to stall", func() bool {
		var err error
		r, err = e.db.GetRun(t.Context(), id)
		return err == nil && strings.HasPrefix(r.Reason, "stalled: ")
	})
	if r.StatusChangedAt == nil || r.StatusChangedAt.Sub(from) < threshold {
		t.Fatalf("run = %+v; parked sooner than %s after the stall clock restarted at %s", r, threshold, from)
	}
}
