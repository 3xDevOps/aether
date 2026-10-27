package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeWaits stands in for the run-wait condition store.
type fakeWaits struct {
	mu         sync.Mutex
	conditions []WaitCondition
	err        error
}

func (f *fakeWaits) RunWaits(context.Context, domain.RunID) ([]WaitCondition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WaitCondition(nil), f.conditions...), f.err
}

func (f *fakeWaits) set(err error, conditions ...WaitCondition) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conditions, f.err = conditions, err
}

const checksSpec = "pr:3xDevOps/Aether#582:checks"

// checking is a pending condition read just now on a one-minute poll.
func checking(now time.Time) WaitCondition {
	return WaitCondition{Spec: checksSpec, State: waitPending, Summary: "open, 3 checks running",
		LastCheckedAt: now, SourceCheckedAt: now, NextCheckAt: now.Add(time.Minute)}
}

func TestClassifyWaitByItsConditions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	fresh := checking(now.Add(-30 * time.Second))
	stale := checking(now.Add(-5 * time.Minute))
	failing := checking(now.Add(-20 * time.Second))
	failing.SourceCheckedAt = now.Add(-10 * time.Minute)
	failing.ConsecutiveFailures, failing.LastError = 3, "gh: API rate limit exceeded"
	never := WaitCondition{Spec: checksSpec, State: waitPending, LastError: "gh: Could not resolve to a PullRequest"}
	resolved := func(state string, ago time.Duration) WaitCondition {
		c := checking(now.Add(-ago))
		c.State, c.Summary, c.ResolvedAt = state, "merged", now.Add(-ago)
		return c
	}
	errored := resolved("error", time.Minute)
	errored.LastError = "gh: pull request 582 not found"
	lost := resolved("error", 10*time.Minute)
	lost.LastError = "gh: pull request 582 not found"
	undelivered := resolved(waitResolved, 10*time.Minute)
	undelivered.LastDeliveryError = "coord: inbox write: disk full"
	delivered := resolved(waitResolved, 10*time.Minute)
	delivered.DeliveredAt = now.Add(-9 * time.Minute)
	expired := resolved("expired", 10*time.Minute)
	expired.DeliveredAt = now.Add(-9 * time.Minute)
	cancelled := fresh
	unread := failing
	unread.SourceCheckedAt = now.Add(-61 * time.Minute)
	neverOld := WaitCondition{Spec: checksSpec, State: waitPending, CreatedAt: now.Add(-61 * time.Minute)}
	never.CreatedAt = now.Add(-time.Minute)
	cancelled.State = waitCancelled

	cases := []struct {
		name       string
		conditions []WaitCondition
		kind       domain.WaitEvidence
		detail     string
		err        string
		stranded   string
		verified   bool
	}{
		{name: "no condition", kind: domain.WaitUnverified},
		{name: "only cancelled", conditions: []WaitCondition{cancelled}, kind: domain.WaitUnverified},
		{name: "fresh", conditions: []WaitCondition{fresh}, kind: domain.WaitCondition, detail: checksSpec + ": open, 3 checks running", verified: true},
		{name: "stale read claims nothing live", conditions: []WaitCondition{stale}, kind: domain.WaitSourceUnknown,
			detail: checksSpec + ": status unknown", verified: true},
		{name: "failing read", conditions: []WaitCondition{failing}, kind: domain.WaitSourceUnknown,
			detail: checksSpec + ": status unknown", err: "gh: API rate limit exceeded", verified: true},
		{name: "never read", conditions: []WaitCondition{never}, kind: domain.WaitSourceUnknown,
			detail: checksSpec + ": status never read", err: "gh: Could not resolve to a PullRequest"},
		{name: "fresh and stale", conditions: []WaitCondition{fresh, stale}, kind: domain.WaitSourceUnknown,
			detail: checksSpec + ": open, 3 checks running; " + checksSpec + ": status unknown", verified: true},
		{name: "resolved, waking", conditions: []WaitCondition{resolved(waitResolved, time.Minute)}, kind: domain.WaitCondition,
			detail: checksSpec + ": resolved, waking the agent (merged)", verified: true},
		{name: "errored, waking", conditions: []WaitCondition{errored}, kind: domain.WaitFault,
			detail: checksSpec + ": error, waking the agent (merged)", err: "gh: pull request 582 not found", verified: true},
		{name: "delivered", conditions: []WaitCondition{delivered}, kind: domain.WaitCondition,
			detail: checksSpec + ": resolved, woke the agent (merged)", verified: true},
		{name: "expired and told", conditions: []WaitCondition{expired}, kind: domain.WaitFault,
			detail: checksSpec + ": expired, woke the agent (merged)", verified: true},
		{name: "wake undelivered", conditions: []WaitCondition{undelivered, fresh}, kind: domain.WaitFault,
			detail:   checksSpec + " resolved, but the agent was not woken (merged); " + checksSpec + ": open, 3 checks running",
			err:      "coord: inbox write: disk full",
			stranded: checksSpec + " resolved, but the agent was not woken", verified: true},
		{name: "wake undelivered behind a failing read", conditions: []WaitCondition{failing, undelivered}, kind: domain.WaitFault,
			detail:   checksSpec + ": status unknown; " + checksSpec + " resolved, but the agent was not woken (merged)",
			err:      "coord: inbox write: disk full",
			stranded: checksSpec + " resolved, but the agent was not woken", verified: true},
		{name: "target lost, never told", conditions: []WaitCondition{lost}, kind: domain.WaitFault,
			detail: checksSpec + " errored, but the agent was not woken (merged)", err: "gh: pull request 582 not found",
			stranded: checksSpec + " errored, but the agent was not woken", verified: true},
		{name: "unread for an hour", conditions: []WaitCondition{unread}, kind: domain.WaitFault,
			detail: "cannot read " + checksSpec + " for over 1h, so the agent will not be woken", err: "gh: API rate limit exceeded",
			stranded: "cannot read " + checksSpec + " for over 1h, so the agent will not be woken", verified: true},
		{name: "never read for an hour", conditions: []WaitCondition{neverOld}, kind: domain.WaitFault,
			detail:   "cannot read " + checksSpec + " for over 1h, so the agent will not be woken",
			stranded: "cannot read " + checksSpec + " for over 1h, so the agent will not be woken"},
	}
	for _, tc := range cases {
		wait, stranded := classifyWait(tc.conditions, time.Time{}, now)
		if wait.Kind != tc.kind || wait.Detail != tc.detail || wait.Error != tc.err || stranded != tc.stranded || (wait.VerifiedAt != nil) != tc.verified {
			t.Errorf("%s: got %+v stranded %q, want %s %q error %q stranded %q verified %v", tc.name, wait, stranded, tc.kind, tc.detail, tc.err, tc.stranded, tc.verified)
		}
	}
}

