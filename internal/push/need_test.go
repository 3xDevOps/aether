package push

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestNeedOf(t *testing.T) {
	t.Parallel()
	parked := func(activity domain.RunActivity, reason string, edit ...func(*domain.Run)) *domain.Run {
		run := &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention, Activity: activity, Reason: reason}
		for _, e := range edit {
			e(run)
		}
		return run
	}
	enhanced := func(r *domain.Run) { r.Mode, r.ACP = domain.LaunchACP, true }
	background := func(r *domain.Run) { r.Mode = domain.LaunchHeadless }
	worker := func(r *domain.Run) { r.MissionRole = "worker" }
	integrator := func(r *domain.Run) { r.MissionRole = "integrator" }
	unseen := func(r *domain.Run) { r.OutcomeUnseen = true }
	question := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	permission := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}, {ID: "p1", SessionID: "s1", Kind: "permission"}}
	approval := &store.Approval{ID: "appr-1", Action: "ExitPlanMode"}
	idle, needsYou := domain.ActivityIdle, domain.ActivityNeedsYou

	for _, tc := range []struct {
		name     string
		run      *domain.Run
		approval *store.Approval
		inputs   []domain.RunInputRequest
		want     string
	}{
		{name: "working", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning, Activity: domain.ActivityWorking}},
		{name: "approval", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunRunning}, approval: approval, want: "Permission: ExitPlanMode"},
		{name: "approval before a native request", run: parked(needsYou, "agent idle"), approval: approval, inputs: question, want: "Permission: ExitPlanMode"},
		{name: "permission before question", run: parked(needsYou, "agent idle", enhanced), inputs: permission, want: "Permission requested"},
		{name: "terminal permission", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning, Activity: needsYou}, inputs: permission, want: "Permission: answer in the terminal"},
		{name: "enhanced question while working", run: &domain.Run{Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning, Activity: needsYou}, inputs: question, want: "Question from the agent"},
		{name: "terminal question", run: parked(needsYou, "agent idle"), inputs: question, want: "Question: answer in the terminal"},
		{name: "background question", run: parked(needsYou, "agent idle", background), inputs: question},
		{name: "worker request", run: parked(needsYou, "agent idle", worker), approval: approval, inputs: permission},
		{name: "idle", run: parked(idle, "agent idle")},
		{name: "enhanced turn ended", run: parked(idle, "agent idle", enhanced)},
		{name: "stalled", run: parked(idle, "stalled: no output for 10m0s", enhanced)},
		{name: "turn failed", run: parked(idle, "enhanced turn failed: overloaded", enhanced)},
		{name: "integrator waiting on workers", run: parked(domain.ActivityWaitingAgents, "agent idle", integrator)},
		{name: "reported waiting", run: parked(domain.ActivityWaitingAgents, "waiting on 3 workers", enhanced)},
		{name: "waiting on a tool", run: &domain.Run{Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning, Activity: domain.ActivityWaitingTools}},
		{name: "blocked", run: parked(needsYou, "blocked: need the staging API key", background), want: "Blocked: need the staging API key"},
		{name: "worker blocked", run: parked(needsYou, "blocked: merge conflict", worker), want: "Worker blocked: merge conflict"},
		{name: "needs input", run: parked(needsYou, "needs input: which region?", enhanced), want: "which region?"},
		{name: "worker needs input", run: parked(needsYou, "needs input: which region?", worker)},
		{name: "enhanced failure", run: parked(domain.ActivityDisconnected, "enhanced session failed: not logged in", background), want: "Enhanced could not start: not logged in"},
		{name: "adapter exited", run: parked(domain.ActivityDisconnected, "enhanced session ended: exit code 1", enhanced), want: "Agent disconnected: exit code 1"},
		{name: "worker disconnected", run: parked(domain.ActivityDisconnected, "enhanced session ended: exit code 1", worker)},
		{name: "reported success", run: parked(domain.ActivityFinished, "agent reported success", unseen), want: "Agent reported success, review the result"},
		{name: "reported failure", run: parked(domain.ActivityFailed, "agent reported failure", enhanced, unseen), want: "Agent reported failure, review the result"},
		{name: "reported and seen", run: parked(domain.ActivityFinished, "agent reported success")},
		{name: "finished", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true}, want: "Finished, review the result"},
		{name: "failed", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunFailed, OutcomeUnseen: true}, want: "Failed, review the result"},
		{name: "finished and seen", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunCompleted}},
		{name: "closed by a member", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunAbandoned}},
		{name: "worker finished", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true, MissionRole: "worker"}},
	} {
		got := needOf(tc.run, tc.approval, tc.inputs)
		switch {
		case got == nil && tc.want != "":
			t.Errorf("%s: no need, want %q", tc.name, tc.want)
		case got != nil && got.body != tc.want:
			t.Errorf("%s: body = %q, want %q", tc.name, got.body, tc.want)
		}
	}
}

// The key is what makes a standing state announce once and a new one
// announce again: a status change that lands in the same activity keeps no
// key of its own, a later transition into it does.
func TestNeedKeyFollowsTheStandingState(t *testing.T) {
	t.Parallel()
	blocked := &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou,
		Reason: "blocked: need a key", StatusSeq: 4}
	again := *blocked
	again.StatusSeq = 6
	asked := *blocked
	asked.Reason, asked.StatusSeq = "needs input: which key?", 5
	first := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	second := []domain.RunInputRequest{{ID: "q2", SessionID: "s1", Kind: "question"}}

	same := needOf(blocked, nil, nil).key
	for name, other := range map[string]*need{
		"blocked again after working": needOf(&again, nil, nil),
		"a question":                  needOf(&asked, nil, nil),
		"a native question":           needOf(blocked, nil, first),
	} {
		if other.key == same {
			t.Errorf("%s has the key of the first block, %q", name, same)
		}
	}
	if repeat := needOf(blocked, nil, nil).key; repeat != same {
		t.Errorf("the same block has keys %q and %q", same, repeat)
	}
	if needOf(blocked, nil, first).key == needOf(blocked, nil, second).key {
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
		if got := RunTitle(&tc.run); got != tc.want {
			t.Errorf("%s: runTitle = %q, want %q", name, got, tc.want)
		}
	}
}
