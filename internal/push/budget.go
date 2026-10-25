package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

type Budgets interface {
	Budget(ctx context.Context, workspace domain.WorkspaceID) (cost.Status, error)
}

// Notice is a member's news of a workspace, such as its budget reaching the
// warning: Title names the workspace and Body is the line.
type Notice struct {
	Workspace   domain.WorkspaceID
	Title, Body string
}

type budgetKey struct {
	workspace domain.WorkspaceID
	member    domain.MemberID
}

// budgetDue is a state an admin is to hear of and the event that rose to it.
type budgetDue struct {
	state events.BudgetState
	seq   uint64
}

// budget tells every admin once that a workspace's spend rose past its
// budget's warning or its cap; a fall re-arms what it fell below.
func (s *Service) budget(ctx context.Context, e events.Event, p events.BudgetPayload) {
	if level(p.State) <= level(p.From) {
		return
	}
	workspace, d := e.WorkspaceID, budgetDue{p.State, e.Seq}
	s.wg.Go(func() {
		for {
			members, err := s.store.ListMembers(ctx)
			if err == nil {
				for _, m := range members {
					if m.Role == domain.RoleAdmin && !m.Pending {
						s.due(ctx, budgetKey{workspace, m.ID}, d)
					}
				}
				return
			}
			slog.Warn("push: list the admins to tell of a budget; trying again later", "workspace", workspace, "in", retry, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
			}
		}
	})
}

func level(state events.BudgetState) int {
	switch state {
	case events.BudgetWarn:
		return 1
	case events.BudgetExceeded:
		return 2
	}
	return 0
}

// due hands d to the key's watcher, starting one if there is none. Admin
// lookups can finish out of order, so of two handoffs the later event's
// stands.
func (s *Service) due(ctx context.Context, k budgetKey, d budgetDue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, watching := s.notices[k]
	if !watching {
		next = make(chan budgetDue, 1)
		s.notices[k] = next
		s.wg.Go(func() { s.watchBudget(ctx, k, next) })
	}
	select {
	case pending := <-next:
		if pending.seq > d.seq {
			d = pending
		}
	default:
	}
	next <- d
}

// watchBudget alone sends one admin a workspace's budget notices, so none
// overtakes another, each once they have left every dashboard alone for the
// quiet period. A state handed to it again before it went out goes out once.
func (s *Service) watchBudget(ctx context.Context, k budgetKey, next chan budgetDue) {
	var due budgetDue
	devices := true
	for {
		var wake <-chan time.Time
		if due.state != "" {
			if wait := time.Until(s.freeAt(k.member)); wait > 0 {
				wake = time.After(wait)
			} else if done, err := s.sendBudget(ctx, k, due.state, &devices); !done {
				slog.Warn("push: a budget notice was not sent; trying again later", "member", k.member, "workspace", k.workspace, "in", retry, "error", err)
				wake = time.After(retry)
			} else {
				due.state = ""
			}
		}
		if due.state == "" && s.unwatchBudget(k, next) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case handed := <-next:
			if handed.seq > due.seq {
				due, devices = handed, true
			}
		case <-wake:
		}
	}
}

func (s *Service) unwatchBudget(k budgetKey, next chan budgetDue) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(next) > 0 {
		return false
	}
	delete(s.notices, k)
	return true
}

// sendBudget reads the budget again and, while it stands at state, sends it
// to the admin's devices unless they have had it, then to the sink. It
// reports whether it is done: sent, or no longer standing.
func (s *Service) sendBudget(ctx context.Context, k budgetKey, state events.BudgetState, devices *bool) (bool, error) {
	member, workspace := k.member, k.workspace
	st, err := s.budgets.Budget(ctx, workspace)
	if err != nil {
		return false, err
	}
	if st.State != state {
		return true, nil
	}
	ws, err := s.store.GetWorkspace(ctx, workspace)
	if err != nil {
		return false, fmt.Errorf("push: read workspace %s: %w", workspace, err)
	}
	n := budgetNotice(ws, st)
	if *devices {
		subs, listErr := s.store.ListPushSubscriptions(ctx, member)
		if listErr != nil {
			return false, fmt.Errorf("push: list subscriptions: %w", listErr)
		}
		var payload []byte
		if payload, err = json.Marshal(notification{Title: n.Title, Body: n.Body, Workspace: string(workspace)}); err != nil {
			return false, fmt.Errorf("push: encode notification: %w", err)
		}
		urgency := urgencyNormal
		if state == events.BudgetExceeded {
			urgency = urgencyHigh
		}
		for _, sub := range subs {
			if err = s.send(ctx, sub, payload, topicOf("budget:"+string(workspace)), urgency); err != nil {
				slog.Warn("push: notification not delivered", "member", member, "error", err)
			}
		}
		*devices = false
	}
	if err = s.sink.Notice(ctx, member, n); err != nil {
		return false, err
	}
	return true, nil
}

func budgetNotice(ws *domain.Workspace, st cost.Status) Notice {
	spent := fmt.Sprintf("$%.2f of $%.2f spent", st.Spend.CostUSD, st.Budget.LimitUSD)
	if st.Spend.Advisory() {
		spent = "at least " + spent
	}
	body := "Budget warning: " + spent
	switch {
	case st.State == events.BudgetExceeded && st.Budget.Override:
		body = "Budget reached: " + spent + ", the override admits new runs"
	case st.State == events.BudgetExceeded:
		body = "Budget reached: " + spent + ", new runs are refused"
	}
	return Notice{Workspace: ws.ID, Title: clip(ws.Name, fallbackTitleRunes), Body: body}
}
