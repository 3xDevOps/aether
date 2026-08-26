// Package protocol is the shared client/server wire surface: the JSON-RPC
// 2.0 envelope, method names, param/result types, error codes, subsystem
// names, and wire DTOs, framed as NDJSON (one JSON object per newline-
// terminated line). It is pure data plus framing — the CLI imports it
// unchanged — so it depends only on the standard library and
// internal/domain.
package protocol

import (
	"encoding/json"
	"fmt"
)

const (
	// Version is the protocol version reported by server.info; the CLI
	// refuses to talk to a server with a different one.
	Version = "2"

	// SubsystemControl is the JSON-RPC control channel.
	SubsystemControl = "aether-control"
	// SubsystemEvents is the event stream channel.
	SubsystemEvents = "aether-events"
	// SubsystemAttach is the raw PTY attach channel.
	SubsystemAttach = "aether-attach"
	// SubsystemWorkspaceShell is the unified bootstrap and harness-login
	// channel.
	SubsystemWorkspaceShell = "aether-workspace-shell"
	// SubsystemSync is the live file-overlay channel: one mutagen remote
	// endpoint stream bridging a member's local directory to a run
	// worktree.
	SubsystemSync = "aether-sync"
)

// MaxLineBytes is the maximum length of one NDJSON line, framing included.
// 32 MiB covers the base64 form of the valid 20 MiB aggregate profile cap.
const MaxLineBytes = 32 << 20

// Control-channel method names.
const (
	MethodServerInfo    = "server.info"
	MethodWorkspaceList = "workspace.list"
	MethodSessionList   = "session.list"
	MethodSessionGet    = "session.get"
	MethodMemberList    = "member.list"
	MethodMemberApprove = "member.approve"
	MethodMemberInvite  = "member.invite"
	MethodMemberRemove  = "member.remove"
	MethodMemberColor   = "member.color"
	MethodWorkspaceAdd  = "workspace.add"
	MethodSessionNew    = "session.new"
	MethodRunLaunch     = "run.launch"
	MethodRunList       = "run.list"
	MethodRunGet        = "run.get"
	MethodRunKill       = "run.kill"
	MethodRunPause      = "run.pause"
	MethodRunResume     = "run.resume"
	MethodRunInject     = "run.inject"
	MethodRunClose      = "run.close"
	MethodRunRelaunch   = "run.relaunch"
	MethodRunHandoff    = "run.handoff"
	MethodRunPull       = "run.pull"
)

// Workspace tool snapshot control methods.
const (
	MethodWorkspaceToolsList     = "workspace.tools.list"
	MethodWorkspaceToolsVerify   = "workspace.tools.verify"
	MethodWorkspaceToolsRollback = "workspace.tools.rollback"
	MethodWorkspaceToolsReset    = "workspace.tools.reset"
)

// Custom agent (harness) onboarding methods.
const (
	MethodAgentRegister = "agent.register"
	MethodAgentList     = "agent.list"
)

// Wave 3 permission-model methods.
const (
	// MethodSessionSettings updates session settings (admin only).
	MethodSessionSettings = "session.settings"
	// MethodRunProtect toggles a run's protected flag (owner or admin).
	MethodRunProtect = "run.protect"
	// MethodSyncConflict reports a live-overlay sync conflict so both
	// affected members are notified via the event feed (Steer-gated,
	// like the sync bridge itself).
	MethodSyncConflict = "sync.conflict"
)

// JSON-RPC 2.0 error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeNotFound     = -32000 // unknown run/session/workspace/member
	CodeDenied       = -32001 // write-gate / permission denial
	CodeInvalidState = -32002 // invalid lifecycle transition, conflict-free misuse
	CodeConflict     = -32003 // store conflict / in-use
	CodeUnavailable  = -32004 // run has no live PTY session / agent gone
)

// Request is one JSON-RPC 2.0 request. Clients send requests only, every
// one carrying an ID; there are no batches and no client notifications.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC 2.0 response; exactly one of Result and Error
// is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC error object. It implements error so clients can
// surface it directly.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}
