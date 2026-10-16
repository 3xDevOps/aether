package push

import (
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// These mirror the scheduler's reasons: acp*Reason in
// internal/scheduler/acp_turn.go, reported*Reason in
// internal/scheduler/reported.go, usageLimitReason in
// internal/scheduler/usage_fallback.go, and the blocked and needs-input
// prefixes.
// The labels are the dashboard's (web/src/lib/needs-you.ts).
var disconnects = []struct{ prefix, label string }{
	{"enhanced session failed: ", "Enhanced could not start: "},
	{"enhanced session ended: ", "Agent disconnected: "},
}

const (
	needsInputPrefix = "needs input: "
	usageLimitPrefix = "Usage limit reached on "
	turnFailedPrefix = "enhanced turn failed: "
	reportedPrefix   = "agent reported "
	reportedFailure  = reportedPrefix + "failure"

	// fallbackTitleRunes matches fallbackLabelLength in web/src/lib/status.ts.
	fallbackTitleRunes = 120
	// maxBodyRunes keeps a notification inside one push message whatever an
	// agent put in its reason.
	maxBodyRunes = 240
)

// need is one reason a run waits on its owner. key identifies the standing
// state, so the same one is announced once. since is when the run entered a
// standing status need, zero for a request.
type need struct {
	key     string
	body    string
	urgency string
	since   time.Time
}

// needOf is the owner's rows of the dashboard's Needs you table
// (web/src/lib/needs-you.ts), in its order, from what the server knows about
// the run alone. A swarm worker's integrator answers for it, so a worker
// counts only when it reports itself blocked (domain.Run.NeedsAttention).
func needOf(run *domain.Run, approval *store.Approval, inputs []domain.RunInputRequest, paused bool) *need {
	if paused {
		return nil
	}
	worker := run.MissionRole == "worker"
	background := run.Mode == domain.LaunchHeadless
	if !worker {
		if approval != nil {
			return &need{key: "approval:" + approval.ID, body: clip("Permission: "+approval.Action, maxBodyRunes), urgency: urgencyHigh}
		}
		if request := firstInput(inputs, "permission"); request != nil {
			body := "Permission: answer in the terminal"
			if run.ACP {
				body = "Permission requested"
			}
			return &need{key: inputKey(request), body: body, urgency: urgencyHigh}
		}
		if request := firstInput(inputs, "question", "form", "extension_ui"); request != nil && !background {
			body := "Question: answer in the terminal"
			if run.ACP {
				body = "Question from the agent"
			}
			return &need{key: inputKey(request), body: body, urgency: urgencyHigh}
		}
	}
	body := statusBody(run, worker)
	if body == "" {
		return nil
	}
	n := &need{key: "status:" + run.NeedKey(), body: clip(body, maxBodyRunes), urgency: urgencyHigh}
	if run.Activity == domain.ActivityFinished || run.Status == domain.RunCompleted {
		n.urgency = urgencyNormal
	}
	if run.StatusChangedAt != nil {
		n.since = *run.StatusChangedAt
	}
	return n
}

func AttentionBody(run *domain.Run) string {
	return clip(statusBody(run, run.MissionRole == "worker"), maxBodyRunes)
}

// statusBody announces a run only when it needs a member
// (domain.Run.NeedsAttention) or finished with a result nobody has seen.
// Idle, waiting and working never do.
func statusBody(run *domain.Run, worker bool) string {
	if !run.NeedsAttention(false) {
		if worker || !run.OutcomeUnseen {
			return ""
		}
		switch {
		case run.Activity == domain.ActivityFinished:
			return "Agent reported " + strings.TrimPrefix(run.Reason, reportedPrefix) + ", review the result"
		case run.Status == domain.RunFailed:
			return "Failed, review the result"
		case run.Status == domain.RunCompleted:
			return "Finished, review the result"
		}
		return ""
	}
	switch run.Activity {
	case domain.ActivityNeedsYou:
		if rest, ok := strings.CutPrefix(run.Reason, domain.BlockedReasonPrefix); ok {
			if worker {
				return "Worker blocked: " + rest
			}
			return "Blocked: " + rest
		}
		if rest, ok := strings.CutPrefix(run.Reason, needsInputPrefix); ok {
			return rest
		}
		if strings.HasPrefix(run.Reason, usageLimitPrefix) {
			return run.Reason
		}
		return "Waiting for your reply"
	case domain.ActivityDisconnected:
		for _, d := range disconnects {
			if rest, ok := strings.CutPrefix(run.Reason, d.prefix); ok {
				return d.label + rest
			}
		}
		return "Agent disconnected"
	}
	if rest, ok := strings.CutPrefix(run.Reason, turnFailedPrefix); ok {
		return "Turn failed: " + rest
	}
	if run.Reason != reportedFailure {
		return "Agent failed"
	}
	return "Agent reported failure, review the result"
}

func firstInput(inputs []domain.RunInputRequest, kinds ...string) *domain.RunInputRequest {
	for i := range inputs {
		for _, kind := range kinds {
			if inputs[i].Kind == kind {
				return &inputs[i]
			}
		}
	}
	return nil
}

func inputKey(request *domain.RunInputRequest) string {
	return "input:" + request.SessionID + "|" + request.Kind + "|" + request.ID
}

// RunTitle names a run the way the dashboard does (runLabel in
// web/src/lib/status.ts): its title, else the first line of its task.
func RunTitle(run *domain.Run) string {
	if title := strings.TrimSpace(run.Title); title != "" {
		return clip(title, fallbackTitleRunes)
	}
	for line := range strings.Lines(run.Task) {
		if line = strings.TrimSpace(line); line != "" {
			return clip(line, fallbackTitleRunes)
		}
	}
	return "Untitled run"
}

func clip(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}
