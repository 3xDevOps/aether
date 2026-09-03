package protocol

import (
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Run is the wire form of a run. Times are RFC3339; server-side host paths
// never appear on the wire. Reason is the last run.status reason,
// sanitized server-side. Paused reports a frozen container; it is
// decorated by handlers from the scheduler, never derived from the
// stored run.
type Run struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	MemberID    string `json:"member_id"`
	Task        string `json:"task"`
	Harness     string `json:"harness"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	// Paused has no omitempty: absence must keep meaning "gateway too old
	// to know", never "not paused", or clients cannot seed pause state.
	Paused            bool    `json:"paused"`
	Branch            string  `json:"branch"`
	Protected         bool    `json:"protected,omitempty"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         *string `json:"started_at"`
	FinishedAt        *string `json:"finished_at"`
	ProfileSnapshotID string  `json:"profile_snapshot_id,omitempty"`
}

// Workspace is the wire form of a workspace; image, env, and setup script
// stay server-side.
type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseBranch  string `json:"base_branch"`
	SteerOthers string `json:"steer_others,omitempty"`
	CreatedAt   string `json:"created_at"`
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

// RunFromDomain converts a domain run to its wire form.
func RunFromDomain(r *domain.Run) Run {
	return Run{
		ID:                string(r.ID),
		WorkspaceID:       string(r.WorkspaceID),
		MemberID:          string(r.MemberID),
		Task:              r.Task,
		Harness:           r.Harness,
		Mode:              string(r.Mode),
		Status:            string(r.Status),
		Reason:            r.Reason,
		Branch:            r.Branch,
		Protected:         r.Protected,
		CreatedAt:         rfc3339(r.CreatedAt),
		StartedAt:         rfc3339Ptr(r.StartedAt),
		FinishedAt:        rfc3339Ptr(r.FinishedAt),
		ProfileSnapshotID: string(r.ProfileSnapshotID),
	}
}

// WorkspaceFromDomain converts a domain workspace to its wire form.
func WorkspaceFromDomain(w *domain.Workspace) Workspace {
	return Workspace{
		ID:          string(w.ID),
		Name:        w.Name,
		BaseBranch:  w.BaseBranch,
		SteerOthers: w.SteerOthers,
		CreatedAt:   rfc3339(w.CreatedAt),
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
	Member          Member `json:"member"`
	// TailnetHostname is the server's MagicDNS name, discovered once at
	// startup; empty when the server is not on a tailnet.
	TailnetHostname string `json:"tailnet_hostname,omitempty"`
	// TailnetIdentityAuth reports whether the server resolves tailnet
	// identities (WhoIs) for keyless auth.
	TailnetIdentityAuth bool `json:"tailnet_identity_auth,omitempty"`
	// NeutralImage is the server-owned image used for workspaces whose
	// environment selects the neutral base.
	NeutralImage string `json:"neutral_image,omitempty"`
	// StandardImage is the published standard environment image the
	// server recommends for new workspaces; clients pin it as the
	// workspace's custom image. Absent on servers predating it.
	StandardImage string `json:"standard_image,omitempty"`
}

// WorkspaceListResult is the result of workspace.list.
type WorkspaceListResult struct {
	Workspaces []Workspace `json:"workspaces"`
}

// WorkspaceGetParams are the params of workspace.get.
type WorkspaceGetParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceGetResult is the result of workspace.get.
type WorkspaceGetResult struct {
	Workspace Workspace `json:"workspace"`
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

// RunLaunchParams are the params of run.launch. Task is optional in the
// default tui mode - an empty task drops the member into the agent's
// interactive TUI with no seeded prompt - but required in headless mode,
// which has no interactive surface.
type RunLaunchParams struct {
	WorkspaceID string `json:"workspace_id"`
	Task        string `json:"task,omitempty"`
	Harness     string `json:"harness"`
	Mode        string `json:"mode,omitempty"`
}

// RunListParams are the params of run.list.
type RunListParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	MemberID    string `json:"member_id,omitempty"`
	ActiveOnly  bool   `json:"active_only,omitempty"`
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

// WorkspaceSettingsParams are the params of workspace.settings (admin
// only). SteerOthers is "" (permissive default) or "admins_only".
type WorkspaceSettingsParams struct {
	WorkspaceID string `json:"workspace_id"`
	SteerOthers string `json:"steer_others"`
}

// WorkspaceSettingsResult is the result of workspace.settings.
type WorkspaceSettingsResult struct {
	Workspace Workspace `json:"workspace"`
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
	WorkspaceID string   `json:"workspace_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	Types       []string `json:"types,omitempty"`
	Replay      bool     `json:"replay,omitempty"`
	AfterSeq    uint64   `json:"after_seq,omitempty"`
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
	// Shell names a shell tab inside the run container; write is required.
	Shell string `json:"shell,omitempty"`
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

// Exit statuses of the attach subsystem. 0 is the run's terminal session
// ending and 1 an attach that failed after its ack; these two name the
// server dropping a live attach because its authorization re-check
// failed, so a client can say why it was detached instead of only that
// it was.
const (
	// AttachExitSteerRevoked: a write attach lost the steer capability
	// (role change, handoff, run protection, or the workspace's steering
	// policy). A read-only attach of the same run is still allowed.
	AttachExitSteerRevoked = 3
	// AttachExitMembershipRevoked: the member was removed or set back to
	// pending. Every attach of theirs ends, read-only ones included.
	AttachExitMembershipRevoked = 4
)

// WorkspaceSelector addresses a workspace by exactly one of ID or Name.
type WorkspaceSelector struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
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

// AgentDefinition is the wire form of a member-supplied custom harness
// launch definition. Paths are absolute container paths; validation lives
// in internal/harness.Definition.Validate.
type AgentDefinition struct {
	Name            string   `json:"name"`
	Executable      string   `json:"executable"`
	TUIArgs         []string `json:"tui_args"`
	HeadlessArgs    []string `json:"headless_args"`
	ProfileRoot     string   `json:"profile_root,omitempty"`
	CredentialPaths []string `json:"credential_paths,omitempty"`
	DenyNames       []string `json:"deny_names,omitempty"`
}

// AgentRegisterParams are the params of agent.register.
type AgentRegisterParams struct {
	Definition AgentDefinition `json:"definition"`
}

// AgentRegisterResult is the result of agent.register: the stored
// definition echoed back.
type AgentRegisterResult struct {
	Definition AgentDefinition `json:"definition"`
}

// AgentListResult is the result of agent.list.
type AgentListResult struct {
	Agents []AgentInfo `json:"agents"`
}

// AgentInfo is one entry of agent.list; Source is "shipped" or "member".
type AgentInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// InstallScript is the shipped harness's vendor install command. It is
	// empty for member-owned custom agents.
	InstallScript string `json:"install_script,omitempty"`
}
