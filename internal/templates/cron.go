package templates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

// Schedules returns a session's schedules with the next instant each is
// due.
func (s *Service) Schedules(ctx context.Context, session domain.SessionID) ([]ScheduleInfo, error) {
	schedules, err := s.store.ListSchedules(ctx, session)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduleInfo, 0, len(schedules))
	for _, sc := range schedules {
		out = append(out, ScheduleInfo{Schedule: sc, Next: s.nextFire(sc.ID)})
	}
	return out, nil
}

// SaveSchedule creates or replaces the cron rule firing a template. member
// is recorded as the schedule's owner: fires are attributed to them and
// re-checked against their role, so this is not a way to launch runs as
// somebody else.
func (s *Service) SaveSchedule(ctx context.Context, session domain.SessionID, template, spec string, member domain.MemberID) (*ScheduleInfo, error) {
	rule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("%w: cron %q: %s", ErrInvalidDefinition, spec, err)
	}
	// Field-valid but calendar-impossible rules ("0 0 31 4 *") parse
	// fine and then have no next slot at all. Rejecting them here is the
	// only place a person sees the mistake.
	if rule.Next(s.now()).IsZero() {
		return nil, fmt.Errorf("%w: cron %q never occurs", ErrInvalidDefinition, spec)
	}
	t, err := s.store.GetTemplate(ctx, session, template)
	if err != nil {
		return nil, fmt.Errorf("templates: schedule %s: %w", template, err)
	}
	// A fire supplies no parameters, so a schedule is only honest for a
	// template that renders from its defaults alone.
	if _, err = Render(t.Task, t.Params, nil); err != nil {
		return nil, fmt.Errorf("%w: template %q cannot fire unattended: %s", ErrInvalidDefinition, template, err)
	}
	sc := &store.Schedule{TemplateID: t.ID, Cron: spec, MemberID: member}
	if err := s.store.SaveSchedule(ctx, sc); err != nil {
		return nil, fmt.Errorf("templates: schedule %s: %w", template, err)
	}
	sc.SessionID, sc.Template = t.SessionID, t.Name
	return &ScheduleInfo{Schedule: sc, Next: s.seed(sc.ID, spec, rule)}, nil
}

// DeleteSchedule removes a template's cron rule, leaving the template.
func (s *Service) DeleteSchedule(ctx context.Context, session domain.SessionID, template string) error {
	t, err := s.store.GetTemplate(ctx, session, template)
	if err != nil {
		return fmt.Errorf("templates: unschedule %s: %w", template, err)
	}
	if err := s.store.DeleteSchedule(ctx, t.ID); err != nil {
		return fmt.Errorf("templates: unschedule %s: %w", template, err)
	}
	return nil
}

func (s *Service) loop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.scan(ctx, true); err != nil && ctx.Err() == nil {
				slog.Warn("templates: cron scan", "error", err)
			}
		}
	}
}

// scan advances every schedule's timer and fires the due ones. A schedule
// the service has not seen before is only seeded: its first fire is the
// next slot from now, which is how a schedule missed while the server was
// down is skipped instead of caught up.
func (s *Service) scan(ctx context.Context, fire bool) error {
	schedules, err := s.store.ListSchedules(ctx, "")
	if err != nil {
		return fmt.Errorf("templates: list schedules: %w", err)
	}
	now := s.now()
	live := make(map[string]struct{}, len(schedules))
	var due []*store.Schedule

	s.mu.Lock()
	for _, sc := range schedules {
		live[sc.ID] = struct{}{}
		t, ok := s.timers[sc.ID]
		if !ok || t.spec != sc.Cron {
			rule, perr := cron.ParseStandard(sc.Cron)
			if perr != nil {
				slog.Warn("templates: unparseable cron", "schedule", sc.ID, "cron", sc.Cron, "error", perr)
				continue
			}
			s.timers[sc.ID] = &timer{spec: sc.Cron, rule: rule, next: rule.Next(now)}
			continue
		}
		// A zero next is a rule with no reachable slot; it is never due,
		// or it would be due on every scan forever.
		if t.next.IsZero() || now.Before(t.next) {
			continue
		}
		// One fire per due schedule per scan, and the next slot is
		// computed from now: overdue slots are dropped, not queued.
		t.next = t.rule.Next(now)
		due = append(due, sc)
	}
	for id := range s.timers {
		if _, ok := live[id]; !ok {
			delete(s.timers, id)
		}
	}
	s.mu.Unlock()

	if !fire {
		return nil
	}
	for _, sc := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.fire(ctx, sc, now)
	}
	return nil
}

// fire launches one schedule's template. The creating member's capability
// is re-checked against their current row first: a cron fire never reaches
// the RPC boundary, so this is the only place that check can happen.
func (s *Service) fire(ctx context.Context, sc *store.Schedule, at time.Time) {
	if err := s.store.MarkScheduleFired(ctx, sc.ID, at); err != nil {
		slog.Warn("templates: record schedule fire", "schedule", sc.ID, "error", err)
	}
	t, err := s.store.GetTemplate(ctx, sc.SessionID, sc.Template)
	if err != nil {
		slog.Warn("templates: schedule template unavailable", "schedule", sc.ID, "error", err)
		return
	}
	if err = s.allowed(ctx, sc.MemberID); err != nil {
		slog.Warn("templates: schedule skipped", "schedule", sc.ID, "template", t.Name, "error", err)
		s.publish(ctx, t.SessionID, "", sc.MemberID,
			fmt.Sprintf("schedule for template %q skipped: %s", t.Name, err))
		return
	}
	launched, err := s.launch(ctx, t, sc.MemberID, nil)
	if err != nil {
		slog.Warn("templates: schedule fire failed", "schedule", sc.ID, "template", t.Name, "error", err)
		s.publish(ctx, t.SessionID, "", sc.MemberID,
			fmt.Sprintf("schedule for template %q failed: %s", t.Name, err))
		return
	}
	s.publish(ctx, t.SessionID, launched.Run.ID, sc.MemberID,
		fmt.Sprintf("scheduled run from template %q (cron %q); %s", t.Name, sc.Cron, launched.Base))
}

// allowed re-reads the schedule's member and checks the launch capability
// against their current role.
func (s *Service) allowed(ctx context.Context, member domain.MemberID) error {
	m, err := s.store.GetMember(ctx, member)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("member %s is no longer a member", member)
		}
		return err
	}
	if m.Pending {
		return fmt.Errorf("member %s is awaiting admin approval", member)
	}
	if err := permissions.Check(permissions.Launch, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{}); err != nil {
		return fmt.Errorf("member %s may no longer launch runs: %w", member, err)
	}
	return nil
}

func (s *Service) seed(id, spec string, rule cron.Schedule) time.Time {
	next := rule.Next(s.now())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timers[id] = &timer{spec: spec, rule: rule, next: next}
	return next
}

func (s *Service) nextFire(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.timers[id]; ok {
		return t.next
	}
	return time.Time{}
}
