// Package domain defines the three core Aether objects - Workspace, Run,
// Member - and their shared enums. This is the type contract the SQLite
// store, the event bus, and the runtime all build against; they add no
// fields of their own.
//
// IDs are opaque strings; the store assigns them at creation time.
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type (
	// WorkspaceID identifies a Workspace.
	WorkspaceID string
	// RunID identifies a Run.
	RunID string
	// MemberID identifies a Member.
	MemberID string
	// ToolSnapshotID identifies an immutable per-member workspace tool tree.
	ToolSnapshotID string
)

// SetupPolicy controls the script run before a command starts in the
// workspace environment.
type SetupPolicy struct {
	Script string `json:"script,omitempty"`
}

// WorkspaceEnvironment is the server-owned environment definition for a
// workspace. Exactly one of CustomImage and NeutralImage selects the base
// image. Variables and setup policy retain the run-level environment inputs.
type WorkspaceEnvironment struct {
	CustomImage  string            `json:"custom_image,omitempty"`
	NeutralImage bool              `json:"neutral_image,omitempty"`
	Variables    map[string]string `json:"variables,omitempty"`
	SetupPolicy  SetupPolicy       `json:"setup_policy,omitempty"`
}

// UnmarshalJSON accepts canonical JSON booleans and legacy SQLite numeric
// 0/1 values for neutral_image. MarshalJSON remains the standard encoder,
// so values are always written as canonical JSON booleans.
func (e *WorkspaceEnvironment) UnmarshalJSON(data []byte) error {
	type environmentAlias WorkspaceEnvironment
	var raw struct {
		*environmentAlias
		NeutralImage json.RawMessage `json:"neutral_image"`
	}
	raw.environmentAlias = (*environmentAlias)(e)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.NeutralImage) == 0 {
		return nil
	}

	value := bytes.TrimSpace(raw.NeutralImage)
	if len(value) == 0 {
		return fmt.Errorf("domain: neutral_image must be a boolean or numeric 0/1")
	}
	switch value[0] {
	case 't', 'f':
		var neutral bool
		if err := json.Unmarshal(value, &neutral); err != nil {
			return err
		}
		e.NeutralImage = neutral
		return nil
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-':
		var legacy int
		if err := json.Unmarshal(value, &legacy); err == nil {
			if legacy == 0 || legacy == 1 {
				e.NeutralImage = legacy == 1
				return nil
			}
			return fmt.Errorf("domain: neutral_image numeric value must be 0 or 1, got %d", legacy)
		}
	}

	return fmt.Errorf("domain: neutral_image must be a boolean or numeric 0/1")
}

// Valid reports whether the environment has one unambiguous image selection
// and valid environment variable names.
func (e WorkspaceEnvironment) Valid() bool {
	if (e.CustomImage == "") == !e.NeutralImage {
		return false
	}
	for name := range e.Variables {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return false
		}
	}
	return true
}

// EffectiveImage resolves the custom image or the configured neutral image.
func (e WorkspaceEnvironment) EffectiveImage(neutralImage string) string {
	if e.CustomImage != "" {
		return e.CustomImage
	}
	if e.NeutralImage {
		return neutralImage
	}
	return ""
}

// WorkspaceSelector addresses a workspace by exactly one of ID or Name.
type WorkspaceSelector struct {
	ID   WorkspaceID
	Name string
}

// Valid reports whether exactly one selector form is present.
func (s WorkspaceSelector) Valid() bool {
	return (s.ID != "") != (strings.TrimSpace(s.Name) != "")
}

// WorkspaceShellMode identifies the purpose of a workspace shell.
type WorkspaceShellMode string

const (
	WorkspaceShellBootstrapTools WorkspaceShellMode = "bootstrap-tools"
	WorkspaceShellHarnessLogin   WorkspaceShellMode = "harness-login"
	// WorkspaceShellAgentSetup combines bootstrap and login in one session:
	// install the agent, run its login flow, exit; the server snapshots
	// tools, persists login state, and registers the member definition.
	WorkspaceShellAgentSetup WorkspaceShellMode = "agent-setup"
)

// Valid reports whether m is a supported workspace shell mode.
func (m WorkspaceShellMode) Valid() bool {
	return m == WorkspaceShellBootstrapTools || m == WorkspaceShellHarnessLogin || m == WorkspaceShellAgentSetup
}

