// Package protocol is the client/server wire surface: JSON-RPC 2.0 framed as
// NDJSON. The CLI imports it, so it depends only on the standard library and
// internal/domain.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// Version is the protocol version reported by server.info; the CLI
	// refuses to talk to a server with a different one.
	Version = "3"

	// SubsystemControl is the JSON-RPC control channel.
	SubsystemControl = "aether-control"
	// SubsystemEvents is the event stream channel.
	SubsystemEvents = "aether-events"
	// SubsystemAttach is the raw PTY attach channel.
	SubsystemAttach = "aether-attach"
	// SubsystemSync carries one mutagen remote endpoint stream into a run
	// worktree.
	SubsystemSync = "aether-sync"
	// SubsystemTerminal is the per-member terminal control and PTY channel.
	SubsystemTerminal = "aether-terminal"

	// WindowChangeRequest is the RFC 4254 resize request. The server also
	// sends it to report a resize by someone else; see docs/local-gateway.md.
	WindowChangeRequest = "window-change"
)

// MaxLineBytes is the maximum length of one NDJSON line, framing included.
// 96 MiB covers base64 encoding and framing for a 64 MiB config import request.
const MaxLineBytes = 96 << 20

// Control-channel method names.
const (
	MethodServerInfo      = "server.info"
	MethodWorkspaceList   = "workspace.list"
	MethodWorkspaceGet    = "workspace.get"
	MethodMemberList      = "member.list"
	MethodMemberApprove   = "member.approve"
	MethodMemberInvite    = "member.invite"
	MethodMemberRemove    = "member.remove"
	MethodMemberColor     = "member.color"
	MethodMemberRename    = "member.rename"
	MethodMemberGit       = "member.git"
	MethodMemberRole      = "member.role"
	MethodAccountList     = "account.list"
	MethodAccountShare    = "account.share"
	MethodAccountRevoke   = "account.revoke"
	MethodWorkspaceAdd    = "workspace.add"
	MethodWorkspaceDelete = "workspace.delete"
	MethodRunLaunch       = "run.launch"
	MethodRunList         = "run.list"
	MethodRunGet          = "run.get"
	MethodRunKill         = "run.kill"
	MethodRunRelease      = "run.release"
	MethodRunDelete       = "run.delete"
	MethodRunPause        = "run.pause"
	MethodRunResume       = "run.resume"
	MethodRunInject       = "run.inject"
	MethodRunClose        = "run.close"
	MethodRunRelaunch     = "run.relaunch"
	MethodRunHandoff      = "run.handoff"
	MethodRunPull         = "run.pull"
	MethodTerminalStatus  = "terminal.status"
	MethodTerminalStop    = "terminal.stop"
	// MethodTerminalExec runs one command in the caller's environment
	// terminal and answers when it ends or TerminalExecTimeout passes.
	MethodTerminalExec = "terminal.exec"
	// MethodTerminalImage stores an image in the target member home and
	// returns the absolute path visible inside its container. An empty run
	// ID targets the caller's environment terminal.
	MethodTerminalImage = "terminal.image"
	// MethodTerminalFile is terminal.image for any file: it keeps the
	// sanitised original name in a generated directory.
	MethodTerminalFile = "terminal.file"
	// MethodEnvSave snapshots the caller's running environment terminal.
	MethodEnvSave = "env.save"
	// MethodEnvReset stops the caller's environment terminal and returns it to the standard image.
	MethodEnvReset = "env.reset"
	// MethodGitHubConnect finishes the GitHub login the caller started
	// with gh auth login in their environment terminal.
	MethodGitHubConnect = "github.connect"
	// MethodGitHubProbe reports the gh in the caller's environment
	// terminal, before they are told to log in with it.
	MethodGitHubProbe = "github.probe"
)

// MethodTerminalHistory reads a bounded page of normalized run output.
const MethodTerminalHistory = "terminal.history"

// Custom agent (harness) onboarding methods.
const (
	MethodAgentRegister = "agent.register"
	MethodAgentList     = "agent.list"
	// MethodAgentInstall answers when the install command ends or
	// AgentInstallTimeout passes, whichever is first.
	MethodAgentInstall = "agent.install"
	MethodAgentModels  = "agent.models"
	// MethodAgentRemove deletes a shipped agent's ~/.local/bin links, or a
	// member agent's definition, in the caller's own environment.
	MethodAgentRemove = "agent.remove"
)

