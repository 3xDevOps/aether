package push

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
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
	unseen := func(r *domain.Run) { r.OutcomeUnseen = true }
	review := func(state domain.RunReview) func(*domain.Run) { return func(r *domain.Run) { r.Review = state } }
	question := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	permission := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}, {ID: "p1", SessionID: "s1", Kind: "permission"}}
	waiting, needsYou := domain.ActivityWaiting, domain.ActivityNeedsYou

	for _, tc := range []struct {
		name   string
		run    *domain.Run
		inputs []domain.RunInputRequest
		paused bool
		want   string
	}{
		{name: "working", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning, Activity: domain.ActivityWorking}},
		{name: "permission before question", run: parked(needsYou, "turn ended without a report", enhanced), inputs: permission, want: "Permission requested"},
		{name: "terminal permission", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunRunning, Activity: needsYou}, inputs: permission, want: "Permission: answer in the terminal"},
		{name: "enhanced question while working", run: &domain.Run{Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning, Activity: needsYou}, inputs: question, want: "Question from the agent"},
		{name: "terminal question", run: parked(needsYou, "turn ended without a report"), inputs: question, want: "Question: answer in the terminal"},
		{name: "background question", run: parked(waiting, "turn ended without a report", background), inputs: question},
		{name: "background turn failed", run: parked(domain.ActivityFailed, "enhanced turn failed: overloaded", background, unseen), want: "Turn failed: overloaded"},
		{name: "waiting", run: parked(waiting, "turn ended without a report")},
		{name: "legacy idle", run: parked(domain.ActivityIdle, "agent idle")},
		{name: "enhanced turn ended", run: parked(waiting, "turn ended without a report", enhanced)},
		{name: "stalled", run: parked(waiting, "stalled: no output for 10m0s", enhanced)},
		{name: "stale wait", run: parked(waiting, "no update for 30m", enhanced)},
		{name: "turn interrupted", run: parked(needsYou, "turn interrupted", enhanced), want: "Turn interrupted, send a message to continue"},
		{name: "turn stopped short", run: parked(needsYou, "turn ended: max_tokens", enhanced), want: "Turn ended (max_tokens), send a message to continue"},
		{name: "turn cancelled", run: parked(waiting, "turn cancelled", enhanced)},
		{name: "turn failed", run: parked(domain.ActivityFailed, "enhanced turn failed: overloaded", enhanced, unseen), want: "Turn failed: overloaded"},
		{name: "agent failed", run: parked(domain.ActivityFailed, "agent failed", unseen), want: "Agent failed"},
		{name: "failure seen", run: parked(domain.ActivityFailed, "agent failed")},
		{name: "usage limit", run: parked(needsYou, "Usage limit reached on Ada's account; no fallback has usage left"), want: "Usage limit reached on Ada's account; no fallback has usage left"},
		{name: "paused", run: parked(needsYou, "blocked: need the staging API key"), paused: true},
		{name: "reported waiting", run: parked(domain.ActivityWaitingAgents, "waiting on 3 workers", enhanced)},
		{name: "waiting for CI", run: parked(domain.ActivityWaitingCI, "CI on PR 12", enhanced)},
		{name: "wait broken", run: parked(needsYou, "wait broken: pr:o/r#12:checks resolved, but the agent was not woken", enhanced), want: "Wait broken, send a message to continue: pr:o/r#12:checks resolved, but the agent was not woken"},
		{name: "waiting for a process", run: parked(domain.ActivityWaitingTools, "release build", enhanced)},
		{name: "answered", run: parked(domain.ActivityAnswered, "explained the retry policy", enhanced)},
		{name: "waiting on a tool", run: &domain.Run{Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning, Activity: domain.ActivityWaitingTools}},
		{name: "blocked", run: parked(needsYou, "blocked: need the staging API key", background), want: "Blocked: need the staging API key"},
		{name: "needs input", run: parked(needsYou, "needs input: which region?", enhanced), want: "which region?"},
		{name: "enhanced failure", run: parked(domain.ActivityDisconnected, "enhanced session failed: not logged in", background), want: "Enhanced could not start: not logged in"},
		{name: "adapter exited", run: parked(domain.ActivityDisconnected, "enhanced session ended: exit code 1", enhanced), want: "Agent disconnected: exit code 1"},
		{name: "reported success", run: parked(domain.ActivityFinished, "agent reported success", unseen, review(domain.ReviewReady)), want: "Agent reported success, review the result"},
		{name: "reported success seen, still to review", run: parked(domain.ActivityFinished, "agent reported success", review(domain.ReviewReady)), want: "Agent reported success, review the result"},
		{name: "reported success for later", run: parked(domain.ActivityFinished, "agent reported success", unseen, review(domain.ReviewLater))},
		{name: "reported success disconnected", run: parked(domain.ActivityDisconnected, "enhanced session ended: exit code 1", review(domain.ReviewReady)), want: "Agent disconnected: exit code 1"},
		{name: "reported failure", run: parked(domain.ActivityFailed, "agent reported failure", enhanced, unseen), want: "Agent reported failure, review the result"},
		{name: "finished before reviews", run: parked(domain.ActivityFinished, "agent reported success", unseen)},
		{name: "background success", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true, Review: domain.ReviewReady}, want: "Agent reported success, review the result"},
		{name: "background success accepted", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true, Review: domain.ReviewAccepted}},
		{name: "finished without a report", run: &domain.Run{Mode: domain.LaunchHeadless, Status: domain.RunCompleted, OutcomeUnseen: true}},
		{name: "failed", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunFailed, OutcomeUnseen: true}, want: "Failed, review the result"},
		{name: "finished and seen", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunCompleted}},
		{name: "closed by a member", run: &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunAbandoned}},
	} {
		got := needOf(tc.run, tc.inputs, tc.paused)
		switch {
		case got == nil && tc.want != "":
			t.Errorf("%s: no need, want %q", tc.name, tc.want)
		case got != nil && got.body != tc.want:
			t.Errorf("%s: body = %q, want %q", tc.name, got.body, tc.want)
		}
	}
}

