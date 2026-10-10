// Package domain defines the core Aether objects - Workspace, Run, Member -
// and their shared enums. IDs are opaque strings the store assigns.
package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

type (
	WorkspaceID string
	RunID       string
	MemberID    string
)

// SetupPolicy is the script run before a command starts in the workspace
// environment.
type SetupPolicy struct {
	Script string `json:"script,omitempty"`
}

type WorkspaceEnvironment struct {
	Variables   map[string]string `json:"variables,omitempty"`
	SetupPolicy SetupPolicy       `json:"setup_policy,omitempty"`
}

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

func (s WorkspaceSelector) Valid() bool {
	return (s.ID != "") != (strings.TrimSpace(s.Name) != "")
}

// RunStatus is the lifecycle state of a Run:
//
//	queued -> provisioning -> running <-> needs-attention -> completed -> (merged | abandoned)
//
// A run can also become failed or interrupted before it completes. Completed
// is excluded from active-run operations but remains closable until a final
// disposition is chosen. A terminal TUI run may still hold a paused container
// for relaunch.
type RunStatus string

const (
	RunQueued         RunStatus = "queued"
	RunProvisioning   RunStatus = "provisioning"
	RunRunning        RunStatus = "running"
	RunNeedsAttention RunStatus = "needs-attention"
	RunCompleted      RunStatus = "completed"
	RunMerged         RunStatus = "merged"
	RunAbandoned      RunStatus = "abandoned"
	RunFailed         RunStatus = "failed"
	RunInterrupted    RunStatus = "interrupted"
)

// AllRunStatuses lists every run status in lifecycle order; consumers such as
// the store's non-terminal query derive their status sets from it.
var AllRunStatuses = []RunStatus{
	RunQueued, RunProvisioning, RunRunning, RunNeedsAttention, RunCompleted,
	RunMerged, RunAbandoned, RunFailed, RunInterrupted,
}

func (s RunStatus) Terminal() bool {
	switch s {
	case RunCompleted, RunMerged, RunAbandoned, RunFailed, RunInterrupted:
		return true
	}
	return false
}

// Final reports whether the status is an immutable disposition.
func (s RunStatus) Final() bool {
	switch s {
	case RunMerged, RunAbandoned, RunFailed, RunInterrupted:
		return true
	}
	return false
}

func (s RunStatus) Valid() bool {
	return slices.Contains(AllRunStatuses, s)
}

// LaunchMode is how the agent process is hosted inside a run.
type LaunchMode string

const (
	// LaunchTUI runs the agent's native TUI in a persistent server-side PTY.
	LaunchTUI LaunchMode = "tui"
	// LaunchHeadless runs the agent in its structured output mode.
	LaunchHeadless LaunchMode = "headless"
	// LaunchACP is "enhanced": the agent speaks the Agent Client Protocol.
	LaunchACP LaunchMode = "acp"
)

func (m LaunchMode) Valid() bool {
	return m == LaunchTUI || m == LaunchHeadless || m == LaunchACP
}

// Interactive reports whether a run in mode m is long-lived: it outlives
// the agent's turn, can be closed, retained and reopened, and can be woken
// by mail.
func (m LaunchMode) Interactive() bool {
	return m == LaunchTUI || m == LaunchACP
}

// LaunchOptions carries one-shot launch controls that are not part of the
// strict Launch seam. CachedBase pins a retry to the exact accepted mirror
// commit returned by a prior base-capture failure. AssignedRunID is reserved
// by durable mission state and is never exposed through generic run.launch.
type LaunchOptions struct {
	CachedBase    string
	AssignedRunID RunID
}

// Role is a member's role within the deployment.
type Role string

const (
	RoleViewer       Role = "viewer"
	RoleCollaborator Role = "collaborator"
	RoleAdmin        Role = "admin"
)

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
	BaseBranch  string
	// SteerOthers "" lets any collaborator steer or kill any run;
	// SteerOthersAdminsOnly restricts that to the run's owner and admins.
	SteerOthers string
	// Origin is the upstream git URL run checkouts push to; "" when the
	// workspace has none, in which case a checkout keeps the origin its
	// clone made.
	Origin    string
	CreatedAt time.Time
}

type MirrorAuth string

const (
	// MirrorAuthPublic fetches a public HTTPS source without credentials.
	MirrorAuthPublic MirrorAuth = "public"
	// MirrorAuthDeployKey fetches with an operator-provided deploy key.
	MirrorAuthDeployKey MirrorAuth = "deploy-key"
	// MirrorAuthGitHub reads the bound member's native GitHub credential on demand.
	MirrorAuthGitHub MirrorAuth = "github"
)

