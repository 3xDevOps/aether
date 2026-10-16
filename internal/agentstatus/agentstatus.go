// Package agentstatus normalizes native CLI execution and correlated input requests.
package agentstatus

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
)

// State describes execution, independently of outstanding input requests.
type State string

const (
	Working State = "working"
	// Idle retains the existing wire value; it does not imply a human wait.
	Idle State = "waiting"
)

func ParseState(s string) (State, bool) {
	switch State(s) {
	case Working:
		return Working, true
	case Idle:
		return Idle, true
	}
	return "", false
}

const (
	ReasonIdle    = "agent idle"
	ReasonResumed = "agent resumed"
	ReasonFailed  = "agent failed"
	// ReasonBackground is a working run's reason while its agent works with
	// no prompt open (web/src/lib/status.ts).
	ReasonBackground = "working in background"
)

// BackgroundReason is ReasonBackground with ": 1 task" or ": N tasks"
// appended while the agent waits for background tasks.
func BackgroundReason(tasks int) string {
	switch tasks {
	case 0:
		return ReasonBackground
	case 1:
		return ReasonBackground + ": 1 task"
	}
	return fmt.Sprintf("%s: %d tasks", ReasonBackground, tasks)
}

// HeldTask classifies a Claude Code background task by its type: held says
// the agent waits for it, agent that it is one of the agent's own subagents,
// workflows or teammates rather than a tool such as a monitor. A shell, such
// as a dev server, may never end, so nothing waits for it.
func HeldTask(typ string) (held, agent bool) {
	if typ == "local_bash" {
		return false, false
	}
	return true, strings.Contains(typ, "agent") || strings.Contains(typ, "workflow") || strings.Contains(typ, "teammate")
}

