package push

import (
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// These mirror the scheduler's reasons: acp*Reason in
// internal/scheduler/acp_driver.go, reported*Reason in
// internal/scheduler/reported.go, and the blocked and stalled prefixes.
var enhancedFailurePrefixes = []string{
	"enhanced session failed: ",
	"enhanced session ended: ",
	"enhanced turn failed: ",
}

const (
	blockedPrefix   = "blocked: "
	stalledPrefix   = "stalled:"
	reportedPrefix  = "agent reported "
	reportedSuccess = reportedPrefix + "success"
	reportedFailure = reportedPrefix + "failure"

	// fallbackTitleRunes matches fallbackLabelLength in web/src/lib/status.ts.
	fallbackTitleRunes = 120
	// maxBodyRunes keeps a notification inside one push message whatever an
	// agent put in its reason.
	maxBodyRunes = 240
)

// need is one reason a run waits on its owner. key identifies the standing
// state, so the same one is announced once.
type need struct {
	key  string
	body string
}

// needOf is the owner's rows of the dashboard's Needs you table
// (web/src/lib/needs-you.ts), in its order, from what the server knows about
// the run alone. A swarm worker's integrator answers for it, so a worker
// counts only when it reports itself blocked.
func needOf(run *domain.Run, approval *store.Approval, inputs []domain.RunInputRequest, paused bool) *need {
	worker := run.MissionRole == "worker"
	background := run.Mode == domain.LaunchHeadless
	if !worker {
		if approval != nil {
			return &need{key: "approval:" + approval.ID, body: clip("Permission: "+approval.Action, maxBodyRunes)}
		}
		if request := firstInput(inputs, "permission"); request != nil {
			body := "Permission: answer in the terminal"
			if run.ACP {
				body = "Permission requested"
			}
			return &need{key: inputKey(request), body: body}
		}
		if request := firstInput(inputs, "question", "form", "extension_ui"); request != nil && !background {
			body := "Question: answer in the terminal"
			if run.ACP {
				body = "Question from the agent"
			}
			return &need{key: inputKey(request), body: body}
		}
	}
	body := statusBody(run, worker, background, paused)
	if body == "" {
		return nil
	}
	changed := ""
	if run.StatusChangedAt != nil {
		changed = strconv.FormatInt(run.StatusChangedAt.UnixNano(), 10)
	}
	return &need{
		key:  "status:" + string(run.Status) + "|" + run.Reason + "|" + changed,
		body: clip(body, maxBodyRunes),
	}
}

func statusBody(run *domain.Run, worker, background, paused bool) string {
	switch run.Status {
	case domain.RunNeedsAttention:
		if rest, ok := strings.CutPrefix(run.Reason, blockedPrefix); ok {
			if worker {
				return "Worker blocked: " + rest
			}
			return "Blocked: " + rest
		}
		if worker {
			return ""
		}
		for _, prefix := range enhancedFailurePrefixes {
			if rest, ok := strings.CutPrefix(run.Reason, prefix); ok {
				return "Enhanced unavailable: " + rest
			}
		}
		if background {
			return ""
		}
		switch {
		case run.Reason == reportedSuccess || run.Reason == reportedFailure:
			if !run.OutcomeUnseen {
				return ""
			}
			return "Agent reported " + strings.TrimPrefix(run.Reason, reportedPrefix) + ", review the result"
		case paused:
			return ""
		case strings.HasPrefix(run.Reason, stalledPrefix):
			return "No activity"
		case run.ACP:
			return "Waiting for your reply"
		}
		return "Agent idle"
	case domain.RunCompleted, domain.RunFailed:
		if worker || !run.OutcomeUnseen {
			return ""
		}
		if run.Status == domain.RunFailed {
			return "Failed, review the result"
		}
		return "Finished, review the result"
	}
	return ""
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

// runTitle names a run the way the dashboard does (runLabel in
// web/src/lib/status.ts): its title, else the first line of its task.
func runTitle(run *domain.Run) string {
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