func (a MirrorAuth) Valid() bool {
	return a == MirrorAuthPublic || a == MirrorAuthDeployKey || a == MirrorAuthGitHub
}

type MirrorStatus string

const (
	MirrorStatusPending       MirrorStatus = "pending"
	MirrorStatusRefreshing    MirrorStatus = "refreshing"
	MirrorStatusDisabling     MirrorStatus = "disabling"
	MirrorStatusReady         MirrorStatus = "ready"
	MirrorStatusAuthFailed    MirrorStatus = "auth-failed"
	MirrorStatusOffline       MirrorStatus = "offline"
	MirrorStatusSourceMissing MirrorStatus = "source-missing"
	MirrorStatusRewritten     MirrorStatus = "rewritten"
	MirrorStatusDiverged      MirrorStatus = "diverged"
	MirrorStatusError         MirrorStatus = "error"
)

var AllMirrorStatuses = []MirrorStatus{
	MirrorStatusPending,
	MirrorStatusRefreshing,
	MirrorStatusDisabling,
	MirrorStatusReady,
	MirrorStatusAuthFailed,
	MirrorStatusOffline,
	MirrorStatusSourceMissing,
	MirrorStatusRewritten,
	MirrorStatusDiverged,
	MirrorStatusError,
}

func (s MirrorStatus) Valid() bool {
	return slices.Contains(AllMirrorStatuses, s)
}

// WorkspaceMirror is the server-owned configuration and refresh state for a
// workspace's upstream source. A missing row means the workspace is local-only.
//
// Credentials are never part of this model. GitHub bindings identify the
// authorizing member and numeric account, not a token or shared member home.
type WorkspaceMirror struct {
	WorkspaceID    WorkspaceID
	SourceURL      string
	SourceIdentity string
	Branch         string
	Auth           MirrorAuth
	GitHubMemberID MemberID
	GitHubUserID   int64
	Generation     int64
	Status         MirrorStatus
	ObservedCommit string
	AcceptedCommit string
	KeyFingerprint string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastAttemptAt  time.Time
	LastSuccessAt  time.Time
}

// Valid checks storage-level shapes only. Git object and ref rules are
// duplicated here rather than importing the git engine package.
func (m WorkspaceMirror) Valid() bool {
	return m.WorkspaceID != "" &&
		ValidMirrorSourceURL(m.SourceURL) &&
		ValidMirrorSourceIdentity(m.SourceIdentity) &&
		ValidMirrorBranch(m.Branch) &&
		m.Auth.Valid() &&
		((m.Auth == MirrorAuthGitHub && m.GitHubMemberID != "" && m.GitHubUserID > 0 && ValidGitHubMirrorSourceURL(m.SourceURL)) ||
			(m.Auth != MirrorAuthGitHub && m.GitHubMemberID == "" && m.GitHubUserID == 0)) &&
		m.Generation > 0 &&
		m.Status.Valid() &&
		ValidMirrorSHA(m.ObservedCommit) &&
		ValidMirrorSHA(m.AcceptedCommit) &&
		ValidMirrorFingerprint(m.KeyFingerprint)
}

// ValidMirrorSourceURL accepts HTTPS and SSH remotes, including Git's
// scp-like form.
func ValidMirrorSourceURL(url string) bool {
	if url == "" || strings.TrimSpace(url) != url || strings.ContainsAny(url, "\x00\r\n\t ") || strings.HasPrefix(url, "-") {
		return false
	}
	return strings.HasPrefix(url, "https://") ||
		strings.HasPrefix(url, "ssh://") ||
		scpLikeOrigin.MatchString(url)
}

