package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Run is the wire form of a run. Times are RFC3339; server-side host paths
// never appear on the wire. Paused comes from the scheduler, never from the
// stored run.
type Run struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	MemberID    string `json:"member_id"`
	// AccountMemberID's vendor login backs the run; MemberID remains the
	// owner and actor.
	AccountMemberID string `json:"account_member_id"`
	Task            string `json:"task"`
	Title           string `json:"title,omitempty"`
	Harness         string `json:"harness"`
	Mode            string `json:"mode"`
	ACP             bool   `json:"acp,omitempty"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	// Paused has no omitempty: absence must keep meaning "gateway too old
	// to know", never "not paused", or clients cannot seed pause state.
	Paused bool `json:"paused"`
	// ControllerMemberID holds the run's control lease; empty means nobody
	// does. No omitempty, so absence still means a gateway too old to say.
	ControllerMemberID string `json:"controller_member_id"`
	Switching          string `json:"switching,omitempty"`
	// Always a list, including [].
	PendingInputs []domain.RunInputRequest `json:"pending_inputs"`
	Branch        string                   `json:"branch"`
	LastCommit    string                   `json:"last_commit,omitempty"`
	LastCommitAt  *string                  `json:"last_commit_at,omitempty"`
	Protected     bool                     `json:"protected,omitempty"`
	// DeletesAt is computed here so no client hardcodes the retention window.
	ArchivedAt *string `json:"archived_at,omitempty"`
	DeletesAt  *string `json:"deletes_at,omitempty"`
	// run.seen clears OutcomeUnseen.
	OutcomeUnseen     bool    `json:"outcome_unseen,omitempty"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         *string `json:"started_at"`
	FinishedAt        *string `json:"finished_at"`
	ProfileSnapshotID string  `json:"profile_snapshot_id,omitempty"`
	// Always present, including zero; web clients keep it optional for older
	// gateways.
	UnansweredQuestions int     `json:"unanswered_questions"`
	UnackedMessages     int     `json:"unacked_messages"`
	OldestUnackedAt     *string `json:"oldest_unacked_at,omitempty"`
	MissionID           string  `json:"mission_id,omitempty"`
	MissionRole         string  `json:"mission_role,omitempty"`
	IntegratorRunID     string  `json:"integrator_run_id,omitempty"`
	// Immutable base provenance captured at launch.
	BaseCommit    string  `json:"base_commit,omitempty"`
	BaseBranch    string  `json:"base_branch,omitempty"`
	BaseSource    string  `json:"base_source,omitempty"`
	BaseCheckedAt *string `json:"base_checked_at,omitempty"`
}

// MirrorFailure lets a caller retry a failed base capture with the exact
// accepted commit returned by the mirror.
type MirrorFailure struct {
	Kind           string `json:"kind"`
	AcceptedCommit string `json:"accepted_commit,omitempty"`
	BaseCommit     string `json:"base_commit,omitempty"`
	ObservedCommit string `json:"observed_commit,omitempty"`
	Source         string `json:"source,omitempty"`
	Branch         string `json:"branch,omitempty"`
}

// Workspace is the wire form of a workspace; image, env, and setup script
// stay server-side.
type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseBranch  string `json:"base_branch"`
	SteerOthers string `json:"steer_others,omitempty"`
	// Origin is the upstream git URL run checkouts push to.
	Origin    string `json:"origin,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Member is the wire form of a member. Pending appears only while the
// member awaits admin approval.
type Member struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Color       string `json:"color"`
	Role        string `json:"role"`
	// Empty means the fallback identity applies.
	GitName  string `json:"git_name,omitempty"`
	GitEmail string `json:"git_email,omitempty"`
	Pending  bool   `json:"pending,omitempty"`
}

// Event is the wire envelope streamed on the events subsystem. Payload is
// the raw JSON of the corresponding internal/events payload struct.
type Event struct {
	ID          string          `json:"id"`
	Seq         uint64          `json:"seq"`
	Time        string          `json:"time"`
	WorkspaceID string          `json:"workspace_id"`
	RunID       string          `json:"run_id"`
	ActorID     string          `json:"actor_id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}
func rfc3339ValuePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := rfc3339(t)
	return &s
}

