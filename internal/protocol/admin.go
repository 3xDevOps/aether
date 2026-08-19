package protocol

// WorkspaceAddParams are the params of workspace.add (admin only).
type WorkspaceAddParams struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Env         map[string]string `json:"env,omitempty"`
	SetupScript string            `json:"setup_script,omitempty"`
}

// WorkspaceAddResult is the result of workspace.add.
type WorkspaceAddResult struct {
	Workspace Workspace `json:"workspace"`
}

// SessionNewParams are the params of session.new. BaseBranch defaults to
// "main" when empty. Any non-pending member may call this.
type SessionNewParams struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	BaseBranch  string `json:"base_branch,omitempty"`
}

// SessionNewResult is the result of session.new.
type SessionNewResult struct {
	Session Session `json:"session"`
}

// MemberInviteParams are the params of member.invite (admin only).
// TTLSeconds defaults to 86400 when zero.
type MemberInviteParams struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// MemberInviteResult is the result of member.invite: the one-time code
// (shown once) and its expiry.
type MemberInviteResult struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// MemberRemoveParams are the params of member.remove (admin only).
type MemberRemoveParams struct {
	MemberID string `json:"member_id"`
}
