// Package domain defines the three core Aether objects - Workspace, Run,
// Member - and their shared enums. This is the type contract the SQLite
// store, the event bus, and the runtime all build against; they add no
// fields of their own.
//
// IDs are opaque strings; the store assigns them at creation time.
package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

type (
	// WorkspaceID identifies a Workspace.
	WorkspaceID string
	// RunID identifies a Run.
	RunID string
	// MemberID identifies a Member.
	MemberID string
)

// SetupPolicy controls the script run before a command starts in the
// workspace environment.
type SetupPolicy struct {
	Script string `json:"script,omitempty"`
}

// WorkspaceEnvironment carries workspace variables and its pre-launch setup
// policy.
type WorkspaceEnvironment struct {
	Variables   map[string]string `json:"variables,omitempty"`
	SetupPolicy SetupPolicy       `json:"setup_policy,omitempty"`
}

// Valid reports whether all environment variable names are valid.
func (e WorkspaceEnvironment) Valid() bool {
	for name := range e.Variables {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return false
		}
	}
	return true
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
	// Origin is the upstream git URL run checkouts push to; "" when the
	// workspace has none, in which case a checkout keeps the origin its
	// clone made.
	Origin    string
	CreatedAt time.Time
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

// originPrefixes are the URL schemes a workspace origin may use, plus the
// absolute-path form for a local upstream.
var originPrefixes = []string{"https://", "http://", "ssh://", "git://", "/"}

// scpLikeOrigin matches git's scp-like remote form (user@host:path).
var scpLikeOrigin = regexp.MustCompile(`^[A-Za-z0-9._-]+@[^:/\s]+:`)

// ValidOrigin reports whether url is usable as a workspace origin. The
// empty string is valid and clears the origin. Otherwise the value is
// handed to git as a remote URL inside a run container, so it must be one
// line with no whitespace, and must not start with "-", which git would
// read as an option rather than a URL.
func ValidOrigin(url string) bool {
	if url == "" {
		return true
	}
	if len(url) > 1024 || strings.HasPrefix(url, "-") {
		return false
	}
	if strings.ContainsFunc(url, func(r rune) bool {
		return unicode.IsSpace(r) || isGitControl(r)
	}) {
		return false
	}
	for _, prefix := range originPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return scpLikeOrigin.MatchString(url)
}

// NormalizeOrigin rewrites a github.com origin written in either of git's
// SSH forms - git@github.com:acme/app.git and
// ssh://[git@]github.com[:22]/acme/app.git - to
// https://github.com/acme/app.git. A run authenticates to github.com over
// https only, so an SSH origin recorded as typed could never be pushed to
// from a container. Every other origin, an SSH URL on another host
// included, is returned unchanged: only github.com's https form is known
// to address the same repository.
func NormalizeOrigin(origin string) string {
	var authority, path string
	if rest, ok := strings.CutPrefix(origin, "ssh://"); ok {
		var found bool
		if authority, path, found = strings.Cut(rest, "/"); !found {
			return origin
		}
	} else if scpLikeOrigin.MatchString(origin) {
		authority, path, _ = strings.Cut(origin, ":")
	} else {
		return origin
	}
	if _, host, ok := strings.Cut(authority, "@"); ok {
		authority = host
	}
	if strings.TrimSuffix(authority, ":22") != "github.com" {
		return origin
	}
	return "https://github.com/" + strings.TrimPrefix(path, "/")
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
	Color string
	Role  Role
	// GitName and GitEmail are the identity commits made for this member
	// are authored as. Empty falls back to GitIdentity's defaults.
	GitName   string
	GitEmail  string
	Image     string `json:"image,omitempty"`
	CreatedAt time.Time
}

// GitIdentity is a git author or co-author: the name and address a commit
// is attributed to.
type GitIdentity struct {
	Name  string
	Email string
}

// fallbackGitEmailDomain addresses a member who has set no git email. It
// resolves nowhere on purpose: an unmapped address is honest about the
// commit crediting no upstream account.
const fallbackGitEmailDomain = "@aether.local"

// GitIdentity returns what commits for this member are authored as: the
// git name and email they set, falling back to the display name and the
// member's aether.local address.
//
// Every half is validated here rather than trusted from the row. A display
// name is member-supplied and never checked - it comes out of the SSH
// username an invite is redeemed with - and one holding angle brackets
// would put a second address inside the "Name <email>" form, which git and
// GitHub both resolve to the first one they see. A member id credits
// nobody, which is the honest answer; crediting the wrong account is not.
func (m *Member) GitIdentity() GitIdentity {
	id := GitIdentity{Name: string(m.ID), Email: string(m.ID) + fallbackGitEmailDomain}
	switch {
	case ValidGitName(m.GitName):
		id.Name = m.GitName
	case ValidGitName(m.DisplayName):
		id.Name = m.DisplayName
	}
	if ValidGitEmail(m.GitEmail) {
		id.Email = m.GitEmail
	}
	return id
}