func runDeletesAt(archivedAt *time.Time) *string {
	if archivedAt == nil {
		return nil
	}
	deletesAt := archivedAt.Add(domain.ArchiveRetention)
	return rfc3339Ptr(&deletesAt)
}

func RunFromDomain(r *domain.Run) Run {
	return Run{
		ID:                  string(r.ID),
		WorkspaceID:         string(r.WorkspaceID),
		MemberID:            string(r.MemberID),
		AccountMemberID:     string(r.AccountMember()),
		Task:                r.Task,
		Title:               r.Title,
		Harness:             r.Harness,
		Mode:                string(r.Mode),
		ACP:                 r.ACP,
		Status:              string(r.Status),
		Reason:              r.Reason,
		PendingInputs:       []domain.RunInputRequest{},
		Branch:              r.Branch,
		LastCommit:          r.LastCommit,
		LastCommitAt:        rfc3339ValuePtr(r.LastCommitAt),
		Protected:           r.Protected,
		ArchivedAt:          rfc3339Ptr(r.ArchivedAt),
		DeletesAt:           runDeletesAt(r.ArchivedAt),
		OutcomeUnseen:       r.OutcomeUnseen,
		CreatedAt:           rfc3339(r.CreatedAt),
		StartedAt:           rfc3339Ptr(r.StartedAt),
		FinishedAt:          rfc3339Ptr(r.FinishedAt),
		ProfileSnapshotID:   string(r.ProfileSnapshotID),
		UnansweredQuestions: r.UnansweredQuestions,
		UnackedMessages:     r.UnackedMessages,
		OldestUnackedAt:     rfc3339Ptr(r.OldestUnackedAt),
		MissionID:           string(r.MissionID),
		MissionRole:         r.MissionRole,
		IntegratorRunID:     string(r.IntegratorRunID),
		BaseCommit:          r.BaseCommit,
		BaseBranch:          r.BaseBranch,
		BaseSource:          r.BaseSource,
		BaseCheckedAt:       rfc3339ValuePtr(r.BaseCheckedAt),
	}
}

func WorkspaceFromDomain(w *domain.Workspace) Workspace {
	return Workspace{
		ID:          string(w.ID),
		Name:        w.Name,
		BaseBranch:  w.BaseBranch,
		SteerOthers: w.SteerOthers,
		Origin:      w.Origin,
		CreatedAt:   rfc3339(w.CreatedAt),
	}
}

func MemberFromDomain(m *domain.Member) Member {
	return Member{
		ID:          string(m.ID),
		DisplayName: m.DisplayName,
		Color:       m.Color,
		Role:        string(m.Role),
		GitName:     m.GitName,
		GitEmail:    m.GitEmail,
		Pending:     m.Pending,
	}
}

// ServerInfoResult is the result of server.info; Member is the caller.
type ServerInfoResult struct {
	ServerVersion   string `json:"server_version"`
	ProtocolVersion string `json:"protocol_version"`
	Time            string `json:"time"`
	Member          Member `json:"member"`
	// MagicDNS name discovered once at startup; empty off a tailnet.
	TailnetHostname string `json:"tailnet_hostname,omitempty"`
	// Whether the server resolves tailnet identities (WhoIs) for keyless auth.
	TailnetIdentityAuth bool `json:"tailnet_identity_auth,omitempty"`
}

type WorkspaceListResult struct {
	Workspaces []Workspace `json:"workspaces"`
}

type WorkspaceGetParams struct {
	WorkspaceID string `json:"workspace_id"`
}

type WorkspaceGetResult struct {
	Workspace Workspace `json:"workspace"`
}

type MemberListResult struct {
	Members []Member `json:"members"`
}

// MemberApproveParams: member.approve is admin only.
type MemberApproveParams struct {
	MemberID string `json:"member_id"`
}

type MemberApproveResult struct {
	Member Member `json:"member"`
}

// AccountListResult: Accounts are those the caller may launch with;
// SharedWith are members allowed to launch with the caller's account.
type AccountListResult struct {
	Accounts   []Member `json:"accounts"`
	SharedWith []Member `json:"shared_with"`
}

// AccountMemberParams addresses the other side of an account share. For
// account.share/revoke the authenticated caller is always the account owner.
type AccountMemberParams struct {
	MemberID string `json:"member_id"`
}