// AgentInstallTimeout bounds one agent.install.
const AgentInstallTimeout = 10 * time.Minute

const TerminalExecTimeout = 10 * time.Minute

// Permission-model methods.
const (
	// MethodWorkspaceSettings updates workspace settings (admin only).
	MethodWorkspaceSettings = "workspace.settings"
	// MethodWorkspaceOrigin sets the upstream git URL run checkouts push to.
	MethodWorkspaceOrigin = "workspace.origin"
	// MethodRunProtect toggles a run's protected flag (owner or admin).
	MethodRunProtect = "run.protect"
	// MethodRunArchive hides or restores a finished run; its data is
	// untouched (same gate as run.delete: Kill).
	MethodRunArchive = "run.archive"
	// MethodRunSeen records that the caller opened a run: it clears
	// finish_unopened, and outcome_unseen too when the caller owns the run.
	MethodRunSeen = "run.seen"
	// MethodRunReview records what the owner did with a reported success:
	// complete accepts it, closing a live run as completed; later puts it
	// off (Kill-gated, like run.close).
	MethodRunReview = "run.review"
	// MethodSyncConflict notifies both affected members via the event feed
	// (Steer-gated, like the sync bridge).
	MethodSyncConflict = "sync.conflict"
)

// Workspace mirror administration methods (admin only).
const (
	MethodWorkspaceMirrorStatus    = "workspace.mirror.status"
	MethodWorkspaceMirrorConfigure = "workspace.mirror.configure"
	MethodWorkspaceMirrorRefresh   = "workspace.mirror.refresh"
	MethodWorkspaceMirrorAdopt     = "workspace.mirror.adopt"
	MethodWorkspaceMirrorDisable   = "workspace.mirror.disable"
)

// JSON-RPC 2.0 error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeNotFound     = -32000 // unknown run/workspace/member
	CodeDenied       = -32001 // write-gate / permission denial
	CodeInvalidState = -32002 // invalid lifecycle transition, conflict-free misuse
	CodeConflict     = -32003 // store conflict / in-use
	CodeUnavailable  = -32004 // run has no live PTY session / agent gone
	// CodeUpgradeRequired refuses a client older than the server's release;
	// see UpgradeRequired.
	CodeUpgradeRequired = -32005
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

// Error is the JSON-RPC error object. Data lets callers act on a refusal
// without parsing Message.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// UpdateCommand updates a CLI to the newest release.
const UpdateCommand = "aether update"

// UpgradeRequiredData is the Data of a CodeUpgradeRequired error.
// ClientVersion is empty for a client whose SSH banner names no version.
type UpgradeRequiredData struct {
	ClientVersion    string `json:"client_version"`
	ServerVersion    string `json:"server_version"`
	MinClientVersion string `json:"min_client_version"`
	Command          string `json:"command"`
}

// UpgradeRequired is the refusal of a client older than minClientVersion.
// Clients from before this code print Message verbatim, so it names the
// fix on its own.
func UpgradeRequired(clientVersion, serverVersion, minClientVersion string) *Error {
	client := clientVersion
	if client == "" {
		client = "older than " + minClientVersion
	}
	data, _ := json.Marshal(UpgradeRequiredData{
		ClientVersion:    clientVersion,
		ServerVersion:    serverVersion,
		MinClientVersion: minClientVersion,
		Command:          UpdateCommand,
	})
	return &Error{
		Code: CodeUpgradeRequired,
		Message: fmt.Sprintf("this aether is %s and the server runs %s; update this client first: run `%s` "+
			"(desktop: Update in the app; Android: install the latest aether-android.apk), then retry",
			client, serverVersion, UpdateCommand),
		Data: data,
	}
}

// IsUpgradeRequired reports whether err is a CodeUpgradeRequired refusal,
// which retrying cannot fix.
func IsUpgradeRequired(err error) bool {
	var perr *Error
	return errors.As(err, &perr) && perr.Code == CodeUpgradeRequired
}
