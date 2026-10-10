package push

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestNeedOf(t *testing.T) {
	t.Parallel()
	parked := func(reason string, edit ...func(*domain.Run)) *domain.Run {
		run := &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention, Reason: reason}
		for _, e := range edit {
			e(run)
		}
		return run
	}
	enhanced := func(r *domain.Run) { r.Mode, r.ACP = domain.LaunchACP, true }
	background := func(r *domain.Run) { r.Mode = domain.LaunchHeadless }
	worker := func(r *domain.Run) { r.MissionRole = "worker" }
	unseen := func(r *domain.Run) { r.OutcomeUnseen = true }
	question := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	permission := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}, {ID: "p1", SessionID: "s1", Kind: "permission"}}
	approval := &store.Approval{ID: "appr-1", Action: "ExitPlanMode"}

	for _, tc := range []struct {
		name     string
		run      *domain.Run
		approval *store.Approval
		inputs   []domain.RunInputRequest
		paused   bool
		want     string
	}{
		{name: "working", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning}},
		{name: "approval", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunRunning}, approval: approval, want: "Permission: ExitPlanMode"},
		{name: "approval before a native request", run: parked("agent idle"), approval: approval, inputs: question, want: "Permission: ExitPlanMode"},
		{name: "permission before question", run: parked("agent idle", enhanced), inputs: permission, want: "Permission requested"},
		{name: "terminal permission", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning}, inputs: permission, want: "Permission: answer in the terminal"},
		{name: "enhanced question while working", run: &domain.Run{Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}, inputs: question, want: "Question from the agent"},
		{name: "terminal question", run: parked("agent idle"), inputs: question, want: "Question: answer in the terminal"},
		{name: "background question", run: parked("agent idle", background), inputs: question},
		{name: "worker request", run: parked("agent idle", worker), approval: approval, inputs: permission},
		{name: "idle", run: parked("agent idle"), want: "Agent idle"},
		{name: "enhanced turn ended", run: parked("agent idle", enhanced), want: "Waiting for your reply"},
		{name: "stalled", run: parked("stalled: no output for 10m0s", enhanced), want: "No activity"},
		{name: "paused", run: parked("agent idle"), paused: true},
		{name: "background idle", run: parked("stalled: no output for 10m0s", background)},
		{name: "worker idle", run: parked("agent idle", worker)},
		{name: "blocked", run: parked("blocked: need the staging API key", background), want: "Blocked: need the staging API key"},
		{name: "blocked while paused", run: parked("blocked: need the staging API key"), paused: true, want: "Blocked: need the staging API key"},
		{name: "worker blocked", run: parked("blocked: merge conflict", worker), want: "Worker blocked: merge conflict"},
		{name: "enhanced failure", run: parked("enhanced session failed: not logged in", background), want: "Enhanced unavailable: not logged in"},
		{name: "worker enhanced failure", run: parked("enhanced turn failed: timeout", worker)},
		{name: "reported success", run: parked("agent reported success", unseen), want: "Agent reported success, review the result"},
		{name: "reported failure", run: parked("agent reported failure", enhanced, unseen), want: "Agent reported failure, review the result"},
		{name: "reported and seen", run: parked("agent reported success")},
		{name: "finished", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true}, want: "Finished, review the result"},
		{name: "failed", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunFailed, OutcomeUnseen: true}, want: "Failed, review the result"},
		{name: "finished and seen", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunCompleted}},
		{name: "closed by a member", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunAbandoned}},
		{name: "worker finished", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true, MissionRole: "worker"}},
	} {
		got := needOf(tc.run, tc.approval, tc.inputs, tc.paused)
		switch {
		case got == nil && tc.want != "":
			t.Errorf("%s: no need, want %q", tc.name, tc.want)
		case got != nil && got.body != tc.want:
			t.Errorf("%s: body = %q, want %q", tc.name, got.body, tc.want)
		}
	}
}

// The key is what makes a standing state announce once and a new one
// announce again.
func TestNeedKeyFollowsTheStandingState(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_800_000_000, 0)
	later := at.Add(time.Hour)
	idle := &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention, Reason: "agent idle", StatusChangedAt: &at}
	again := *idle
	again.StatusChangedAt = &later
	blocked := *idle
	blocked.Reason = "blocked: need a key"
	first := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	second := []domain.RunInputRequest{{ID: "q2", SessionID: "s1", Kind: "question"}}

	same := needOf(idle, nil, nil, false).key
	for name, other := range map[string]*need{
		"idle again after working": needOf(&again, nil, nil, false),
		"a different reason":       needOf(&blocked, nil, nil, false),
		"a question":               needOf(idle, nil, first, false),
	} {
		if other.key == same {
			t.Errorf("%s has the key of the first idle park, %q", name, same)
		}
	}
	if repeat := needOf(idle, nil, nil, false).key; repeat != same {
		t.Errorf("the same park has keys %q and %q", same, repeat)
	}
	if needOf(idle, nil, first, false).key == needOf(idle, nil, second, false).key {
		t.Error("two different questions share a key")
	}
}

func TestRunTitle(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 40)
	for name, tc := range map[string]struct {
		run  domain.Run
		want string
	}{
		"title":           {domain.Run{Title: " Fix the login redirect ", Task: "do it"}, "Fix the login redirect"},
		"first line":      {domain.Run{Task: "\n  fix the auth bug\nand add a test"}, "fix the auth bug"},
		"neither":         {domain.Run{}, "Untitled run"},
		"long is clipped": {domain.Run{Task: long}, strings.TrimRight(long[:fallbackTitleRunes], " ") + "…"},
	} {
		if got := runTitle(&tc.run); got != tc.want {
			t.Errorf("%s: runTitle = %q, want %q", name, got, tc.want)
		}
	}
}