// A condition that left pending before the current turn started was acted
// on, by the agent or by the member who messaged it instead, so it neither
// backs nor breaks a wait reported since.
func TestAnEarlierTurnsConditionBacksNoWait(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	turn := now.Add(-20 * time.Minute)
	woke := checking(now.Add(-30 * time.Minute))
	woke.State, woke.ResolvedAt, woke.DeliveredAt = waitResolved, now.Add(-25*time.Minute), now.Add(-24*time.Minute)
	stranded := checking(now.Add(-30 * time.Minute))
	stranded.State, stranded.ResolvedAt = waitResolved, now.Add(-25*time.Minute)
	for name, conditions := range map[string][]WaitCondition{"woke the agent": {woke}, "never delivered": {stranded}} {
		if wait, broken := classifyWait(conditions, turn, now); wait.Kind != domain.WaitUnverified || broken != "" {
			t.Errorf("%s before the turn: got %+v stranded %q, want unverified", name, wait, broken)
		}
	}
	pending := checking(now.Add(-30 * time.Second))
	pending.CreatedAt = now.Add(-time.Hour)
	if wait, _ := classifyWait([]WaitCondition{pending, stranded}, turn, now); wait.Kind != domain.WaitCondition {
		t.Errorf("a condition still pending from an earlier turn: got %+v, want condition", wait)
	}
}

