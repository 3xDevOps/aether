package sshd

import (
	"context"
	"errors"
	"fmt"
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

type capturedLauncher struct {
	templates.Launcher
}

func (l capturedLauncher) LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	run, err := l.Launcher.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, opts)
	if run != nil {
		run.BaseCommit = "0123456789abcdef0123456789abcdef01234567"
		run.BaseBranch = "main"
		run.BaseSource = "local"
		run.BaseCheckedAt = time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)
	}
	return run, err
}

func templateEnv(t *testing.T, clock *testClock, mods ...func(*Config)) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		for _, mod := range mods {
			mod(c)
		}
		svc, err := templates.New(templates.Config{
			Store:    c.Store,
			Bus:      c.Bus,
			Runs:     capturedLauncher{Launcher: c.Runs.(templates.Launcher)},
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

// The whole workflow: an admin saves a template, a collaborator launches it
// by hand and schedules it, the cron loop fires it, and both paths report the
// immutable provenance carried by their captured runs. Then the collaborator
// is demoted and the schedule stops firing.
func TestTemplateLaunchedByHandAndByScheduleWithCapturedProvenance(t *testing.T) {
	t.Parallel()
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
		WorkspaceID: string(e.ws.ID),
		Name:        "nightly-deps",
		Task:        "upgrade {{ecosystem}} deps",
		Harness:     "claude",
		Mode:        "headless",
		Params:      map[string]string{"ecosystem": "go"},
		BudgetUSD:   2.5,
	}

	// Templates are workspace configuration: a collaborator may use them,
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
		WorkspaceID: string(e.ws.ID),
	}, &list); err != nil {
		t.Fatalf("template.list: %v", err)
	}
	if len(list.Templates) != 1 || list.Templates[0].Name != "nightly-deps" || list.Templates[0].BudgetUSD != 2.5 {
		t.Fatalf("templates = %+v, want the saved nightly-deps", list.Templates)
	}

	// Launched by hand, with a parameter overriding the default.
	var launched protocol.TemplateLaunchResult
	if err := bobControl.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
		WorkspaceID: string(e.ws.ID),
		Name:        "nightly-deps",
		Params:      map[string]string{"ecosystem": "npm"},
	}, &launched); err != nil {
		t.Fatalf("template.launch: %v", err)
	}
	if launched.BaseCommit != "0123456789abcdef0123456789abcdef01234567" ||
		launched.BaseBranch != "main" || launched.BaseSource != "local" ||
		launched.BaseCheckedAt != "2026-08-13T03:00:00Z" {
		t.Fatalf("launch provenance = commit %q branch %q source %q checked %q",
			launched.BaseCommit, launched.BaseBranch, launched.BaseSource, launched.BaseCheckedAt)
	}
	wantManual := "launch:" + string(e.ws.ID) + ":" + string(bob.ID) + ":upgrade npm deps:claude:headless"
	if calls := e.runs.Calls(); len(calls) != 1 || calls[0] != wantManual {
		t.Fatalf("calls after manual launch = %v, want [%s]", calls, wantManual)
	}

	// An unknown parameter is a typo, not a silent no-op.
	if err := bobControl.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
		WorkspaceID: string(e.ws.ID), Name: "nightly-deps", Params: map[string]string{"nope": "x"},
	}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("launch with an unknown parameter = %v, want CodeInvalidParams", err)
	}

	// A launch pinned to a version the template no longer has starts nothing.
	if list.Templates[0].Version == "" {
		t.Fatal("template.list returned no version")
	}
	if err := bobControl.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
		WorkspaceID: string(e.ws.ID), Name: "nightly-deps", ExpectedVersion: "0123456789abcdef",
	}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeConflict || !strings.Contains(pe.Message, list.Templates[0].Version) {
		t.Fatalf("launch at a stale version = %v, want CodeConflict naming %s", err, list.Templates[0].Version)
	}
	if calls := e.runs.Calls(); len(calls) != 1 {
		t.Fatalf("calls after a stale launch = %v, want only the manual launch", calls)
	}

	// Scheduling is launching: a standing order for future runs.
	var scheduled protocol.ScheduleSaveResult
	if err := bobControl.Call(protocol.MethodScheduleSave, protocol.ScheduleSaveParams{
		WorkspaceID: string(e.ws.ID), Template: "nightly-deps", Cron: "* * * * *",
	}, &scheduled); err != nil {
		t.Fatalf("schedule.save: %v", err)
	}
	if scheduled.Schedule.NextFireAt != "2026-08-13T03:01:00Z" {
		t.Fatalf("next fire = %q, want the next minute from now", scheduled.Schedule.NextFireAt)
	}

	// The clock reaches the slot: the loop fires exactly one run.
	clock.set(time.Date(2026, 8, 13, 3, 1, 30, 0, time.UTC))
	wantFired := "launch:" + string(e.ws.ID) + ":" + string(bob.ID) + ":upgrade go deps:claude:headless"
	eventually(t, "the schedule to fire", func() bool {
		calls := e.runs.Calls()
		return len(calls) == 2 && calls[1] == wantFired
	})

	entry := awaitTimeline(t, timeline, "scheduled session")
	wantEntry := "scheduled session run_new from local main at 0123456789abcdef0123456789abcdef01234567 (nightly-deps)"
	if !strings.Contains(entry, wantEntry) {
		t.Fatalf("timeline entry = %q, want %q", entry, wantEntry)
	}

	// A hand-launched run of the same prompt is indistinguishable from the
	// fired one: same scheduler call, same arguments.
	if err := bobControl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "upgrade go deps", Harness: "claude", Mode: "headless",
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
	if !strings.Contains(skipped, "may no longer launch sessions") {
		t.Fatalf("skip entry = %q, want the demotion as the reason", skipped)
	}
	if calls := e.runs.Calls(); len(calls) != 3 {
		t.Fatalf("calls after demotion = %v, want no further launches", calls)
	}
}