// MemberColorParams: empty MemberID means the caller; anyone else requires
// the admin role.
type MemberColorParams struct {
	MemberID string `json:"member_id,omitempty"`
	Color    string `json:"color"`
}

type MemberColorResult struct {
	Member Member `json:"member"`
}

// MemberRenameParams: empty MemberID means the caller; anyone else requires
// the admin role.
type MemberRenameParams struct {
	MemberID    string `json:"member_id,omitempty"`
	DisplayName string `json:"display_name"`
}

type MemberRenameResult struct {
	Member Member `json:"member"`
}

// MemberGitParams: an empty field clears that half back to its fallback.
// MemberID defaults to the caller.
type MemberGitParams struct {
	MemberID string `json:"member_id,omitempty"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

type MemberGitResult struct {
	Member Member `json:"member"`
}

type GitHubConnectResult struct {
	Login       string `json:"login"`
	SigningKey  string `json:"signing_key"`
	Fingerprint string `json:"fingerprint"`
}

// GitHubProbeResult.Status is "ok", "missing" (no gh on PATH), "broken" (gh
// would not run) or "outdated".
type GitHubProbeResult struct {
	Status string `json:"status"`
	// Minimum is the oldest gh the login check can read.
	Version string `json:"version,omitempty"`
	Minimum string `json:"minimum"`
	// Detail is what gh, or the container that could not run it, printed.
	Detail string `json:"detail,omitempty"`
	// SavedImage is the caller's own saved image, if any.
	Image      string `json:"image"`
	SavedImage string `json:"saved_image,omitempty"`
	// Path is set only when gh resolves inside the caller's environment
	// home, so it outlives every image.
	Path string `json:"path,omitempty"`
	// AdminRemedy must run before Remedy; both are omitted while gh is fine.
	Remedy      string `json:"remedy,omitempty"`
	AdminRemedy string `json:"admin_remedy,omitempty"`
}

// RunLaunchParams.Task is optional in tui mode and required in headless
// mode.
type RunLaunchParams struct {
	WorkspaceID string `json:"workspace_id"`
	Task        string `json:"task,omitempty"`
	Harness     string `json:"harness"`
	Mode        string `json:"mode,omitempty"`
	// Empty means the caller's own account.
	AccountMemberID string `json:"account_member_id,omitempty"`
	// CachedBase retries a transient base-capture failure with the accepted
	// mirror commit; one launch consumes it.
	CachedBase string `json:"cached_base,omitempty"`
}

type RunListParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	MemberID    string `json:"member_id,omitempty"`
	ActiveOnly  bool   `json:"active_only,omitempty"`
}

type RunListResult struct {
	Runs []Run `json:"runs"`
}

// RunIDParams are the params of methods addressing one run by ID
// (run.get, run.kill, run.release, run.delete, run.pause, run.resume, run.relaunch, run.pull).
type RunIDParams struct {
	RunID string `json:"run_id"`
}

// RunResult is the result of methods returning one run
// (run.launch, run.get, run.close, run.relaunch).
type RunResult struct {
	Run Run `json:"run"`
}

// RunInjectParams.IdempotencyKey makes a retry return the original room
// mutation.
type RunInjectParams struct {
	RunID          string   `json:"run_id"`
	Message        string   `json:"message"`
	Attachments    []string `json:"attachments,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
	// Steer adds the message to an enhanced run's running turn instead of
	// queueing it.
	Steer bool `json:"steer,omitempty"`
	// Holding the control lease skips the moderation delay.
	ControlSessionID  string `json:"control_session_id,omitempty"`
	ControlGeneration uint64 `json:"control_generation,omitempty"`
}

// RunCloseParams.Outcome is "merged" or "abandoned".
type RunCloseParams struct {
	RunID   string `json:"run_id"`
	Outcome string `json:"outcome"`
}

type RunHandoffParams struct {
	RunID      string `json:"run_id"`
	ToMemberID string `json:"to_member_id"`
}

// RunProtectParams: run.protect is owner or admin only.
type RunProtectParams struct {
	RunID     string `json:"run_id"`
	Protected bool   `json:"protected"`
}

type RunArchiveParams struct {
	RunID    string `json:"run_id"`
	Archived bool   `json:"archived"`
}

