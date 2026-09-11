// Package agentstatus is what an agent says about itself. A harness that
// can run a command on its own lifecycle events - Claude Code's hooks, an
// opencode plugin - is given one that calls back into the run's
// coordination socket, and this package turns that callback's payload into
// the two states Aether shows a member: the agent is working, or the agent
// needs them.
//
// It is a leaf: the mapping functions are pure, and the only other thing
// here is the harness-side asset the server writes into the run's
// coordination directory so the callback exists at all.
package agentstatus

import (
	_ "embed"
	"encoding/json"
)

// State is what the agent last said it was doing. The zero value means no
// report has arrived, which is what every harness without a reporter stays
// at for the life of the run.
type State string

const (
	// Working: the agent is executing a turn.
	Working State = "working"
	// Waiting: the agent needs the member - its turn ended, or it is
	// asking for permission or an answer.
	Waiting State = "waiting"
)

// ParseState accepts exactly the two states that travel on the wire.
func ParseState(s string) (State, bool) {
	switch State(s) {
	case Working:
		return Working, true
	case Waiting:
		return Waiting, true
	}
	return "", false
}

// The reasons a report carries. They are user-visible verbatim: they land
// in the run's status reason, which is what the run card, the run header
// and `aether runs` show.
const (
	ReasonInput      = "waiting for your input"
	ReasonPermission = "waiting for your permission"
	ReasonAnswer     = "waiting for your answer"
	// ReasonResumed is the reason a Working report un-parks a run with.
	ReasonResumed = "agent resumed"
)

// Report is one agent status report.
type Report struct {
	State  State
	Reason string
}

// ClaudeSettingsName is the file the server writes into the run's
// coordination directory and points Claude Code at with --settings.
const ClaudeSettingsName = "claude-settings.json"

// ClaudeSettings is that file: a Claude Code settings document whose only
// key is hooks, registering the reporter on every event that tells Aether
// whether the agent is working or waiting. The command path is the staged
// server binary inside the run container (internal/mcpbridge.BinaryPath).
//
//go:embed claude-settings.json
var ClaudeSettings []byte

// OpenCodePluginName is the file the server writes into the run's
// coordination directory and names in opencode's OPENCODE_CONFIG_CONTENT.
const OpenCodePluginName = "opencode-status.js"

// OpenCodePlugin is that file: an opencode plugin that runs the reporter
// on the events below. It spawns the staged server binary inside the run
// container (internal/mcpbridge.BinaryPath).
//
//go:embed opencode-status.js
var OpenCodePlugin []byte

// claudeHook is the subset of Claude Code's hook payload the mapping
// reads. Every event carries hook_event_name; tool_name comes with the
// tool events and notification_type with Notification.
type claudeHook struct {
	Event        string `json:"hook_event_name"`
	Tool         string `json:"tool_name"`
	Notification string `json:"notification_type"`
}

// FromClaudeHook maps one Claude Code hook payload onto a report. The
// second result is false for anything that says nothing about whether the
// agent is working or waiting - a session opening or closing, a subagent,
// a compaction, an unparsable body, an event a newer CLI invented - and
// the caller then reports nothing at all rather than guessing.
func FromClaudeHook(stdin []byte) (Report, bool) {
	var hook claudeHook
	if err := json.Unmarshal(stdin, &hook); err != nil {
		return Report{}, false
	}
	switch hook.Event {
	case "UserPromptSubmit", "PostToolUse", "PostToolUseFailure":
		return Report{State: Working}, true
	case "PreToolUse":
		// The question tool is the agent handing the turn back: it blocks
		// on an answer the member has to give.
		if hook.Tool == "AskUserQuestion" {
			return Report{State: Waiting, Reason: ReasonAnswer}, true
		}
		return Report{State: Working}, true
	case "PermissionRequest":
		return Report{State: Waiting, Reason: ReasonPermission}, true
	case "Notification":
		switch hook.Notification {
		case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog":
			return Report{State: Waiting, Reason: ReasonPermission}, true
		case "idle_prompt":
			return Report{State: Waiting, Reason: ReasonInput}, true
		}
		return Report{}, false
	case "Stop", "StopFailure":
		return Report{State: Waiting, Reason: ReasonInput}, true
	}
	return Report{}, false
}

// FromOpenCodeEvent maps one opencode event onto a report. status is the
// status type a session.status event carries and is empty for every other
// event. The second result is false for anything that says nothing about
// whether the agent is working or waiting - a session being created, a
// message part streaming in, an event a newer opencode invented.
//
// A permission or question that has been answered maps back to Working
// because opencode never says so itself: the session is busy for the whole
// tool call the prompt interrupted, so no new session.status arrives to
// un-park the run when the member answers.
func FromOpenCodeEvent(event, status string) (Report, bool) {
	switch event {
	case "session.status":
		// idle travels as session.idle as well, and that is where the
		// plugin decides whether the run's own turn ended.
		if status == "busy" {
			return Report{State: Working}, true
		}
	case "session.idle":
		return Report{State: Waiting, Reason: ReasonInput}, true
	case "permission.asked":
		return Report{State: Waiting, Reason: ReasonPermission}, true
	case "question.asked":
		return Report{State: Waiting, Reason: ReasonAnswer}, true
	case "permission.replied", "question.replied", "question.rejected":
		return Report{State: Working}, true
	}
	return Report{}, false
}
