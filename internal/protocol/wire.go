package protocol

import (
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Run is the wire form of a run. Times are RFC3339; server-side host paths
// never appear on the wire.
type Run struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	MemberID          string  `json:"member_id"`
	Task              string  `json:"task"`
	Harness           string  `json:"harness"`
	Mode              string  `json:"mode"`
	Status            string  `json:"status"`
	Branch            string  `json:"branch"`
	Protected         bool    `json:"protected,omitempty"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         *string `json:"started_at"`
	FinishedAt        *string `json:"finished_at"`
	ProfileSnapshotID string  `json:"profile_snapshot_id,omitempty"`
	ToolSnapshotID    string  `json:"tool_snapshot_id,omitempty"`
}

// Session is the wire form of a session.
type Session struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	BaseBranch  string `json:"base_branch"`
	SteerOthers string `json:"steer_others,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// Workspace is the wire form of a workspace; image, env, and setup script
// stay server-side.
type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// Member is the wire form of a member. Pending appears only while the
// member awaits admin approval.
type Member struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Color       string `json:"color"`
	Role        string `json:"role"`
	Pending     bool   `json:"pending,omitempty"`
}

// Event is the wire envelope streamed on the events subsystem. Payload is
// the raw JSON of the corresponding internal/events payload struct.
type Event struct {
	ID        string          `json:"id"`
	Seq       uint64          `json:"seq"`
	Time      string          `json:"time"`
	SessionID string          `json:"session_id"`
	RunID     string          `json:"run_id"`
	ActorID   string          `json:"actor_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

// RunFromDomain converts a domain run to its wire form.
func RunFromDomain(r *domain.Run) Run {
	return Run{
		ID:                string(r.ID),
		SessionID:         string(r.SessionID),
		MemberID:          string(r.MemberID),
		Task:              r.Task,
		Harness:           r.Harness,
		Mode:              string(r.Mode),
		Status:            string(r.Status),
		Branch:            r.Branch,
		Protected:         r.Protected,
		CreatedAt:         rfc3339(r.CreatedAt),
		StartedAt:         rfc3339Ptr(r.StartedAt),
		FinishedAt:        rfc3339Ptr(r.FinishedAt),
		ProfileSnapshotID: string(r.ProfileSnapshotID),
		ToolSnapshotID:    string(r.ToolSnapshotID),
	}
}

// SessionFromDomain converts a domain session to its wire form.
func SessionFromDomain(s *domain.Session) Session {
	return Session{
		ID:          string(s.ID),
		WorkspaceID: string(s.WorkspaceID),
		Name:        s.Name,
		BaseBranch:  s.BaseBranch,
		SteerOthers: s.SteerOthers,
		CreatedAt:   rfc3339(s.CreatedAt),
	}
}

// WorkspaceFromDomain converts a domain workspace to its wire form.
func WorkspaceFromDomain(w *domain.Workspace) Workspace {
	return Workspace{
		ID:        string(w.ID),
		Name:      w.Name,
		CreatedAt: rfc3339(w.CreatedAt),
	}
}

// MemberFromDomain converts a domain member to its wire form.
func MemberFromDomain(m *domain.Member) Member {
	return Member{
		ID:          string(m.ID),
		DisplayName: m.DisplayName,
		Color:       m.Color,
		Role:        string(m.Role),
		Pending:     m.Pending,
	}
}

// ServerInfoResult is the result of server.info; Member is the caller.
type ServerInfoResult struct {
	ServerVersion   string `json:"server_version"`
	ProtocolVersion string `json:"protocol_version"`
	Time            string `json:"time"`
	DashboardPort   int    `json:"dashboard_port"`
	Member          Member `json:"member"`
	// TailnetHostname is the server's MagicDNS name, discovered once at
	// startup; empty when the server is not on a tailnet.
	TailnetHostname string `json:"tailnet_hostname,omitempty"`
	// TailnetIdentityAuth reports whether the server resolves tailnet
	// identities (WhoIs) for keyless auth.
	TailnetIdentityAuth bool `json:"tailnet_identity_auth,omitempty"`
}

// WorkspaceListResult is the result of workspace.list.
type WorkspaceListResult struct {
	Workspaces []Workspace `json:"workspaces"`
}

// SessionListParams are the params of session.list.
type SessionListParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// SessionListResult is the result of session.list.
type SessionListResult struct {
	Sessions []Session `json:"sessions"`
}

// SessionGetParams are the params of session.get.
type SessionGetParams struct {
	SessionID string `json:"session_id"`
}

// SessionGetResult is the result of session.get.
type SessionGetResult struct {
	Session Session `json:"session"`
}

// MemberListResult is the result of member.list.
type MemberListResult struct {
	Members []Member `json:"members"`
}

// MemberApproveParams are the params of member.approve (admin only):
// clears the target member's pending flag.
type MemberApproveParams struct {
	MemberID string `json:"member_id"`
}

// MemberApproveResult is the result of member.approve.
type MemberApproveResult struct {
	Member Member `json:"member"`
}

// MemberColorParams are the params of member.color: set a member's
// attribution color. MemberID empty means the caller; setting anyone
// else's color requires the admin role.
type MemberColorParams struct {
	MemberID string `json:"member_id,omitempty"`
	Color    string `json:"color"`
}

// MemberColorResult is the result of member.color.
type MemberColorResult struct {
	Member Member `json:"member"`
}

// RunLaunchParams are the params of run.launch.
type RunLaunchParams struct {
	SessionID string `json:"session_id"`
	Task      string `json:"task"`
	Harness   string `json:"harness"`
	Mode      string `json:"mode,omitempty"`
}

// RunListParams are the params of run.list.
type RunListParams struct {
	SessionID  string `json:"session_id,omitempty"`
	MemberID   string `json:"member_id,omitempty"`
	ActiveOnly bool   `json:"active_only,omitempty"`
}

// RunListResult is the result of run.list.
type RunListResult struct {
	Runs []Run `json:"runs"`
}

// RunIDParams are the params of methods addressing one run by ID
// (run.get, run.kill, run.pause, run.resume, run.relaunch, run.pull).
type RunIDParams struct {
	RunID string `json:"run_id"`
}

// RunResult is the result of methods returning one run
// (run.launch, run.get, run.close, run.relaunch).
type RunResult struct {
	Run Run `json:"run"`
}

// RunInjectParams are the params of run.inject.
type RunInjectParams struct {
	RunID   string `json:"run_id"`
	Message string `json:"message"`
}

// RunCloseParams are the params of run.close; Outcome is "merged" or
// "abandoned".
type RunCloseParams struct {
	RunID   string `json:"run_id"`
	Outcome string `json:"outcome"`
}

// RunHandoffParams are the params of run.handoff.
type RunHandoffParams struct {
	RunID      string `json:"run_id"`
	ToMemberID string `json:"to_member_id"`
}

// RunProtectParams are the params of run.protect (owner or admin):
// toggles the run's protected flag.
type RunProtectParams struct {
	RunID     string `json:"run_id"`
	Protected bool   `json:"protected"`
}

// SessionSettingsParams are the params of session.settings (admin only).
// SteerOthers is "" (permissive default) or "admins_only".
type SessionSettingsParams struct {
	SessionID   string `json:"session_id"`
	SteerOthers string `json:"steer_others"`
}

// SessionSettingsResult is the result of session.settings.
type SessionSettingsResult struct {
	Session Session `json:"session"`
}

// RunPullResult is the result of run.pull: fetch coordinates for the run's
// branch; the transfer itself is a normal git fetch over the exec git
// transport.
type RunPullResult struct {
	WorkspaceID string `json:"workspace_id"`
	RepoPath    string `json:"repo_path"`
	Branch      string `json:"branch"`
}

// SubscribeRequest is the single negotiation line a client sends after
// opening the events subsystem.
type SubscribeRequest struct {
	SessionID string   `json:"session_id,omitempty"`
	RunID     string   `json:"run_id,omitempty"`
	Types     []string `json:"types,omitempty"`
	Replay    bool     `json:"replay,omitempty"`
	AfterSeq  uint64   `json:"after_seq,omitempty"`
}

// SubscribeResponse acknowledges a SubscribeRequest; on failure the server
// sends OK false with a code and closes the channel.
type SubscribeResponse struct {
	OK    bool   `json:"ok"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// AttachRequest is the single header line a client sends after opening the
// attach subsystem. Geometry precedence is pty-req > header > 80x24.
type AttachRequest struct {
	RunID    string `json:"run_id"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Cols     uint   `json:"cols,omitempty"`
	Rows     uint   `json:"rows,omitempty"`
}

// AttachResponse acknowledges an AttachRequest with the effective
// geometry; on failure the server sends OK false with a code and closes.
type AttachResponse struct {
	OK    bool   `json:"ok"`
	Cols  uint   `json:"cols,omitempty"`
	Rows  uint   `json:"rows,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// WorkspaceSelector addresses a workspace by exactly one of ID or Name.
type WorkspaceSelector struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// WorkspaceShellMode identifies the purpose of a workspace shell.
type WorkspaceShellMode = domain.WorkspaceShellMode

const (
	WorkspaceShellBootstrapTools     = domain.WorkspaceShellBootstrapTools
	WorkspaceShellHarnessLogin       = domain.WorkspaceShellHarnessLogin
	WorkspaceShellModeBootstrapTools = domain.WorkspaceShellBootstrapTools
	WorkspaceShellModeHarnessLogin   = domain.WorkspaceShellHarnessLogin
)

// WorkspaceShellRequest is the single header line a client sends after
// opening the unified workspace-shell subsystem. Geometry precedence is
// pty-req > header > 80x24.
type WorkspaceShellRequest struct {
	Workspace              WorkspaceSelector  `json:"workspace"`
	Mode                   WorkspaceShellMode `json:"mode"`
	Harness                string             `json:"harness,omitempty"`
	VerificationExecutable string             `json:"verification_executable,omitempty"`
	Resume                 bool               `json:"resume,omitempty"`
	Reset                  bool               `json:"reset,omitempty"`
	Cols                   uint               `json:"cols,omitempty"`
	Rows                   uint               `json:"rows,omitempty"`
}

// Validate checks the server-facing request contract.
func (r WorkspaceShellRequest) Validate() error {
	return (domain.WorkspaceShellRequest{
		Workspace:              domain.WorkspaceSelector{ID: domain.WorkspaceID(r.Workspace.ID), Name: r.Workspace.Name},
		Mode:                   r.Mode,
		Harness:                r.Harness,
		VerificationExecutable: r.VerificationExecutable,
		Resume:                 r.Resume,
		Reset:                  r.Reset,
		Cols:                   r.Cols,
		Rows:                   r.Rows,
	}).Validate()
}

// WorkspaceShellResponse acknowledges a WorkspaceShellRequest with the
// effective geometry and echoed shell selection.
type WorkspaceShellResponse struct {
	OK                     bool               `json:"ok"`
	Workspace              WorkspaceSelector  `json:"workspace,omitempty"`
	Mode                   WorkspaceShellMode `json:"mode,omitempty"`
	Harness                string             `json:"harness,omitempty"`
	VerificationExecutable string             `json:"verification_executable,omitempty"`
	Resume                 bool               `json:"resume,omitempty"`
	Reset                  bool               `json:"reset,omitempty"`
	Cols                   uint               `json:"cols,omitempty"`
	Rows                   uint               `json:"rows,omitempty"`
	Code                   int                `json:"code,omitempty"`
	Error                  string             `json:"error,omitempty"`
}

// SyncRequest is the single header line a client sends after opening the
// sync subsystem. Force overrides the mid-write refusal: without it the
// server rejects the bridge while the run is `running`.
type SyncRequest struct {
	RunID string `json:"run_id"`
	Force bool   `json:"force,omitempty"`
}

// SyncResponse acknowledges a SyncRequest; after an OK the raw remaining
// stream is a mutagen remote endpoint protocol session rooted at the
// run's worktree. On failure the server sends OK false with a code and
// closes.
type SyncResponse struct {
	OK    bool   `json:"ok"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// SyncConflictParams are the params of sync.conflict: a paused live
// overlay reporting its conflicted paths so the server can notify both
// affected members. The target resolver reads run_id, so the Steer gate
// applies to the same run the overlay was opened against.
type SyncConflictParams struct {
	RunID         string   `json:"run_id"`
	SyncSessionID string   `json:"sync_session_id,omitempty"`
	Files         []string `json:"files"`
}
