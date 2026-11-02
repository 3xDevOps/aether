package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
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

func TestEnhancedChildRunForwardsATurnItsKeeperEndedDuringARestart(t *testing.T) {
	shortProvisionalGrace(t)
	e, _ := newACPEnv(t)
	c, binDir := withCoordination(t, e)
	parent := e.launchACP(t, "parent")
	child := launchChildRun(t, e, parent, "fake", domain.LaunchACP, "k")
	waitItems(t, e.sched, child.ID, "the child's first turn", turnEnded("end_turn", 1))
	release := filepath.Join(t.TempDir(), "release")
	prompt := acpmock.PromptRunPrefix + "while [ ! -e " + release + " ]; do sleep 0.02; done; echo replayed"
	if _, err := e.sched.Inject(t.Context(), child.ID, e.member.ID, domain.AgentPrompt{Text: prompt}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child's second turn", func() bool {
		sess := e.sched.acp.session(child.ID)
		return sess != nil && sess.State().TurnInFlight
	})
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	// The turn ends while no server is attached; the keeper replays its end.
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	// The adopted session then records its id after it opens, slowly, so the
	// keeper's replay ends the turn before the session is published.
	if err := e.db.SetRunAgentSession(t.Context(), child.ID, ""); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Store = slowAgentSessionStore{e.cfg.Store}
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	next.UseCoordination(c, binDir)
	startScheduler(t, next)
	waitFor(t, "the replayed turn's message at the parent", func() bool {
		for _, call := range c.provisionalReports() {
			if call.run == child.ID && strings.Contains(call.message, "replayed") {
				return true
			}
		}
		return false
	})
}

type slowAgentSessionStore struct{ store.Store }

func (s slowAgentSessionStore) SetRunAgentSession(ctx context.Context, id domain.RunID, session string) error {
	time.Sleep(time.Second)
	return s.Store.SetRunAgentSession(ctx, id, session)
}