func TestAConditionDecidesWhatAReportedWaitShows(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seq := row.StatusSeq
	sub := e.subscribe(t)
	expect := func(what string, activity domain.RunActivity, cause events.RunStatusCause, reason string, kind domain.WaitEvidence) events.RunStatusPayload {
		t.Helper()
		p := nextActivity(t, sub, run.ID, &seq)
		got := domain.WaitEvidence("")
		if p.Wait != nil {
			got = p.Wait.Kind
		}
		if p.Activity != activity || p.Cause != cause || p.Reason != reason || got != kind {
			t.Fatalf("%s: published %s by %s %q wait %q, want %s by %s %q wait %q", what, p.Activity, p.Cause, p.Reason, got, activity, cause, reason, kind)
		}
		stored := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
		if stored.NeedsAttention(false) != (activity == domain.ActivityNeedsYou) {
			t.Fatalf("%s: stored %s %q needs attention = %v", what, stored.Activity, stored.Reason, stored.NeedsAttention(false))
		}
		return p
	}

	waits.set(nil, checking(time.Now()))
	if err = e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on #582", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	p := expect("waiting for CI with a checked condition", domain.ActivityWaitingCI, events.CauseReport, "CI on #582", domain.WaitCondition)
	if p.Wait.VerifiedAt == nil || !strings.Contains(p.Wait.Text(time.Now()), "open, 3 checks running (checked just now)") {
		t.Fatalf("wait %+v reads %q", p.Wait, p.Wait.Text(time.Now()))
	}

	quietFor(t, e)
	e.sched.refreshActivities(ctx)
	expectNoStatusEvent(t, sub, run.ID, "a checked CI wait past three stall thresholds")
	if got := e.sched.Wait(run.ID, domain.ActivityWaitingCI); got == nil || got.Kind != domain.WaitCondition {
		t.Fatalf("Wait = %+v after the quiet spell, want condition", got)
	}

	stale := checking(time.Now().Add(-10 * time.Minute))
	stale.LastError = "gh: API rate limit exceeded"
	waits.set(nil, stale)
	if err = e.sched.WaitChanged(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	p = expect("the source unreadable", domain.ActivityWaitingCI, events.CauseReport, "CI on #582", domain.WaitSourceUnknown)
	if text := p.Wait.Text(time.Now()); strings.Contains(text, "running") || !strings.HasSuffix(text, ": gh: API rate limit exceeded") {
		t.Fatalf("unknown source reads %q", text)
	}
	quietFor(t, e)
	expectNoStatusEvent(t, sub, run.ID, "an unknown source past three stall thresholds")

	stuck := checking(time.Now().Add(-10 * time.Minute))
	stuck.State, stuck.ResolvedAt, stuck.LastDeliveryError = waitResolved, time.Now().Add(-6*time.Minute), "coord: inbox write: disk full"
	waits.set(nil, stuck)
	e.sched.refreshActivities(ctx)
	broken := waitBrokenReasonPrefix + checksSpec + " resolved, but the agent was not woken"
	p = expect("the wake undelivered past its grace", domain.ActivityNeedsYou, events.CauseWaitFault, broken, domain.WaitFault)
	if p.Wait.Error != "coord: inbox write: disk full" {
		t.Fatalf("fault error = %q", p.Wait.Error)
	}
	e.sched.refreshActivities(ctx)
	expectNoStatusEvent(t, sub, run.ID, "refreshing a broken wait")

	reboot := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, reboot)
	waitFor(t, "the broken wait recovered", func() bool {
		return reboot.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault &&
			reboot.Wait(run.ID, domain.ActivityNeedsYou) != nil
	})
	// Recovery may publish the state it re-derived; skip what it wrote.
	row, err = e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for drained := false; !drained; {
		select {
		case <-sub.Events():
		case <-time.After(100 * time.Millisecond):
			drained = true
		}
	}
	seq = row.StatusSeq

	stuck.DeliveredAt = time.Now()
	waits.set(nil, stuck)
	if err = reboot.WaitChanged(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	expect("the wake delivered", domain.ActivityWaitingCI, events.CauseReport, "CI on #582", domain.WaitCondition)
}

// The reason a broken wait shows names its stranded conditions in one
// order, whatever order the store returns them in, so one need is
// announced once.
func TestAStrandedReasonIgnoresTheStoresOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	lost := checking(now.Add(-10 * time.Minute))
	lost.Spec, lost.State, lost.ResolvedAt = "pr:o/b#2:checks", waitResolved, now.Add(-10*time.Minute)
	unread := checking(now.Add(-2 * time.Hour))
	unread.Spec, unread.LastError = "pr:o/a#1:checks", "gh: API rate limit exceeded"
	_, forward := classifyWait([]WaitCondition{lost, unread}, time.Time{}, now)
	_, backward := classifyWait([]WaitCondition{unread, lost}, time.Time{}, now)
	if forward == "" || forward != backward {
		t.Fatalf("stranded reads %q one way and %q the other", forward, backward)
	}
}