// validatingRuns refuses a model or an effort for the "fake" agent the way
// the scheduler refuses them for an agent with no model or effort flag.
type validatingRuns struct {
	*fakeRuns
}

func (validatingRuns) ValidateLaunch(_ context.Context, _, _ domain.MemberID, harness string, _ domain.LaunchMode, model, effort string) error {
	if harness == "fake" && model != "" {
		return fmt.Errorf("scheduler: agent %q has no model flag; launch it without a model", harness)
	}
	if harness == "fake" && effort != "" {
		return fmt.Errorf("scheduler: agent %q has no effort flag; launch it without an effort", harness)
	}
	return nil
}

func TestTemplateSaveRefusesModelTheAgentCannotTake(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}
	e := templateEnv(t, clock, func(c *Config) {
		c.Runs = validatingRuns{fakeRuns: c.Runs.(*fakeRuns)}
	})
	control := controlAs(t, e, e.signer)
	save := protocol.TemplateSaveParams{
		WorkspaceID: string(e.ws.ID),
		Name:        "nightly",
		Task:        "triage",
		Harness:     "fake",
		Mode:        "headless",
		Model:       "opus",
	}

	var pe *protocol.Error
	err := control.Call(protocol.MethodTemplateSave, save, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams ||
		pe.Message != `scheduler: agent "fake" has no model flag; launch it without a model` {
		t.Fatalf("template.save with a model = %v, want the no-model-flag refusal", err)
	}

	save.Model = ""
	if err := control.Call(protocol.MethodTemplateSave, save, nil); err != nil {
		t.Fatalf("template.save without a model: %v", err)
	}
}

