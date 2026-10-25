package scheduler

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func shortProvisionalGrace(t *testing.T) {
	grace := provisionalGrace
	provisionalGrace = time.Second
	t.Cleanup(func() { provisionalGrace = grace })
}

func TestBackgroundWorkerOverACPForwardsItsLastMessage(t *testing.T) {
	shortProvisionalGrace(t)
	for _, stop := range []string{"end_turn", "max_tokens"} {
		t.Run(stop, func(t *testing.T) {
			e, rt := newBackgroundEnv(t, enhancedAgent, true)
			rt.fixture.Prompt.Result = json.RawMessage(`{"stopReason":"` + stop + `"}`)
			c, _ := withCoordination(t, e)
			run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
				WorkspaceID: e.ws.ID, RunID: "silent-worker", Actor: e.member.ID,
				RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "say pong",
				Harness: enhancedAgent, Mode: domain.LaunchHeadless,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the worker's last message", func() bool {
				calls := c.provisionalReports()
				return len(calls) == 1 && calls[0].run == run.ID && strings.Contains(calls[0].message, "pong")
			})
		})
	}
}

func TestEnhancedWorkerWokenWithinTheGraceForwardsOnlyItsLastTurn(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, _ := withCoordination(t, e)
	var wake sync.Once
	c.onWake = func(run domain.RunID) {
		wake.Do(func() {
			go func() {
				if err := e.sched.WakeEnhanced(t.Context(), run, "say pong"); err != nil {
					t.Errorf("WakeEnhanced: %v", err)
				}
			}()
		})
	}
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "woken-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "say pong",
		Harness: "fake", Mode: domain.LaunchACP,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "both turns", turnEnded("end_turn", 2))
	waitFor(t, "a provisional report", func() bool { return len(c.provisionalReports()) > 0 })
	e.sched.mu.Lock()
	last := e.sched.runs[run.ID].turnStartedAt
	e.sched.mu.Unlock()
	if calls := c.provisionalReports(); !calls[0].since.Equal(last) {
		t.Fatalf("provisional report for the turn started %v, want only the last turn's (%v)", calls[0].since, last)
	}
}