// A stranded condition does not need a member while a live swarm can still
// wake the agent: the wait shows its fault quietly, and needs a member only
// once the swarm ends.
func TestALiveSwarmKeepsAStrandedWaitQuiet(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	var integrator *integratorStore
	e := newReportingEnv(t, func(cfg *Config) {
		integrator = &integratorStore{Store: cfg.Store}
		cfg.Store = integrator
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	integrator.workers.Store(1)
	stuck := checking(time.Now().Add(-time.Hour))
	stuck.State, stuck.ResolvedAt = waitResolved, time.Now().Add(-time.Hour)
	waits.set(nil, stuck)
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-agents", "", "two workers", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the quiet fault", func() bool {
		got := e.sched.Wait(run.ID, domain.ActivityWaitingAgents)
		return got != nil && got.Kind == domain.WaitFault
	})
	e.sched.refreshActivities(ctx)
	if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); row.NeedsAttention(false) {
		t.Fatalf("a stranded wait with a live swarm shows %s %q", row.Activity, row.Reason)
	}

	integrator.workers.Store(0)
	e.sched.refreshActivities(ctx)
	waitFor(t, "the broken wait", func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })
}

// One failed read of the condition store long after the wait was reported
// does not break it: the hour runs from the first failure of a streak, and
// a read that succeeds starts it over.
func TestAConditionStoreBreaksTheWaitOnlyAfterAnHourOfFailures(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	waits.set(nil, checking(time.Now()))
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on #582", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	// age moves the start of the failing streak back by d.
	age := func(d time.Duration) {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		entry := e.sched.runs[run.ID]
		entry.waitsFailingSince = entry.waitsFailingSince.Add(-d)
	}
	judged := func(what string, kind domain.WaitEvidence, broken bool) {
		t.Helper()
		e.sched.refreshActivities(ctx)
		if broken {
			waitFor(t, what, func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })
			return
		}
		if got := e.sched.Wait(run.ID, domain.ActivityWaitingCI); got == nil || got.Kind != kind {
			t.Fatalf("%s: Wait = %+v, want %s and quiet", what, got, kind)
		}
	}
	judged("a checked wait", domain.WaitCondition, false)

	waits.set(errors.New("database is locked"))
	judged("the first failed read, two hours after the report", domain.WaitSourceUnknown, false)
	age(59 * time.Minute)
	judged("59 minutes of failures", domain.WaitSourceUnknown, false)

	waits.set(nil, checking(time.Now()))
	judged("a read that succeeds", domain.WaitCondition, false)
	waits.set(errors.New("database is locked"))
	judged("a new failure", domain.WaitSourceUnknown, false)
	age(2 * time.Minute)
	judged("two minutes into the new streak", domain.WaitSourceUnknown, false)

	age(time.Hour)
	judged("an hour of failures", domain.WaitFault, true)
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if want := waitBrokenReasonPrefix + "cannot read this run's wait conditions for over 1h, so the agent will not be woken"; row.Reason != want {
		t.Fatalf("reason = %q, want %q", row.Reason, want)
	}
	if got := e.sched.Wait(run.ID, domain.ActivityNeedsYou); got == nil || got.Kind != domain.WaitFault || !strings.Contains(got.Error, "database is locked") {
		t.Fatalf("Wait = %+v, want a fault with the store's error", got)
	}
}