// String renders the identity in git's "Name <email>" form.
func (g GitIdentity) String() string { return g.Name + " <" + g.Email + ">" }

// Trailer renders the identity as a Co-authored-by commit trailer.
func (g GitIdentity) Trailer() string { return "Co-authored-by: " + g.String() }

// ValidGitName reports whether name is usable as a git author name: no
// angle brackets, which would put a second address in the "Name <email>"
// form, no control character, which would break the trailer's single
// line, and no surrounding space, which git strips anyway.
func ValidGitName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return r == '<' || r == '>' || isGitControl(r)
	})
}

// ValidGitEmail reports whether email is usable as a git author address:
// one @ with something either side, and nothing that would break the
// "Name <email>" form or its line.
func ValidGitEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || strings.Count(email, "@") != 1 {
		return false
	}
	return !strings.ContainsFunc(email, func(r rune) bool {
		return r == '<' || r == '>' || r == ' ' || isGitControl(r)
	})
}

// isGitControl reports whether r is a control character. Git's own author
// parsing stops at a line break, and the rest would render as escape
// bytes in every log and trailer that carries the name.
func isGitControl(r rune) bool { return r < 0x20 || r == 0x7f }

// GitHubConnection is the outcome of connecting a member's environment to
// github.com: the account gh is logged in as, the public half of the
// commit signing key kept in that member's environment home, and its
// SHA256 fingerprint as GitHub shows it.
type GitHubConnection struct {
	Login       string
	SigningKey  string
	Fingerprint string
}

// Terminal is the persistent per-member environment container.
type Terminal struct {
	Member      MemberID
	ContainerID string
	Image       string
	StartedAt   time.Time
}

// TerminalStatus describes the running state and live tabs of a member terminal.
type TerminalStatus struct {
	Running    bool
	Image      string
	SavedImage string
	StartedAt  time.Time
	Tabs       []string
}

// Run is one agent execution: a task, an isolated worktree and branch, a
// container, and a PTY transcript, owned by one member within one
// workspace.
type Run struct {
	ID          RunID
	WorkspaceID WorkspaceID
	// MemberID is the owning member (transferable via handoff).
	MemberID MemberID
	// AccountMemberID owns the environment, credentials, profile, and vendor
	// quota used by this run. It normally equals MemberID; a different value
	// records an explicit account-share launch without changing run ownership
	// or actor attribution. Empty rows from older schemas fall back to MemberID.
	AccountMemberID MemberID
	// Task is the prompt the agent was launched with.
	Task string
	// Title is the latest terminal title reported by the agent.
	Title string
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
	// LastCommit is the most recently published commit on the run branch.
	LastCommit string
	// LastCommitAt is when LastCommit was published.
	LastCommitAt time.Time
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
	// HarnessSessionID is the harness-native conversation ID pinned at
	// launch, so relaunching an interrupted run resumes this run's own
	// conversation by name. Empty means the harness cannot pin one, or the
	// row predates pinning; a relaunch then falls back to the harness's
	// "continue the most recent conversation here" flag. See
	// docs/failure-handling.md.
	HarnessSessionID string
}

// AccountMember returns the member whose agent account backs the run.
func (r *Run) AccountMember() MemberID {
	if r.AccountMemberID != "" {
		return r.AccountMemberID
	}
	return r.MemberID
}

// RelaunchAccount preserves an account that is distinct from the current run
// owner, including after handoff. A direct relaunch by someone other than the
// current owner switches an otherwise unshared run to that actor's account.
func (r *Run) RelaunchAccount(actor MemberID) MemberID {
	account := r.AccountMember()
	if account == r.MemberID && actor != r.MemberID {
		return actor
	}
	return account
}

// AccountShare grants Grantee permission to launch agents with Owner's
// environment and vendor account. The authenticated grantee remains the run
// owner and the actor recorded in the timeline.
type AccountShare struct {
	Owner     MemberID
	Grantee   MemberID
	CreatedAt time.Time
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
	// Shells is how many interactive terminal attaches are live. A restart
	// would drop each stream under the person typing into it, so each one
	// holds an update back.
	Shells int
}

// Idle reports that nothing is holding a server update back.
func (b ServerBusy) Idle() bool {
	return !b.Unknown && b.Runs == 0 && b.Shells == 0
}