// The key is what makes a standing state announce once and a new one
// announce again: opening the run bumps status_seq and keeps the key, a
// later transition into the same state does not.
func TestNeedKeyFollowsTheStandingState(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	later := at.Add(time.Minute)
	blocked := &domain.Run{Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou,
		Reason: "blocked: need a key", StatusSeq: 4, StatusChangedAt: &at}
	opened := *blocked
	opened.StatusSeq, opened.FinishUnopened = 5, false
	again := *blocked
	again.StatusSeq, again.StatusChangedAt = 7, &later
	asked := *blocked
	asked.Reason, asked.StatusSeq = "needs input: which key?", 6
	first := []domain.RunInputRequest{{ID: "q1", SessionID: "s1", Kind: "question"}}
	second := []domain.RunInputRequest{{ID: "q2", SessionID: "s1", Kind: "question"}}

	same := needOf(blocked, nil, false).key
	for name, other := range map[string]*need{
		"blocked again after working": needOf(&again, nil, false),
		"a question":                  needOf(&asked, nil, false),
		"a native question":           needOf(blocked, first, false),
	} {
		if other.key == same {
			t.Errorf("%s has the key of the first block, %q", name, same)
		}
	}
	if repeat := needOf(&opened, nil, false).key; repeat != same {
		t.Errorf("opening the block changed its key from %q to %q", same, repeat)
	}
	if needOf(blocked, first, false).key == needOf(blocked, second, false).key {
		t.Error("two different questions share a key")
	}

	finished := *blocked
	finished.Activity, finished.Reason, finished.OutcomeUnseen, finished.Review = domain.ActivityFinished, "agent reported success", true, domain.ReviewReady
	failed := finished
	failed.Activity, failed.Reason, failed.Review = domain.ActivityFailed, "agent reported failure", ""
	for _, tc := range []struct {
		run  *domain.Run
		want string
	}{{blocked, urgencyHigh}, {&failed, urgencyHigh}, {&finished, urgencyNormal}} {
		if got := needOf(tc.run, nil, false).urgency; got != tc.want {
			t.Errorf("%s: urgency = %q, want %q", tc.run.Activity, got, tc.want)
		}
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