// An input-only report preserves the last execution state. SessionID is the
// agent's top-level session, not a subagent's. UsageLimit marks a turn that
// stopped on the account's subscription usage limit; Reason is then the
// agent's own message for it.
type Report struct {
	State        State                   `json:"state,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
	InputUpdates []domain.RunInputUpdate `json:"input_updates,omitempty"`
	SessionID    string                  `json:"session_id,omitempty"`
	UsageLimit   bool                    `json:"usage_limit,omitempty"`
	// Tools says the background tasks a Working report waits for are all
	// tools, none of them the agent's own subagents or workflows.
	Tools bool `json:"tools,omitempty"`
	// Subagent marks a subagent's activity: it keeps the run from stalling
	// and says nothing about the main agent's turn.
	Subagent bool `json:"subagent,omitempty"`
	// TurnStart marks a Working report sent as a prompt starts a turn, also
	// one that arrives while the agent already works.
	TurnStart bool `json:"turn_start,omitempty"`
}

const ReporterCommand = coordtransport.BinaryPath
const ClaudeSettingsName = "claude-settings.json"

//go:embed claude-settings.json
var ClaudeSettings []byte

const OpenCodePluginName = "opencode-status.js"

//go:embed opencode-status.js
var OpenCodePlugin []byte

const OpenCodeV2PluginName = "opencode-status-v2.js"

//go:embed opencode-status-v2.js
var OpenCodeV2Plugin []byte

type claudeHook struct {
	Event           string `json:"hook_event_name"`
	SessionID       string `json:"session_id"`
	AgentID         string `json:"agent_id"`
	Tool            string `json:"tool_name"`
	ToolUseID       string `json:"tool_use_id"`
	ElicitationID   string `json:"elicitation_id"`
	IsInterrupt     bool   `json:"is_interrupt"`
	BackgroundTasks []struct {
		Type string `json:"type"`
	} `json:"background_tasks"`
	// Error and LastAssistantMessage are StopFailure's; the message is the
	// API error Claude Code showed, read only for a usage limit.
	Error                string `json:"error"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// FromClaudeHook reads identifiers only, never tool inputs, answers or
// transcripts, plus the usage-limit message of a StopFailure.
// PermissionRequest explicitly lacks tool_use_id; Notification is advisory.
// Neither can establish a correlated request using the command-hook contract.
func FromClaudeHook(stdin []byte) (Report, bool) {
	var hook claudeHook
	if err := json.Unmarshal(stdin, &hook); err != nil {
		return Report{}, false
	}
	report, ok := fromClaudeHook(hook)
	if ok {
		report.SessionID = hook.SessionID
	}
	return report, ok
}

func fromClaudeHook(hook claudeHook) (Report, bool) {
	session := hook.SessionID
	if session != "" && hook.AgentID != "" {
		// JSON tuple encoding avoids collisions between parent and child IDs.
		scope, _ := json.Marshal([2]string{session, hook.AgentID})
		session = string(scope)
	}
	input := func(operation, kind, id string) (Report, bool) {
		if session == "" || id == "" {
			return Report{}, false
		}
		return Report{InputUpdates: []domain.RunInputUpdate{{
			Operation: operation, SessionID: session, Kind: kind, ID: id,
		}}}, true
	}
	// A subagent's hooks say nothing about the main agent's turn: one that
	// runs in the background works on after the turn that started it ended.
	working := Report{State: Working}
	if hook.AgentID != "" {
		working = Report{Subagent: true}
	}
	switch hook.Event {
	case "UserPromptSubmit":
		return Report{State: Working, TurnStart: true}, true
	case "SubagentStart":
		return Report{Subagent: true}, true
	case "PreToolUse":
		if hook.Tool == "AskUserQuestion" {
			return input("open", "question", hook.ToolUseID)
		}
		return working, true
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		report := working
		if hook.IsInterrupt {
			report.State = ""
		}
		if hook.Tool == "AskUserQuestion" {
			closed, _ := input("close", "question", hook.ToolUseID)
			report.InputUpdates = closed.InputUpdates
		}
		return report, report.State != "" || report.Subagent || len(report.InputUpdates) != 0
	case "Elicitation":
		return input("open", "form", hook.ElicitationID)
	case "ElicitationResult":
		return input("close", "form", hook.ElicitationID)
	case "Stop", "StopFailure", "SubagentStop":
		if hook.AgentID != "" || hook.Event == "SubagentStop" {
			return Report{}, false
		}
		held, agents := 0, 0
		for _, task := range hook.BackgroundTasks {
			h, agent := HeldTask(task.Type)
			if h {
				held++
			}
			if agent {
				agents++
			}
		}
		if held > 0 {
			return Report{State: Working, Reason: BackgroundReason(held), Tools: agents == 0}, true
		}
		if hook.Event == "Stop" {
			return Report{State: Idle, Reason: ReasonIdle}, true
		}
		// "rate_limit" also covers transient 429s; only the vendor's
		// usage-limit wording marks the account's limit.
		if hook.Error == "rate_limit" && IsUsageLimitMessage(hook.LastAssistantMessage) {
			return Report{State: Idle, Reason: hook.LastAssistantMessage, UsageLimit: true}, true
		}
		return Report{State: Idle, Reason: ReasonFailed}, true
	case "SessionEnd":
		if hook.SessionID == "" {
			return Report{}, false
		}
		if hook.AgentID != "" {
			return Report{InputUpdates: []domain.RunInputUpdate{{Operation: "clear", SessionID: session}}}, true
		}
		return Report{State: Idle, Reason: ReasonIdle, InputUpdates: []domain.RunInputUpdate{{
			Operation: "replace", Requests: []domain.RunInputRequest{},
		}}}, true
	}
	return Report{}, false
}

// Codex remains on the native CLI's legacy notify mechanism, not app-server.
const CodexNotifySetting = `notify=["` + ReporterCommand + `","report","codex"]`

func FromCodexNotify(arg string) (Report, bool) {
	var notify struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread-id"`
	}
	if err := json.Unmarshal([]byte(arg), &notify); err != nil {
		return Report{}, false
	}
	if notify.Type == "agent-turn-complete" {
		return Report{State: Idle, Reason: ReasonIdle, SessionID: notify.ThreadID}, true
	}
	return Report{}, false
}

const PiExtensionName = "status.ts"

//go:embed status.ts
var PiExtension []byte

// usageLimitPrefixes is USAGE_LIMIT_ERROR_PREFIXES as exported by
// @anthropic-ai/claude-agent-sdk in sdk.d.ts: Claude Code starts every
// subscription usage-limit message with one of these.
var usageLimitPrefixes = [...]string{
	"You've hit your",
	"You've reached your",
	"You're out of usage credits",
	"Your org is out of usage · add funds to continue",
	"Your org is out of usage · contact your admin",
	"Your seat type doesn't include usage credits",
	"Your seat type doesn't include usage",
	"Your usage allocation has been disabled by your admin",
	"Your group's usage limit is set to $0",
	"Fable 5 requires usage credits",
	"You're out of extra usage",
	"Your seat type doesn't include extra usage",
}

// IsUsageLimitMessage reports whether text is Claude Code's wording for a
// subscription usage limit.
func IsUsageLimitMessage(text string) bool {
	for _, p := range usageLimitPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}