// RunSeenParams: run.seen is owner only.
type RunSeenParams struct {
	RunID string `json:"run_id"`
}

// WorkspaceSettingsParams: admin only. SteerOthers is "" (permissive) or
// "admins_only".
type WorkspaceSettingsParams struct {
	WorkspaceID string `json:"workspace_id"`
	SteerOthers string `json:"steer_others"`
}

type WorkspaceSettingsResult struct {
	Workspace Workspace `json:"workspace"`
}

// WorkspaceOriginParams: an empty Origin clears it.
type WorkspaceOriginParams struct {
	WorkspaceID string `json:"workspace_id"`
	Origin      string `json:"origin"`
}

type WorkspaceOriginResult struct {
	Workspace Workspace `json:"workspace"`
}

// RunPullResult holds fetch coordinates; the transfer is a normal git fetch
// over the exec git transport.
type RunPullResult struct {
	WorkspaceID string `json:"workspace_id"`
	RepoPath    string `json:"repo_path"`
	Branch      string `json:"branch"`
}

// SubscribeRequest is the single line a client sends after opening the
// events subsystem.
type SubscribeRequest struct {
	WorkspaceID string   `json:"workspace_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	Types       []string `json:"types,omitempty"`
	Replay      bool     `json:"replay,omitempty"`
	AfterSeq    uint64   `json:"after_seq,omitempty"`
}

// On failure the server sends OK false with a code and closes the channel.
type SubscribeResponse struct {
	OK    bool   `json:"ok"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// TerminalEpoch identifies one logical terminal incarnation. An empty epoch
// is the legacy/unknown value and must never authorize a delta resume.
type TerminalEpoch string

// TerminalSequence counts client-visible terminal bytes in publication order.
type TerminalSequence uint64

// TerminalPosition's Epoch and Sequence must always be copied and compared
// together.
type TerminalPosition struct {
	Epoch    TerminalEpoch    `json:"resume_id,omitempty"`
	Sequence TerminalSequence `json:"cursor,omitempty"`
}

// Valid: sequence zero is a valid position; an empty epoch is not.
func (p TerminalPosition) Valid() bool { return p.Epoch != "" }

func terminalPosition(position TerminalPosition, resumeID string, cursor uint64) TerminalPosition {
	if position != (TerminalPosition{}) {
		return position
	}
	return TerminalPosition{Epoch: TerminalEpoch(resumeID), Sequence: TerminalSequence(cursor)}
}

func legacyTerminalPosition(position TerminalPosition, resumeID string, cursor uint64) (string, uint64) {
	position = terminalPosition(position, resumeID, cursor)
	return string(position.Epoch), uint64(position.Sequence)
}

// AttachRequest geometry precedence is pty-req > header > 80x24; a Follow
// client's geometry is ignored.
type AttachRequest struct {
	RunID    string `json:"run_id"`
	ReadOnly bool   `json:"read_only,omitempty"`
	// Screen asks for compact current state instead of full history.
	Screen bool `json:"screen,omitempty"`
	// Interactive enables NDJSON input/control frames on a framed attach.
	Interactive bool `json:"interactive,omitempty"`
	Cols        uint `json:"cols,omitempty"`
	Rows        uint `json:"rows,omitempty"`
	Framed      bool `json:"framed,omitempty"`
	// Shell requires write.
	Shell       string `json:"shell,omitempty"`
	Incarnation string `json:"incarnation,omitempty"`
	// Follow imposes no size: the PTY is the minimum over non-followers.
	// Framed output reports size changes before bytes drawn at that size.
	Follow bool `json:"follow,omitempty"`
	// Resume skips scrollback replay so taking control is a state change,
	// not a redraw.
	Resume bool `json:"resume,omitempty"`
	// Legacy; new code uses Position so epoch and sequence come from one
	// observation.
	Cursor   uint64 `json:"cursor,omitempty"`
	ResumeID string `json:"resume_id,omitempty"`
	// Position is encoded with the legacy flat resume_id and cursor keys.
	Position TerminalPosition `json:"-"`
	// One logical client tab, stable across reconnects.
	ControlSessionID string `json:"control_session_id,omitempty"`
	// ControlGeneration is the fenced generation the client expects.
	ControlGeneration uint64 `json:"control_generation,omitempty"`
	// Takeover explicitly displaces another controller.
	Takeover       bool `json:"takeover,omitempty"`
	ReleaseControl bool `json:"release_control,omitempty"`
}

func (r AttachRequest) ResumePosition() TerminalPosition {
	return terminalPosition(r.Position, r.ResumeID, r.Cursor)
}

// SetResumePosition updates the typed value and its legacy compatibility fields.
func (r *AttachRequest) SetResumePosition(position TerminalPosition) {
	r.Position = position
	r.ResumeID, r.Cursor = legacyTerminalPosition(position, "", 0)
}

// MarshalJSON preserves the legacy flat resume_id/cursor wire shape.
func (r AttachRequest) MarshalJSON() ([]byte, error) {
	type wire AttachRequest
	out := wire(r)
	out.ResumeID, out.Cursor = legacyTerminalPosition(r.Position, r.ResumeID, r.Cursor)
	return json.Marshal(out)
}

// UnmarshalJSON accepts old payloads with missing or zero position fields.
func (r *AttachRequest) UnmarshalJSON(data []byte) error {
	type wire AttachRequest
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = AttachRequest(decoded)
	r.Position = TerminalPosition{Epoch: TerminalEpoch(r.ResumeID), Sequence: TerminalSequence(r.Cursor)}
	return nil
}

// AttachResponse carries the session's live geometry, or the requested one
// when no session exists yet. On failure OK is false and the server closes.
type AttachResponse struct {
	OK                   bool   `json:"ok"`
	Cols                 uint   `json:"cols,omitempty"`
	Rows                 uint   `json:"rows,omitempty"`
	Framed               bool   `json:"framed,omitempty"`
	TerminalID           string `json:"terminal_id,omitempty"`
	Incarnation          string `json:"incarnation,omitempty"`
	ServerOwnedResponder bool   `json:"server_owned_responder,omitempty"`
	// Bytes of scrollback replay that follow the ack before live output.
	Replay int `json:"replay,omitempty"`
	// Legacy; Position is authoritative.
	Cursor   uint64 `json:"cursor,omitempty"`
	ResumeID string `json:"resume_id,omitempty"`
	// Position is encoded with the legacy flat resume_id and cursor keys.
	Position TerminalPosition `json:"-"`
	// Resumed false means the replay is the whole scrollback, not a delta.
	Resumed bool `json:"resumed,omitempty"`
	// ControllerID is the member currently holding writable control.
	ControllerID      string `json:"controller_id,omitempty"`
	ControlGeneration uint64 `json:"control_generation,omitempty"`
	// RFC3339.
	ControlExpiresAt string `json:"control_expires_at,omitempty"`
	HasControl       bool   `json:"has_control,omitempty"`
	Code             int    `json:"code,omitempty"`
	Error            string `json:"error,omitempty"`
}

func (r AttachResponse) HighWater() TerminalPosition {
	return terminalPosition(r.Position, r.ResumeID, r.Cursor)
}

// SetHighWater updates the typed value and its legacy compatibility fields.
func (r *AttachResponse) SetHighWater(position TerminalPosition) {
	r.Position = position
	r.ResumeID, r.Cursor = legacyTerminalPosition(position, "", 0)
}

// MarshalJSON quotes cursors above JavaScript's exact integer range.
func (r AttachResponse) MarshalJSON() ([]byte, error) {
	type wire AttachResponse
	out := wire(r)
	out.ResumeID, out.Cursor = legacyTerminalPosition(r.Position, r.ResumeID, r.Cursor)
	return json.Marshal(struct {
		wire
		Cursor any `json:"cursor,omitempty"`
	}{wire: out, Cursor: marshalTerminalCursor(out.Cursor)})
}

func (r *AttachResponse) UnmarshalJSON(data []byte) error {
	type wire AttachResponse
	var decoded struct {
		wire
		Cursor json.RawMessage `json:"cursor"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	cursor, err := unmarshalTerminalCursor(decoded.Cursor)
	if err != nil {
		return err
	}
	decoded.wire.Cursor = cursor
	*r = AttachResponse(decoded.wire)
	r.Position = TerminalPosition{Epoch: TerminalEpoch(r.ResumeID), Sequence: TerminalSequence(r.Cursor)}
	return nil
}

// Attach exit statuses for a live attach dropped by a failed authorization
// re-check. 0 and 1 are a normal end and a post-ack failure.
const (
	// A read-only attach of the same run is still allowed.
	AttachExitSteerRevoked = 3
	// Every attach of the member ends, read-only ones included.
	AttachExitMembershipRevoked = 4
	// Another tab took control or the lease lapsed; not a permission
	// withdrawal, so the client may reconnect read-only.
	AttachExitControlRevoked = 5
)

// RemoteExitError is a nonzero subsystem exit status, from SSH or the
// in-process transport.
type RemoteExitError struct{ Status int }

func (e *RemoteExitError) Error() string {
	return fmt.Sprintf("remote exited with status %d", e.Status)
}

// WorkspaceSelector addresses a workspace by exactly one of ID or Name.
type WorkspaceSelector struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// SyncRequest.Force overrides the refusal while the run is `running`.
type SyncRequest struct {
	RunID string `json:"run_id"`
	Force bool   `json:"force,omitempty"`
}

// After an OK SyncResponse the stream is a mutagen remote endpoint session
// rooted at the run's worktree.
type SyncResponse struct {
	OK    bool   `json:"ok"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// SyncConflictParams: the target resolver reads run_id, so the Steer gate
// applies to the run the overlay was opened against.
type SyncConflictParams struct {
	RunID         string   `json:"run_id"`
	SyncSessionID string   `json:"sync_session_id,omitempty"`
	Files         []string `json:"files"`
}

// AgentDefinition paths are absolute container paths; validation lives in
// internal/harness.Definition.Validate.
type AgentDefinition struct {
	Name            string   `json:"name"`
	Executable      string   `json:"executable"`
	TUIArgs         []string `json:"tui_args"`
	HeadlessArgs    []string `json:"headless_args"`
	ACPArgs         []string `json:"acp_args,omitempty"`
	ProfileRoot     string   `json:"profile_root,omitempty"`
	CredentialPaths []string `json:"credential_paths,omitempty"`
	DenyNames       []string `json:"deny_names,omitempty"`
}

type AgentRegisterParams struct {
	Definition AgentDefinition `json:"definition"`
}

type AgentRegisterResult struct {
	Definition AgentDefinition `json:"definition"`
}

type AgentListResult struct {
	Agents []AgentInfo `json:"agents"`
}

// AgentListParams: empty means the caller's own account.
type AgentListParams struct {
	AccountMemberID string `json:"account_member_id,omitempty"`
}

// AgentInfo.Source is "shipped" or "member".
type AgentInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// Glyph is the shipped agent's name, or "custom" for a member's own.
	Glyph     string `json:"glyph"`
	Source    string `json:"source"`
	Installed bool   `json:"installed"`
	// Enhanced is "native", "adapter" or "none".
	Enhanced          string `json:"enhanced"`
	EnhancedInstalled bool   `json:"enhanced_installed"`
	Switchable        bool   `json:"switchable"`
	// LoginFound checks the launch home for login paths, not for a working
	// session.
	LoginFound bool `json:"login_found"`
	// "acp" when enhanced mode is installed and preferred, else "tui".
	DefaultMode string `json:"default_mode"`
	// EnhancedDefault: the agent prefers enhanced mode, installed or not.
	EnhancedDefault bool `json:"enhanced_default"`
	// Empty for member-owned custom agents.
	InstallScript string `json:"install_script,omitempty"`
	// InstallScript followed by the pinned adapter's install.
	EnhancedInstallScript string `json:"enhanced_install_script,omitempty"`
	// Why a launch on a shared account would be refused; at most one is set,
	// and none for the caller's own account.
	LoginMissing   bool   `json:"login_missing,omitempty"`
	OwnAccountOnly bool   `json:"own_account_only,omitempty"`
	Unavailable    string `json:"unavailable,omitempty"`
}

type AgentInstallParams struct {
	Name     string `json:"name"`
	Enhanced bool   `json:"enhanced,omitempty"`
}

// AgentInstallResult's install flags are read from the home even when Error
// is set.
type AgentInstallResult struct {
	LogTail           string `json:"log_tail"`
	Installed         bool   `json:"installed"`
	EnhancedInstalled bool   `json:"enhanced_installed"`
	Error             string `json:"error,omitempty"`
}