// WorkspaceShellRequest describes one agent-agnostic interactive workspace
// shell request.
type WorkspaceShellRequest struct {
	Workspace              WorkspaceSelector
	Mode                   WorkspaceShellMode
	Harness                string
	VerificationExecutable string
	// TUIArgs/HeadlessArgs are the member's proposed argv templates for an
	// agent-setup shell registering a custom agent. Empty for shipped names.
	TUIArgs      []string
	HeadlessArgs []string
	Resume       bool
	Reset        bool
	Cols         uint
	Rows         uint
}

// Validate checks selector, mode, mode-specific harness, and intent fields.
func (r WorkspaceShellRequest) Validate() error {
	switch {
	case !r.Workspace.Valid():
		return errors.New("workspace selector must contain exactly one ID or name")
	case !r.Mode.Valid():
		return fmt.Errorf("invalid workspace shell mode %q", r.Mode)
	case r.Resume && r.Reset:
		return errors.New("resume and reset cannot both be set")
	case (r.Mode == WorkspaceShellHarnessLogin || r.Mode == WorkspaceShellAgentSetup) && strings.TrimSpace(r.Harness) == "":
		return errors.New("harness is required for login and agent-setup modes")
	case r.Mode == WorkspaceShellBootstrapTools && strings.TrimSpace(r.Harness) != "":
		return errors.New("harness is not allowed for bootstrap mode")
	case r.Mode != WorkspaceShellAgentSetup && (len(r.TUIArgs) > 0 || len(r.HeadlessArgs) > 0):
		return errors.New("argv proposals are only allowed for agent-setup mode")
	}
	if strings.ContainsAny(r.VerificationExecutable, "/\x00\r\n\t ") {
		return errors.New("verification executable must be a name")
	}
	return nil
}

// RunStatus is the lifecycle state of a Run.
//
// The lifecycle is:
//
//	queued -> provisioning -> running -> (needs-attention) -> terminal
//
// where terminal is one of merged, abandoned, failed, interrupted.
type RunStatus string

const (
	RunQueued         RunStatus = "queued"
	RunProvisioning   RunStatus = "provisioning"
	RunRunning        RunStatus = "running"
	RunNeedsAttention RunStatus = "needs-attention"
	RunMerged         RunStatus = "merged"
	RunAbandoned      RunStatus = "abandoned"
	RunFailed         RunStatus = "failed"
	RunInterrupted    RunStatus = "interrupted"
)

// AllRunStatuses lists every defined run status in lifecycle order. It is
// the single source of truth consumers derive status sets from (e.g. the
// store's non-terminal query); extend it when adding a status.
var AllRunStatuses = []RunStatus{
	RunQueued, RunProvisioning, RunRunning, RunNeedsAttention,
	RunMerged, RunAbandoned, RunFailed, RunInterrupted,
}

// Terminal reports whether the status is a finished state: no further
// transitions are possible.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunMerged, RunAbandoned, RunFailed, RunInterrupted:
		return true
	}
	return false
}

// Valid reports whether s is one of the defined run statuses.
func (s RunStatus) Valid() bool {
	return slices.Contains(AllRunStatuses, s)
}

// LaunchMode is how the agent process is hosted inside a run.
type LaunchMode string

const (
	// LaunchTUI runs the agent's native interactive TUI in a persistent
	// server-side PTY. This is the default.
	LaunchTUI LaunchMode = "tui"
	// LaunchHeadless runs the agent in its structured output mode.
	LaunchHeadless LaunchMode = "headless"
)

// Valid reports whether m is a defined launch mode.
func (m LaunchMode) Valid() bool {
	return m == LaunchTUI || m == LaunchHeadless
}

// Role is a member's role within the deployment.
type Role string

const (
	RoleViewer       Role = "viewer"
	RoleCollaborator Role = "collaborator"
	RoleAdmin        Role = "admin"
)

// Valid reports whether r is a defined role.
func (r Role) Valid() bool {
	return r == RoleViewer || r == RoleCollaborator || r == RoleAdmin
}

