package push

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// The cost service publishes budget states; the workspace's admins, its
// owner and the super admins, hear a rise once each, on their devices and
// the sink, as the budget stands when it is sent, and nothing more until a
// fall re-arms it, a restart of both services included. An admin who does
// not own the workspace hears nothing.
func TestAdminsHearOnceWhenAWorkspaceBudgetRises(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	heard := &sink{heard: make(chan string, 64)}
	h.sink = heard
	h.start()
	carol, dan := h.member("Carol"), h.member("Dan")
	for m, role := range map[*domain.Member]domain.Role{h.ada: domain.RoleAdmin, carol: domain.RoleSuperAdmin, dan: domain.RoleAdmin} {
		m.Role = role
		if err := h.db.UpdateMember(h.ctx, m); err != nil {
			t.Fatalf("UpdateMember: %v", err)
		}
	}
	if err := h.db.SetWorkspaceOwner(h.ctx, h.ws.ID, h.ada.ID); err != nil {
		t.Fatalf("SetWorkspaceOwner: %v", err)
	}
	adas, carols, bobs, dans := h.subscribe(h.ada), h.subscribe(carol), h.subscribe(h.bob), h.subscribe(dan)
	run := h.run(h.bob, "Index the monorepo", domain.LaunchACP)
	spend := func(usd float64) {
		h.publish(run, events.RunCostPayload{CostUSD: usd, Metered: true})
	}
	budget := func(limit, warn float64) {
		t.Helper()
		if _, err := h.costs.SetBudget(h.ctx, h.ws.ID, cost.Change{LimitUSD: limit, WarnUSD: warn}, h.ada.ID); err != nil {
			t.Fatalf("SetBudget: %v", err)
		}
	}
	expect := func(body, urgency string) {
		t.Helper()
		want := notification{Title: "proj", Body: body, Workspace: string(h.ws.ID)}
		for _, d := range []*device{adas, carols} {
			d.expect(t, want)
			if got := d.urgency.Load(); got != urgency {
				t.Errorf("urgency = %v, want %s", got, urgency)
			}
		}
		heard.expectNotices(t, fmt.Sprintf("%s hears %q", h.ada.ID, body), fmt.Sprintf("%s hears %q", carol.ID, body))
	}

	budget(10, 8)
	spend(8.5)
	expect("Budget warning: $8.50 of $10.00 spent", urgencyNormal)
	dans.expectNone(t, testQuiet)

	// More spend and an admin's edit in the same state.
	spend(9)
	budget(10, 8.2)
	adas.expectNone(t, testQuiet)

	budget(20, 15)
	spend(16)
	expect("Budget warning: $16.00 of $20.00 spent", urgencyNormal)
	spend(21)
	expect("Budget reached: $21.00 of $20.00 spent, new sessions are refused", urgencyHigh)

	// A warning still waiting out the quiet period when the cap is reached
	// gives way to it.
	budget(30, 25)
	states, err := h.bus.Subscribe(h.ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeBudget}}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	h.clock.freeze()
	h.svc.Active(h.ada.ID)
	h.svc.Active(carol.ID)
	spend(26)
	spend(31)
	for reached := false; !reached; {
		select {
		case e := <-states.Events():
			reached = e.Payload.(events.BudgetPayload).State == events.BudgetExceeded
		case <-time.After(10 * time.Second):
			t.Fatal("the cost service did not publish the cap reached")
		}
	}
	_ = states.Close()
	h.clock.resume(testQuiet)
	expect("Budget reached: $31.00 of $30.00 spent, new sessions are refused", urgencyHigh)

	// A warning raised again before it went out goes out once.
	budget(40, 35)
	h.svc.Active(h.ada.ID)
	h.svc.Active(carol.ID)
	spend(36)
	budget(50, 45)
	budget(40, 35)
	expect("Budget warning: $36.00 of $40.00 spent", urgencyNormal)

	if err := h.costs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.startCosts()
	h.start()
	spend(37)
	adas.expectNone(t, testQuiet)
	bobs.expectNone(t, testQuiet)
}

// clock is the time the service reads; frozen, the quiet period never ends.
type clock struct {
	mu     sync.Mutex
	frozen time.Time
	skew   time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.frozen.IsZero() {
		return c.frozen
	}
	return time.Now().Add(c.skew)
}

func (c *clock) freeze() {
	now := c.now()
	c.mu.Lock()
	c.frozen = now
	c.mu.Unlock()
}

// advance moves the clock forward by d.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.skew += d
	c.mu.Unlock()
}

// resume lets time run again from d past when it froze.
func (c *clock) resume(d time.Duration) {
	c.mu.Lock()
	c.skew = time.Until(c.frozen.Add(d))
	c.frozen = time.Time{}
	c.mu.Unlock()
}

func (s *sink) expectNotices(t *testing.T, want ...string) {
	t.Helper()
	var got []string
	for range want {
		select {
		case n := <-s.heard:
			got = append(got, n)
		case <-time.After(10 * time.Second):
			t.Fatalf("sink heard %q, want %q", got, want)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("sink heard %q, want %q", got, want)
	}
}