// Background tasks the run holds can still wake the agent, so a stranded
// wait stays quiet while they run and needs a member once they end.
func TestHeldTasksKeepAStrandedWaitQuiet(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-agents", "", "two subagents", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	// hold sets the background tasks the parked run holds, as an Enhanced
	// session between turns reports them.
	hold := func(report agentstatus.Report) {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		e.sched.runs[run.ID].agentReport = report
	}
	hold(agentstatus.Report{State: agentstatus.Working, Reason: agentstatus.BackgroundReason(2)})
	stuck := checking(time.Now().Add(-time.Hour))
	stuck.State, stuck.ResolvedAt = waitResolved, time.Now().Add(-time.Hour)
	waits.set(nil, stuck)
	e.sched.refreshActivities(ctx)
	if got := e.sched.Wait(run.ID, domain.ActivityWaitingAgents); got == nil || got.Kind != domain.WaitFault {
		t.Fatalf("Wait = %+v while tasks are held, want a quiet fault", got)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); row.NeedsAttention(false) {
		t.Fatalf("a stranded wait with held tasks shows %s %q", row.Activity, row.Reason)
	}

	hold(agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle})
	e.sched.refreshActivities(ctx)
	waitFor(t, "the broken wait", func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })
}

// A checked wait's verified time reaches subscribers as its source is read
// again, at most once a minute, without moving the run's standing.
func TestANewerSourceReadIsPublished(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	start := time.Now()
	waits.set(nil, checking(start.Add(-110*time.Second)))
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on #582", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the checked wait", func() bool { return e.sched.Wait(run.ID, domain.ActivityWaitingCI) != nil })
	before := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	seq := before.StatusSeq
	sub := e.subscribe(t)

	for _, ago := range []time.Duration{100 * time.Second, 90 * time.Second} {
		waits.set(nil, checking(start.Add(-ago)))
		e.sched.refreshActivities(ctx)
	}
	expectNoStatusEvent(t, sub, run.ID, "reads within a minute of the shown one")

	waits.set(nil, checking(start))
	e.sched.refreshActivities(ctx)
	p := nextActivity(t, sub, run.ID, &seq)
	if p.Wait == nil || p.Wait.VerifiedAt == nil || !p.Wait.VerifiedAt.Equal(start) || p.Reason != "CI on #582" {
		t.Fatalf("published %q wait %+v, want the read at %s", p.Reason, p.Wait, start)
	}
	after := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if after.NeedKey() != before.NeedKey() {
		t.Fatalf("NeedKey moved from %q to %q", before.NeedKey(), after.NeedKey())
	}
}

// A member's message is the remedy a broken wait asks for: the turn it
// starts leaves the stranded condition behind, so the next wait the agent
// reports is judged without it.
func TestAMessageClearsABrokenWait(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	stuck := checking(time.Now().Add(-time.Hour))
	stuck.State, stuck.ResolvedAt = waitResolved, time.Now().Add(-time.Hour)
	waits.set(nil, stuck)
	wait := func() {
		t.Helper()
		if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci-"+time.Now().String(), protocol.CoordWaitingForCI, "CI on #582", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
			t.Fatal(err)
		}
	}
	wait()
	waitFor(t, "the broken wait", func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })

	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working, TurnStart: true}); err != nil {
		t.Fatal(err)
	}
	wait()
	waitFor(t, "the next wait", func() bool {
		got := e.sched.Wait(run.ID, domain.ActivityWaitingCI)
		return got != nil && got.Kind == domain.WaitUnverified
	})
}