// ToolManifest records stable metadata discovered while creating a tool
// snapshot. It contains no server filesystem paths.
type ToolManifest struct {
	Executable string            `json:"executable,omitempty"`
	Version    string            `json:"version,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ToolSnapshot is an immutable member/workspace tool tree.
type ToolSnapshot struct {
	ID          ToolSnapshotID
	WorkspaceID WorkspaceID
	MemberID    MemberID
	Digest      string
	Manifest    ToolManifest
	CreatedAt   time.Time
}

// Workspace is a repo checkout, its server-owned environment definition,
// and the shared context every run against it inherits: runs, costs,
// approvals, templates, and the event feed are all workspace-scoped.
type Workspace struct {
	ID          WorkspaceID
	Name        string
	Environment WorkspaceEnvironment
	// BaseBranch is the branch new run worktrees are created from.
	BaseBranch string
	// SteerOthers is the workspace's steering policy for runs owned by
	// someone else: "" (default) lets any collaborator steer or kill any
	// run; SteerOthersAdminsOnly restricts steering and killing another
	// member's run to its owner and admins.
	SteerOthers string
	CreatedAt   time.Time
}

// DefaultBaseBranch is the branch a workspace falls back to when none was
// given at creation.
const DefaultBaseBranch = "main"

// SteerOthersAdminsOnly is the restrictive Workspace.SteerOthers value.
// The empty string is the permissive default.
const SteerOthersAdminsOnly = "admins_only"

// ValidSteerOthers reports whether v is a defined SteerOthers value.
func ValidSteerOthers(v string) bool {
	return v == "" || v == SteerOthersAdminsOnly
}

// Member is a person. Identity is the SSH public key, the tailnet login
// resolved via Tailscale WhoIs, or both - either may be empty, never both.
// The color is the stable attribution color used everywhere in the UI.
type Member struct {
	ID          MemberID
	DisplayName string
	// PublicKey is the member's SSH public key in authorized_keys format;
	// empty for tailnet-only members.
	PublicKey string
	// TailnetLogin is the member's Tailscale login name (e.g.
	// "alice@example.com") as reported by tailscaled's WhoIs; empty for
	// key-only members.
	TailnetLogin string
	// Pending marks a tailnet-auto-registered member awaiting admin
	// approval: they authenticate but every operation except server.info
	// is denied until approved.
	Pending bool
	// Color is a hex color (e.g. "#e6194b") assigned at join time from a
	// colorblind-safe palette; overridable.
	Color     string
	Role      Role
	CreatedAt time.Time
}

// Run is one agent execution: a task, an isolated worktree and branch, a
// container, and a PTY transcript, owned by one member within one
// workspace.
type Run struct {
	ID          RunID
	WorkspaceID WorkspaceID
	// MemberID is the owning member (transferable via handoff).
	MemberID MemberID
	// Task is the prompt the agent was launched with.
	Task string
	// Harness is the agent harness name, e.g. "claude", "codex".
	Harness string
	Mode    LaunchMode
	Status  RunStatus
	// Reason is the last run.status reason, sanitized like the event
	// payload; empty when the last transition carried no reason.
	Reason string
	// Branch is the run's git branch, aether/run-<slug>-<id>: the task
	// leads so the branch reads as what it is, the run ID trails as the
	// disambiguator.
	Branch string
	// Worktree is the server-side path of the run's git worktree.
	Worktree string
	// Protected restricts steering and killing this run to its owner and
	// admins, regardless of the workspace's SteerOthers setting.
	Protected bool
	CreatedAt time.Time
	// StartedAt is when the run entered running; nil while queued or
	// provisioning.
	StartedAt *time.Time
	// FinishedAt is when the run reached a terminal status; nil until then.
	FinishedAt *time.Time
	// ProfileSnapshotID is the immutable agent-profile snapshot pinned at
	// provisioning. Zero (empty) means unpinned / no snapshot.
	ProfileSnapshotID ProfileSnapshotID
	// ToolSnapshotID is the immutable workspace tool snapshot pinned before
	// container creation. Zero means no active tool snapshot.
	ToolSnapshotID ToolSnapshotID
}

// ServerBusy reports what is keeping a server from being idle, which is
// what a scheduled self-update waits for. It is the run engine's answer,
// consumed by the update service, so it lives here rather than in either.
//
// Paused runs are counted separately because they do not hold anything
// back: a frozen container survives a restart exactly like a live one, and
// nothing is working inside it. They are still reported so an admin
// looking at a pending update is not left wondering why a run that
// `aether runs` calls running is being ignored.
type ServerBusy struct {
	// Unknown reports that the server could not tell what it was doing -
	// a failed store read. It is never idle: an unknown answer must not be
	// the one that decides to restart.
	Unknown bool
	// Runs is how many runs are still working, which holds an update back.
	Runs int
	// Paused is how many runs are paused. They do not hold an update back.
	Paused int
	// Shells is how many workspace shells are open. They have no container
	// to reattach to after a restart, so each one holds an update back.
	Shells int
}

// Idle reports that nothing is holding a server update back.
func (b ServerBusy) Idle() bool {
	return !b.Unknown && b.Runs == 0 && b.Shells == 0
}