func TestTemplateSaveRefusesAnEffortTheAgentHasNoFlagFor(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}
	e := templateEnv(t, clock, func(c *Config) {
		c.Runs = validatingRuns{fakeRuns: c.Runs.(*fakeRuns)}
	})
	control := controlAs(t, e, e.signer)
	save := protocol.TemplateSaveParams{
		WorkspaceID: string(e.ws.ID),
		Name:        "nightly",
		Task:        "triage",
		Harness:     "fake",
		Mode:        "headless",
		Effort:      "high",
	}

	var pe *protocol.Error
	err := control.Call(protocol.MethodTemplateSave, save, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams ||
		pe.Message != `scheduler: agent "fake" has no effort flag; launch it without an effort` {
		t.Fatalf("template.save with an effort = %v, want the no-effort-flag refusal", err)
	}

	save.Harness = "claude"
	var saved protocol.TemplateSaveResult
	if err := control.Call(protocol.MethodTemplateSave, save, &saved); err != nil {
		t.Fatalf("template.save with an effort its agent takes: %v", err)
	}
	if saved.Template.Effort != "high" {
		t.Fatalf("saved template effort = %q, want high", saved.Template.Effort)
	}
	var listed protocol.TemplateListResult
	if err := control.Call(protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: string(e.ws.ID)}, &listed); err != nil {
		t.Fatalf("template.list: %v", err)
	}
	if len(listed.Templates) != 1 || listed.Templates[0].Effort != "high" {
		t.Fatalf("listed templates = %+v, want one with effort high", listed.Templates)
	}
}

// awaitTimeline waits for a workspace.timeline entry whose message contains
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

// A retry of a pinned template launch returns its run even after the
// template changed, whatever version the retry names.
func TestTemplateLaunchReplayIgnoresTheVersion(t *testing.T) {
	t.Parallel()
	e := templateEnv(t, &testClock{now: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)})
	control := controlAs(t, e, e.signer)
	save := protocol.TemplateSaveParams{WorkspaceID: string(e.ws.ID), Name: "triage", Task: "triage", Harness: "claude", Mode: "headless"}
	if err := control.Call(protocol.MethodTemplateSave, save, nil); err != nil {
		t.Fatal(err)
	}
	var list protocol.TemplateListResult
	if err := control.Call(protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: string(e.ws.ID)}, &list); err != nil {
		t.Fatal(err)
	}
	launch := protocol.TemplateLaunchParams{WorkspaceID: string(e.ws.ID), Name: "triage", ExpectedVersion: list.Templates[0].Version, IdempotencyKey: "k1"}
	var first protocol.TemplateLaunchResult
	if err := control.Call(protocol.MethodTemplateLaunch, launch, &first); err != nil {
		t.Fatal(err)
	}
	save.Task = "triage and label"
	if err := control.Call(protocol.MethodTemplateSave, save, nil); err != nil {
		t.Fatal(err)
	}
	// The fake scheduler writes no run row, so the replay finds the key's
	// run gone; a version mismatch would be refused as other params first.
	launch.ExpectedVersion = "0123456789abcdef"
	var pe *protocol.Error
	err := control.Call(protocol.MethodTemplateLaunch, launch, nil)
	if !errors.As(err, &pe) || pe.Code == protocol.CodeConflict || !strings.Contains(pe.Message, first.Run.ID) || len(e.runs.Calls()) != 1 {
		t.Fatalf("retry after the template changed = %v after %v, want the key's session %s and no new launch", err, e.runs.Calls(), first.Run.ID)
	}
	// replay_only finds the same run, and launches nothing for a key that
	// started none.
	launch.ReplayOnly = true
	if err := control.Call(protocol.MethodTemplateLaunch, launch, nil); !errors.As(err, &pe) || !strings.Contains(pe.Message, first.Run.ID) {
		t.Fatalf("replay_only of the first key = %v, want its session %s", err, first.Run.ID)
	}
	launch.IdempotencyKey = "k2"
	if err := control.Call(protocol.MethodTemplateLaunch, launch, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound || len(e.runs.Calls()) != 1 {
		t.Fatalf("replay_only of a new key = %v after %v, want not found and no launch", err, e.runs.Calls())
	}
	launch.ReplayOnly, launch.ExpectedVersion = false, ""
	if err := control.Call(protocol.MethodTemplateLaunch, launch, nil); err != nil || len(e.runs.Calls()) != 2 {
		t.Fatalf("launch after a replay_only miss = %v after %v, want it launched", err, e.runs.Calls())
	}
}