// A wait with no condition keeps today's expiry; one whose conditions
// cannot be read never expires as if it had none.
func TestOnlyAnUnverifiedWaitGoesStale(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seq := row.StatusSeq
	sub := e.subscribe(t)
	waits.set(errors.New("database is locked"))
	if err = e.sched.ReportWaiting(ctx, run.ID, "wait-agents", "", "two workers", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	p := nextActivity(t, sub, run.ID, &seq)
	if p.Activity != domain.ActivityWaitingAgents || p.Wait == nil || p.Wait.Kind != domain.WaitSourceUnknown ||
		!strings.Contains(p.Wait.Error, "database is locked") {
		t.Fatalf("published %s wait %+v", p.Activity, p.Wait)
	}
	quietFor(t, e)
	expectNoStatusEvent(t, sub, run.ID, "a wait whose conditions cannot be read")

	waits.set(nil)
	e.sched.refreshActivities(ctx)
	p = nextActivity(t, sub, run.ID, &seq)
	if p.Wait == nil || p.Wait.Kind != domain.WaitUnverified {
		t.Fatalf("published wait %+v, want unverified", p.Wait)
	}
	quietFor(t, e)
	p = nextActivity(t, sub, run.ID, &seq)
	if p.Activity != domain.ActivityWaiting || p.Cause != events.CauseWaitExpired || p.Wait != nil {
		t.Fatalf("an unverified wait went stale as %s by %s wait %+v", p.Activity, p.Cause, p.Wait)
	}
}

// A paused run is judged again on resume; a run that is not live never is.
func TestAPausedOrEndedRunHasNoWait(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	stuck := checking(time.Now().Add(-time.Hour))
	stuck.State, stuck.ResolvedAt = waitResolved, time.Now().Add(-time.Hour)
	waits.set(nil, stuck)
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on #582", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the broken wait", func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })

	if err := e.sched.Pause(ctx, run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.refreshActivities(ctx)
	if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); row.Activity != domain.ActivityWaitingCI || row.NeedsAttention(true) {
		t.Fatalf("paused run shows %s %q", row.Activity, row.Reason)
	}
	if got := e.sched.Wait(run.ID, domain.ActivityWaitingCI); got != nil {
		t.Fatalf("paused run has wait %+v", got)
	}
	if err := e.sched.Resume(ctx, run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.refreshActivities(ctx)
	waitFor(t, "the broken wait after resume", func() bool { return e.sched.Cause(run.ID, domain.ActivityNeedsYou) == events.CauseWaitFault })

	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	for _, activity := range []domain.RunActivity{"", domain.ActivityNeedsYou, domain.ActivityWaitingCI} {
		if got := e.sched.Wait(run.ID, activity); got != nil {
			t.Fatalf("ended run has wait %+v", got)
		}
	}
}

// Condition events race the stall sweep and the poll's refresh; the
// derived state must stay consistent under -race.
func TestConditionEventsRaceTheStallSweep(t *testing.T) {
	t.Parallel()
	waits := &fakeWaits{}
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.WaitConditions = waits
		cfg.StallThreshold = 10 * time.Millisecond
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	waits.set(nil, checking(time.Now()))
	if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on #582", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 25 {
				switch (i + j) % 4 {
				case 0:
					waits.set(nil, checking(time.Now()))
					_ = e.sched.WaitChanged(ctx, run.ID)
				case 1:
					stale := checking(time.Now().Add(-time.Hour))
					stale.ConsecutiveFailures = 2
					waits.set(nil, stale)
					_ = e.sched.WaitChanged(ctx, run.ID)
				case 2:
					e.sched.checkStalls(ctx)
				default:
					e.sched.refreshActivities(ctx)
					_ = e.sched.Wait(run.ID, domain.ActivityWaitingCI)
				}
			}
		})
	}
	wg.Wait()
	waits.set(nil, checking(time.Now()))
	e.sched.checkStalls(ctx)
	e.sched.refreshActivities(ctx)
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Activity != domain.ActivityWaitingCI || row.Reason != "CI on #582" {
		t.Fatalf("after the race the run shows %s %q", row.Activity, row.Reason)
	}
	if got := e.sched.Wait(run.ID, domain.ActivityWaitingCI); got == nil || got.Kind != domain.WaitCondition {
		t.Fatalf("after the race Wait = %+v", got)
	}
}
