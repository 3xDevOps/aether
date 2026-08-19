package sshd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/templates"
)

// testClock is the cron loop's clock, wound forward by the test instead of
// by real time.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// staleBase is a workspace whose base branch was last pushed ten days ago:
// laptops closed, nobody pushing, exactly the overnight case.
type staleBase struct{ committed time.Time }

func (b staleBase) BaseCommitTime(context.Context, domain.WorkspaceID, string) (time.Time, error) {
	return b.committed, nil
}

func templateEnv(t *testing.T, clock *testClock) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		svc, err := templates.New(templates.Config{
			Store:    c.Store,
			Bus:      c.Bus,
			Runs:     c.Runs,
			Base:     staleBase{committed: clock.Now().Add(-10 * 24 * time.Hour)},
			Interval: 10 * time.Millisecond,
			Now:      clock.Now,
		})
		if err != nil {
			t.Fatalf("templates.New: %v", err)
		}
		if err := svc.Start(context.Background()); err != nil {
			t.Fatalf("templates.Start: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		c.Services.Templates = svc
	})
}

// The whole overnight workflow: an admin saves a template, a collaborator
// launches it by hand and schedules it, the cron loop fires it, and the
// fired run is byte-for-byte the run a hand launch produces - reported
// with the age of the base it started from. Then the collaborator is
// demoted and the schedule stops firing.
func TestTemplateLaunchedByHandAndByScheduleFromAStaleBase(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}
	e := templateEnv(t, clock)
	ctx := context.Background()
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobControl := controlAs(t, e, bobSigner)
	adaControl := controlAs(t, e, e.signer)

	timeline, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = timeline.Close() }()

	save := protocol.TemplateSaveParams{
		SessionID: string(e.sess.ID),
		Name:      "nightly-deps",
		Task:      "upgrade {{ecosystem}} deps",
		Harness:   "claude",
		Mode:      "headless",
		Params:    map[string]string{"ecosystem": "go"},
		BudgetUSD: 2.5,
	}

	// Templates are session configuration: a collaborator may use them,
	// only an admin may define them.
	var pe *protocol.Error
	if err := bobControl.Call(protocol.MethodTemplateSave, save, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator template.save = %v, want CodeDenied", err)
	}
	var saved protocol.TemplateSaveResult
	if err := adaControl.Call(protocol.MethodTemplateSave, save, &saved); err != nil {
		t.Fatalf("template.save: %v", err)
	}

	var list protocol.TemplateListResult
	if err := bobControl.Call(protocol.MethodTemplateList, protocol.TemplateListParams{
		SessionID: string(e.sess.ID),
	}, &list); err != nil {
		t.Fatalf("template.list: %v", err)
	}
	if len(list.Templates) != 1 || list.Templates[0].Name != "nightly-deps" || list.Templates[0].BudgetUSD != 2.5 {
		t.Fatalf("templates = %+v, want the saved nightly-deps", list.Templates)
	}

	// Launched by hand, with a parameter overriding the default.
	var launched protocol.TemplateLaunchResult
	if err := bobControl.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
		SessionID: string(e.sess.ID),
		Name:      "nightly-deps",
		Params:    map[string]string{"ecosystem": "npm"},
	}, &launched); err != nil {
		t.Fatalf("template.launch: %v", err)
	}
	if launched.BaseBranch != "main" || launched.BaseAge != "10d" {
		t.Fatalf("launch reported base %q age %q, want main 10d", launched.BaseBranch, launched.BaseAge)
	}
	wantManual := "launch:" + string(e.sess.ID) + ":" + string(bob.ID) + ":upgrade npm deps:claude:headless"
	if calls := e.runs.Calls(); len(calls) != 1 || calls[0] != wantManual {
		t.Fatalf("calls after manual launch = %v, want [%s]", calls, wantManual)
	}

	// An unknown parameter is a typo, not a silent no-op.
	if err := bobControl.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
		SessionID: string(e.sess.ID), Name: "nightly-deps", Params: map[string]string{"nope": "x"},
	}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("launch with an unknown parameter = %v, want CodeInvalidParams", err)
	}

	// Scheduling is launching: a standing order for future runs.
	var scheduled protocol.ScheduleSaveResult
	if err := bobControl.Call(protocol.MethodScheduleSave, protocol.ScheduleSaveParams{
		SessionID: string(e.sess.ID), Template: "nightly-deps", Cron: "* * * * *",
	}, &scheduled); err != nil {
		t.Fatalf("schedule.save: %v", err)
	}
	if scheduled.Schedule.NextFireAt != "2026-08-13T03:01:00Z" {
		t.Fatalf("next fire = %q, want the next minute from now", scheduled.Schedule.NextFireAt)
	}

	// The clock reaches the slot: the loop fires exactly one run.
	clock.set(time.Date(2026, 8, 13, 3, 1, 30, 0, time.UTC))
	wantFired := "launch:" + string(e.sess.ID) + ":" + string(bob.ID) + ":upgrade go deps:claude:headless"
	eventually(t, "the schedule to fire", func() bool {
		calls := e.runs.Calls()
		return len(calls) == 2 && calls[1] == wantFired
	})

	entry := awaitTimeline(t, timeline, "scheduled run")
	if !strings.Contains(entry, "base main is 10d old") || !strings.Contains(entry, "nightly-deps") {
		t.Fatalf("timeline entry = %q, want the template and the base age", entry)
	}

	// A hand-launched run of the same prompt is indistinguishable from the
	// fired one: same scheduler call, same arguments.
	if err := bobControl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(e.sess.ID), Task: "upgrade go deps", Harness: "claude", Mode: "headless",
	}, nil); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	if calls := e.runs.Calls(); calls[2] != calls[1] {
		t.Fatalf("hand-launched call %q differs from the fired one %q", calls[2], calls[1])
	}

	// Demote the schedule's owner: the cron loop re-checks the capability
	// against the current member row, so the schedule stops firing.
	bob.Role = domain.RoleViewer
	if err := e.store.UpdateMember(ctx, bob); err != nil {
		t.Fatalf("demote Bob: %v", err)
	}
	clock.set(time.Date(2026, 8, 13, 3, 2, 30, 0, time.UTC))
	skipped := awaitTimeline(t, timeline, "skipped")
	if !strings.Contains(skipped, "may no longer launch runs") {
		t.Fatalf("skip entry = %q, want the demotion as the reason", skipped)
	}
	if calls := e.runs.Calls(); len(calls) != 3 {
		t.Fatalf("calls after demotion = %v, want no further launches", calls)
	}
}

// awaitTimeline waits for a session.timeline entry whose message contains
// want, and returns it.
func awaitTimeline(t *testing.T, sub events.Subscription, want string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("timeline stream closed waiting for %q", want)
			}
			if p, isTimeline := ev.Payload.(events.TimelinePayload); isTimeline && strings.Contains(p.Message, want) {
				return p.Message
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a timeline entry containing %q", want)
		}
	}
}
