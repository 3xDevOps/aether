package scheduler

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// A session idle for 7 days, asleep or waiting for its owner, is closed as
// its owner's Close would close it, and Relaunch reopens it. A turn that
// made no progress in those days is no activity.
func TestASessionIdleForSevenDaysIsClosedAndCanBeRelaunched(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, true)
	asleep := e.launchACP(t, "say pong")
	waitItems(t, e.sched, asleep.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, asleep.ID)
	stuck := e.launchACP(t, acpmock.PromptWait)
	waitFor(t, "the turn running", func() bool {
		sess := e.sched.acp.session(stuck.ID)
		return sess != nil && sess.State().TurnInFlight
	})
	sub := e.subscribe(t)

	e.sched.closeIdleRuns(t.Context(), time.Now().UTC().Add(idleCloseAfter-time.Hour))
	if got, _ := e.db.GetRun(t.Context(), asleep.ID); got.Status.Terminal() {
		t.Fatal("a session idle for less than 7 days was closed")
	}
	e.sched.closeIdleRuns(t.Context(), time.Now().UTC().Add(idleCloseAfter+time.Hour))
	got, err := e.db.GetRun(t.Context(), asleep.ID)
	if err != nil || got.Status != domain.RunCompleted || got.Reason != retainedCloseReason {
		t.Fatalf("the idle session = %+v, %v; want closed with its sandbox kept", got, err)
	}
	if ev := waitTimelineEvent(t, sub, asleep.ID, events.TimelineNote); ev.Payload.(events.TimelinePayload).Message != idleClosedNote {
		t.Fatalf("the close's note = %+v", ev.Payload)
	}
	if sr.isSaved(e.cid(t, asleep.ID)) || containerState(e.container(t, asleep.ID)) != "stopped" {
		t.Fatal("the closed VM was not stopped without saved memory, as a closed session is")
	}
	if got, _ := e.db.GetRun(t.Context(), stuck.ID); !got.Status.Terminal() {
		t.Fatal("a session whose turn made no progress for 7 days stayed open")
	}
	if _, err := e.sched.Relaunch(t.Context(), asleep.ID, e.member.ID); err != nil {
		t.Fatalf("relaunch of the idle-closed session: %v", err)
	}
}

// A sleeping VM idle for 7 days is woken to commit before its close. While
// admission refuses the wake it stays open for the next sweep, and so does
// one the memory guard saved while it worked.
func TestAnIdleSleepingVMIsClosedOnlyOnceItWakes(t *testing.T) {
	t.Parallel()
	e, _, _ := newSaveEnv(t, true)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	later := time.Now().UTC().Add(idleCloseAfter + time.Hour)

	e.sched.closeIdleRuns(t.Context(), later)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() || e.sched.Sleep(run.ID).AsleepSince == nil {
		t.Fatalf("with its wake refused the session is %s, asleep %v; want it open and asleep", got.Status, e.sched.Sleep(run.ID).AsleepSince != nil)
	}
	free.Store(80 << 30)
	setMemorySaved := func(n uint64) {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		e.sched.runs[run.ID].memorySaved = n
	}
	setMemorySaved(1 << 30)
	e.sched.closeIdleRuns(t.Context(), later)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() {
		t.Fatal("a VM the memory guard saved while it worked was closed")
	}
	setMemorySaved(0)
	e.sched.closeIdleRuns(t.Context(), later)
	if got, err := e.db.GetRun(t.Context(), run.ID); err != nil || got.Status != domain.RunCompleted {
		t.Fatalf("the idle session = %+v, %v; want it closed once it could wake", got, err)
	}
}
