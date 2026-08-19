// Package domain defines the four core Aether objects - Workspace, Session,
// Run, Member - and their shared enums. This is the type contract agreed in
// Wave 0: the SQLite store, the event bus, and the runtime all build against
// these definitions and add no fields of their own.
//
// IDs are opaque strings; the store assigns them at creation time.
package domain

import (
	"slices"
	"time"
)

type (
	// WorkspaceID identifies a Workspace.
	WorkspaceID string
	// SessionID identifies a Session.
	SessionID string
	// RunID identifies a Run.
	RunID string
	// MemberID identifies a Member.
	MemberID string
)

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

// Workspace is a repo checkout plus its environment: the container image,
// env vars, and setup script that runs execute inside. It lives on the
// server as a bare git repo plus this config.
type Workspace struct {
	ID   WorkspaceID
	Name string
	// Image is the container image runs start from.
	Image string
	// Env is extra environment applied to run containers.
	Env map[string]string
	// SetupScript runs inside the container before the agent launches.
	SetupScript string
	CreatedAt   time.Time
}

// SteerOthersAdminsOnly is the restrictive Session.SteerOthers value.
// The empty string is the permissive default.
const SteerOthersAdminsOnly = "admins_only"

// ValidSteerOthers reports whether v is a defined SteerOthers value.
func ValidSteerOthers(v string) bool {
	return v == "" || v == SteerOthersAdminsOnly
}

// Session is the shared context for one effort: it groups runs, members,
// and the event feed against one workspace and base branch.
type Session struct {
	ID          SessionID
	WorkspaceID WorkspaceID
	Name        string
	// BaseBranch is the branch new run worktrees are created from.
	BaseBranch string
	// SteerOthers is the session's steering policy for runs owned by
	// someone else: "" (default) lets any collaborator steer or kill any
	// run; SteerOthersAdminsOnly restricts steering and killing another
	// member's run to its owner and admins.
	SteerOthers string
	CreatedAt   time.Time
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
// container, and a PTY transcript, owned by one member within one session.
type Run struct {
	ID        RunID
	SessionID SessionID
	// MemberID is the owning member (transferable via handoff).
	MemberID MemberID
	// Task is the prompt the agent was launched with.
	Task string
	// Harness is the agent harness name, e.g. "claude", "codex".
	Harness string
	Mode    LaunchMode
	Status  RunStatus
	// Branch is the run's git branch, aether/run-<id>-<slug>.
	Branch string
	// Worktree is the server-side path of the run's git worktree.
	Worktree string
	// Protected restricts steering and killing this run to its owner and
	// admins, regardless of the session's SteerOthers setting.
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
}