var githubMirrorSource = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+\.git$`)

// ValidGitHubMirrorSourceURL accepts only canonical, credential-free GitHub
// HTTPS repository URLs. It deliberately rejects normalization and escaping.
func ValidGitHubMirrorSourceURL(raw string) bool {
	if len(raw) > 2048 || !githubMirrorSource.MatchString(raw) {
		return false
	}
	repo := strings.TrimSuffix(raw[strings.LastIndexByte(raw, '/')+1:], ".git")
	return repo != "" && repo != "." && repo != ".."
}

func ValidMirrorSourceIdentity(identity string) bool {
	return identity != "" && strings.TrimSpace(identity) == identity &&
		!strings.ContainsAny(identity, "\x00\r\n\t ")
}

// ValidMirrorBranch reports whether branch is a valid Git ref component.
func ValidMirrorBranch(branch string) bool {
	if branch == "" || strings.TrimSpace(branch) != branch ||
		strings.ContainsAny(branch, "\x00\r\n\t ~^:?*[\\") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.HasPrefix(branch, ".") ||
		strings.HasSuffix(branch, ".") {
		return false
	}
	return true
}

// ValidMirrorSHA accepts empty or a full SHA-1 or SHA-256 object ID.
func ValidMirrorSHA(sha string) bool {
	if sha == "" {
		return true
	}
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') &&
			(r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// ValidMirrorFingerprint reports whether fingerprint is empty or an SSH
// SHA-256 fingerprint.
func ValidMirrorFingerprint(fingerprint string) bool {
	if fingerprint == "" || !strings.HasPrefix(fingerprint, "SHA256:") {
		return fingerprint == ""
	}
	if len(fingerprint) <= len("SHA256:") {
		return false
	}
	for _, r := range fingerprint[len("SHA256:"):] {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') &&
			(r < '0' || r > '9') && r != '+' && r != '/' && r != '=' {
			return false
		}
	}
	return true
}

const DefaultBaseBranch = "main"

// SteerOthersAdminsOnly is the restrictive Workspace.SteerOthers value.
// The empty string is the permissive default.
const SteerOthersAdminsOnly = "admins_only"

func ValidSteerOthers(v string) bool {
	return v == "" || v == SteerOthersAdminsOnly
}

// originPrefixes are the URL schemes a workspace origin may use, plus the
// absolute-path form for a local upstream.
var originPrefixes = []string{"https://", "http://", "ssh://", "git://", "/"}

// scpLikeOrigin matches git's scp-like remote form (user@host:path).
var scpLikeOrigin = regexp.MustCompile(`^[A-Za-z0-9._-]+@[^:/\s]+:`)

// ValidOrigin accepts "" (clears the origin). Otherwise the value reaches git
// as a remote URL, so it must be one line and must not start with "-", which
// git would read as an option.
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

// NormalizeOrigin rewrites a github.com SSH origin to its https form, since a
// run authenticates to github.com over https only. Other hosts are left
// unchanged: only github.com's https form is known to be the same repository.
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
	// TailnetLogin is the login tailscaled's WhoIs reports; empty for
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

type GitIdentity struct {
	Name  string
	Email string
}

// fallbackGitEmailDomain addresses a member who has set no git email. It
// resolves nowhere on purpose: an unmapped address is honest about the
// commit crediting no upstream account.
const fallbackGitEmailDomain = "@aether.local"

// GitIdentity validates every half rather than trusting the row: a display
// name comes unchecked from the SSH username, and angle brackets in it would
// put a second address inside "Name <email>", which git and GitHub resolve to
// the first one they see. Crediting nobody beats crediting the wrong account.
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

func (g GitIdentity) String() string { return g.Name + " <" + g.Email + ">" }

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

// GitHubCLIStatus is what the member's environment terminal answers about
// its own gh.
type GitHubCLIStatus string

const (
	// GitHubCLIOK: gh is there and can answer the login check.
	GitHubCLIOK GitHubCLIStatus = "ok"
	// GitHubCLIMissing: nothing named gh on PATH.
	GitHubCLIMissing GitHubCLIStatus = "missing"
	// GitHubCLIBroken: gh is on PATH but would not run.
	GitHubCLIBroken GitHubCLIStatus = "broken"
	// GitHubCLIOutdated: gh ran and is older than the login check needs.
	GitHubCLIOutdated GitHubCLIStatus = "outdated"
)

// GitHubCLI is the gh in one member's environment terminal, and what has to
// happen when it cannot do the login.
type GitHubCLI struct {
	Status GitHubCLIStatus
	// Version is empty when gh did not run, or printed a version line this
	// cannot read.
	Version string
	// Minimum is the oldest gh the login check can read.
	Minimum string
	// Detail is what gh, or the container that could not run it, printed.
	Detail string
	// Image is what the terminal container runs and SavedImage the member's
	// saved one; they differ while a container outlives its intended image.
	Image      string
	SavedImage string
	// Path is where gh resolved, filled only when that is a file inside the
	// member's own environment home. Such a file comes first on PATH and
	// outlives every image, so no image remedy can reach it.
	Path string
	// Remedy is the command this member runs; AdminRemedy is what a server
	// admin has to run first when the server's standard image is the one
	// without a usable gh. Both are empty while gh is fine.
	Remedy      string
	AdminRemedy string
}

// Terminal is the persistent per-member environment container.
type Terminal struct {
	Member      MemberID
	ContainerID string
	Image       string
	StartedAt   time.Time
}

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
	// AccountMemberID owns the vendor login and quota used by this run. A
	// value other than MemberID is an account-share launch, which mounts only
	// the harness's login paths from this member's home and changes neither
	// ownership nor attribution.
	AccountMemberID MemberID
	// HomeMemberID is whose home the container mounts: the launcher. A
	// handoff changes MemberID, never this. Empty on rows created before
	// account shares were narrowed.
	HomeMemberID MemberID
	// Task is the prompt the agent was launched with.
	Task string
	// Title is the latest terminal or ACP session title reported by the agent;
	// before that, the first line of a taskless enhanced run's first prompt.
	Title   string
	Harness string
	Mode    LaunchMode
	// ACP is true when the server drives the agent over the Agent Client
	// Protocol: every LaunchACP run, and a LaunchHeadless run whose agent
	// serves ACP. Such a run has a session item log.
	ACP    bool
	Status RunStatus
	// Reason is the last run.status reason, sanitized like the event
	// payload; empty when the last transition carried no reason.
	Reason string
	// Branch is aether/run-<slug>-<id>.
	Branch string
	// LastCommit is the most recently published commit on the run branch.
	LastCommit   string
	LastCommitAt time.Time
	// Worktree is the server-side path of the run's git worktree.
	Worktree string
	// Protected restricts steering and killing this run to its owner and
	// admins, regardless of the workspace's SteerOthers setting.
	Protected bool
	// ArchivedAt is when this run was hidden from the board; nil means it
	// is not archived. Only a Final run can be archived. Archiving does
	// not schedule deletion.
	ArchivedAt *time.Time
	// OutcomeUnseen is true while a run an agent's report finished
	// (completed or failed) has not been opened by its owner. Any later
	// status change clears it.
	OutcomeUnseen bool
	CreatedAt     time.Time
	// StartedAt is when the run entered running; nil while queued or
	// provisioning.
	StartedAt *time.Time
	// FinishedAt is when the run reached a terminal status; nil until then.
	FinishedAt *time.Time
	// UnansweredQuestions is the number of non-denied, non-cancelled room
	// questions to the run's owner (asked by anyone else) without a
	// correlated reply. It is populated by run snapshot reads and is zero
	// for newly-created runs.
	UnansweredQuestions int
	// UnackedMessages is how much agent mail addressed to the run it has not
	// acknowledged, populated by run snapshot reads.
	UnackedMessages int
	// OldestUnackedAt is when the oldest of that mail was sent; nil when
	// there is none.
	OldestUnackedAt *time.Time
	// Mission fields are read-only snapshot metadata from durable mission and
	// attempt relationships, not authorization. MissionRole is "integrator"
	// only for the current integrator, or "worker" even for finished attempts.
	MissionID       MissionID
	MissionRole     string
	IntegratorRunID RunID
	// ProfileSnapshotID is the agent-profile snapshot pinned at provisioning;
	// empty means unpinned.
	ProfileSnapshotID ProfileSnapshotID
	// HarnessSessionID is the agent's own session id, which a driver records
	// so a reopened run can resume the same conversation. Empty until a
	// driver records one.
	HarnessSessionID string
	// BaseCommit is the commit SHA recorded for the workspace base at the
	// last base check. Empty means no base commit has been observed.
	BaseCommit string
	BaseBranch string
	// BaseSource is the source identity used to obtain BaseCommit.
	BaseSource string
	// BaseCheckedAt is when the base provenance was checked. The zero value
	// means that no base check has been recorded.
	BaseCheckedAt time.Time
}

var agentSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidAgentSessionID reports whether id can be stored as a run's
// HarnessSessionID. An agent reports it from inside its container, and a mode
// switch puts it on the agent's command line.
func ValidAgentSessionID(id string) bool {
	return agentSessionID.MatchString(id)
}

// AccountMember returns the member whose agent account backs the run.
func (r *Run) AccountMember() MemberID {
	if r.AccountMemberID != "" {
		return r.AccountMemberID
	}
	return r.MemberID
}

// HomeMember returns whose persistent home the run's container mounts.
func (r *Run) HomeMember() MemberID {
	if r.HomeMemberID != "" {
		return r.HomeMemberID
	}
	return r.AccountMember()
}

// AccountShare grants Grantee permission to launch agents with Owner's
// vendor login: the harness's declared login paths from Owner's home, inside
// Grantee's own environment. The authenticated grantee remains the run owner
// and the actor recorded in the timeline.
type AccountShare struct {
	Owner     MemberID
	Grantee   MemberID
	CreatedAt time.Time
}

// ServerBusy is what a scheduled self-update waits for. Paused runs do not
// hold an update back - a frozen container survives a restart - but are still
// reported so an admin sees why a "running" run is ignored.
type ServerBusy struct {
	// Unknown reports that the server could not tell what it was doing -
	// a failed store read. It is never idle: an unknown answer must not be
	// the one that decides to restart.
	Unknown bool
	Runs    int
	Paused  int
	// Shells is live interactive terminal attaches; a restart would drop each.
	Shells int
}

func (b ServerBusy) Idle() bool {
	return !b.Unknown && b.Runs == 0 && b.Shells == 0
}
