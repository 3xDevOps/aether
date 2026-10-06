// Package agentstatus normalizes native CLI execution and correlated input requests.
package agentstatus

import (
	_ "embed"
	"encoding/json"

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
)

// An input-only report preserves the last execution state. SessionID is the
// agent's own top-level session, the one a mode switch resumes.
type Report struct {
	State        State                   `json:"state,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
	InputUpdates []domain.RunInputUpdate `json:"input_updates,omitempty"`
	SessionID    string                  `json:"session_id,omitempty"`
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
	Event           string     `json:"hook_event_name"`
	SessionID       string     `json:"session_id"`
	AgentID         string     `json:"agent_id"`
	Tool            string     `json:"tool_name"`
	ToolUseID       string     `json:"tool_use_id"`
	ElicitationID   string     `json:"elicitation_id"`
	IsInterrupt     bool       `json:"is_interrupt"`
	BackgroundTasks []struct{} `json:"background_tasks"`
}

// FromClaudeHook reads identifiers only, never tool inputs, answers or transcripts.
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
	switch hook.Event {
	case "UserPromptSubmit", "SubagentStart":
		return Report{State: Working}, true
	case "PreToolUse":
		if hook.Tool == "AskUserQuestion" {
			return input("open", "question", hook.ToolUseID)
		}
		return Report{State: Working}, true
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		report := Report{State: Working}
		if hook.IsInterrupt {
			report.State = ""
		}
		if hook.Tool == "AskUserQuestion" {
			closed, _ := input("close", "question", hook.ToolUseID)
			report.InputUpdates = closed.InputUpdates
		}
		return report, report.State != "" || len(report.InputUpdates) != 0
	case "Elicitation":
		return input("open", "form", hook.ElicitationID)
	case "ElicitationResult":
		return input("close", "form", hook.ElicitationID)
	case "Stop", "StopFailure":
		if hook.AgentID != "" {
			return Report{}, false
		}
		if len(hook.BackgroundTasks) > 0 {
			return Report{State: Working}, true
		}
		reason := ReasonIdle
		if hook.Event == "StopFailure" {
			reason = "agent failed"
		}
		return Report{State: Idle, Reason: reason}, true
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
