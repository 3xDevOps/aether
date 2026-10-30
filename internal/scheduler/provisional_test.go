package scheduler

import (
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

func launchChildRun(t *testing.T, e *testEnv, parent *domain.Run, harness string, mode domain.LaunchMode, key string) *domain.Run {
	t.Helper()
	reserved, _, err := e.db.ReserveChild(t.Context(), &domain.ChildReservation{
		ParentRunID: parent.ID, RootRunID: parent.ID, TreeKey: string(parent.ID), WorkspaceID: e.ws.ID,
		OwnerID: e.member.ID, AccountID: e.member.ID, Harness: harness, Mode: mode,
		Purpose: "say pong", Task: "say pong", IdempotencyKey: key, RequestHash: "h",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", harness, mode,
		domain.LaunchOptions{ReservedRunID: reserved.RunID})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestEnhancedChildWokenWithinTheGraceForwardsOnlyItsLastTurn(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, _ := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	var wake sync.Once
	c.onWake = func(run domain.RunID) {
		if run == parent.ID {
			return
		}
		wake.Do(func() {
			go func() {
				if err := e.sched.WakeEnhanced(t.Context(), run, "say pong"); err != nil {
					t.Errorf("WakeEnhanced: %v", err)
				}
			}()
		})
	}
	run := launchChildRun(t, e, parent, "fake", domain.LaunchACP, "k")
	waitItems(t, e.sched, run.ID, "both turns", turnEnded("end_turn", 2))
	waitFor(t, "a provisional report", func() bool { return len(c.provisionalReports()) > 0 })
	e.sched.mu.Lock()
	last := e.sched.runs[run.ID].turnStartedAt
	e.sched.mu.Unlock()
	if calls := c.provisionalReports(); !calls[0].since.Equal(last) {
		t.Fatalf("provisional report for the turn started %v, want only the last turn's (%v)", calls[0].since, last)
	}
}

func TestEnhancedChildRunForwardsItsLastMessageToItsParent(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, _ := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	child := launchChildRun(t, e, parent, "fake", domain.LaunchACP, "k")
	waitFor(t, "the child's last message", func() bool {
		for _, call := range c.provisionalReports() {
			if call.run == child.ID && strings.Contains(call.message, "pong") {
				return true
			}
		}
		return false
	})
}
