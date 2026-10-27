package sshd

import (
	"regexp"
	"strings"
)

// These mirror web/src/lib/reasons.ts, which words the reasons
// internal/scheduler, internal/agentstatus and internal/coord write.
var stopReasons = map[string]string{
	"max_tokens":        "the reply reached its token limit",
	"max_turn_requests": "too many model requests in one turn",
	"refusal":           "the agent refused",
}

var turnEnds = map[string]string{
	"end_turn":  "The agent finished its turn",
	"cancelled": "The turn was cancelled",
}

const savedResults = "the worker's saved results"

func turnEnded(stop string) string {
	if why, ok := stopReasons[stop]; ok {
		return "Stopped: " + why
	}
	if words, ok := turnEnds[stop]; ok {
		return words
	}
	return "The agent's turn ended (" + stop + ")"
}

var plainReasons = []struct {
	pattern *regexp.Regexp
	words   func(groups []string) string
}{
	{regexp.MustCompile(`^agent reported success$`), func([]string) string { return "Agent reported success" }},
	{regexp.MustCompile(`^agent reported failure$`), func([]string) string { return "Agent reported failure" }},
	{regexp.MustCompile(`^agent exited; results committed$`), func([]string) string { return "Agent exited and its changes were committed" }},
	{regexp.MustCompile(`(?s)^enhanced session failed: (.*)$`), func(g []string) string { return "Enhanced could not start: " + g[0] }},
	{regexp.MustCompile(`(?s)^enhanced session ended: (.*)$`), func(g []string) string { return "Agent disconnected: " + g[0] }},
	{regexp.MustCompile(`(?s)^enhanced turn failed: (.*)$`), func(g []string) string { return "Turn failed: " + g[0] }},
	{regexp.MustCompile(`(?s)^agent exited (-?\d+)(.*)$`), func(g []string) string { return "Agent exited with code " + g[0] + g[1] }},
	{regexp.MustCompile(`^worker finished$`), func([]string) string { return "Worker finished" }},
	{regexp.MustCompile(`^closed$`), func([]string) string { return "Closed" }},
	{regexp.MustCompile(`^killed$`), func([]string) string { return "Stopped" }},
	{regexp.MustCompile(`^retained container expired$`), func([]string) string { return "Its container was removed when it expired" }},
	{regexp.MustCompile(`^retained container reclaimed for disk space$`), func([]string) string { return "Its container was removed early because disk space was low" }},
	{regexp.MustCompile(`^retained container unavailable$`), func([]string) string { return "Its container is no longer available" }},
	{regexp.MustCompile(`^server restarted: container exited (-?\d+)$`), func(g []string) string {
		return "Its container had stopped with exit code " + g[0] + " when the server restarted"
	}},
	{regexp.MustCompile(`^(?:agent idle|turn ended without a report)$`), func([]string) string { return "Turn ended without a report" }},
	{regexp.MustCompile(`(?s)^no update for ([^\s:]+)(: .*)?$`), func(g []string) string { return "No update for " + g[0] + g[1] }},
	{regexp.MustCompile(`^waiting for (\d+ workers?)$`), func(g []string) string { return "Waiting for " + g[0] }},
	{regexp.MustCompile(`^agent resumed$`), func([]string) string { return "Agent resumed" }},
	{regexp.MustCompile(`^agent failed$`), func([]string) string { return "Agent failed" }},
	{regexp.MustCompile(`^stalled: no output or file changes for (.+)$`), func(g []string) string { return "No output or file changes for " + g[0] }},
	{regexp.MustCompile(`(?s)^blocked: (.*)$`), func(g []string) string { return "Blocked: " + g[0] }},
	{regexp.MustCompile(`(?s)^needs input: (.*)$`), func(g []string) string { return "Agent asks: " + g[0] }},
	{regexp.MustCompile(`^working in background(: \d+ tasks?)?$`), func(g []string) string { return "Working in background" + g[0] }},
	{regexp.MustCompile(`^turn cancelled$`), func([]string) string { return "Turn cancelled" }},
	{regexp.MustCompile(`^turn interrupted$`), func([]string) string { return "Turn interrupted" }},
	{regexp.MustCompile(`^turn ended: (\w+)$`), func(g []string) string { return turnEnded(g[0]) }},
	{regexp.MustCompile(`^the agent's turn ended: (\w+)$`), func(g []string) string { return turnEnded(g[0]) }},
	{regexp.MustCompile(`(?s)^send the task to the agent: (.*)$`), func(g []string) string { return "Could not send the task to the agent: " + g[0] }},
	{regexp.MustCompile(`^review retained evidence$`), func([]string) string { return "Review " + savedResults }},
	{regexp.MustCompile(`^resolve the blocker using the retained evidence$`), func([]string) string { return "Resolve the blocker using " + savedResults }},
	{regexp.MustCompile(`(?s)^retained evidence (?:is )?unavailable(.*)$`), func(g []string) string { return "The worker's saved results are unavailable" + g[0] }},
}

// plainReason is plainReason in web/src/lib/reasons.ts.
func plainReason(reason string) string {
	if rest, ok := strings.CutSuffix(reason, "; retained container"); ok {
		return plainReason(rest)
	}
	for _, r := range plainReasons {
		if m := r.pattern.FindStringSubmatch(reason); m != nil {
			return r.words(m[1:])
		}
	}
	return reason
}

// waitBrokenPrefix mirrors waitBrokenReasonPrefix in
// internal/scheduler/wait.go.
const waitBrokenPrefix = "wait broken: "

func turnCutShort(reason string) bool {
	return reason == "turn interrupted" || strings.HasPrefix(reason, "turn ended: ")
}
