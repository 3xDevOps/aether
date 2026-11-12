package protocol

import "strings"

// WorkspaceEnvironment is the wire form of a workspace environment.
type WorkspaceEnvironment struct {
	Variables   map[string]string `json:"variables,omitempty"`
	SetupPolicy SetupPolicy       `json:"setup_policy,omitempty"`
}

// SetupPolicy is the wire form of the pre-launch setup policy.
type SetupPolicy struct {
	Script string `json:"script,omitempty"`
}

func (e WorkspaceEnvironment) Valid() bool {
	for name := range e.Variables {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return false
		}
	}
	return true
}

// WorkspaceAddParams are the params of workspace.add (admin only).
// BaseBranch defaults to "main" when empty.
type WorkspaceAddParams struct {
	Name        string               `json:"name"`
	Environment WorkspaceEnvironment `json:"environment"`
	BaseBranch  string               `json:"base_branch,omitempty"`
}

// WorkspaceAddResult is the result of workspace.add.
type WorkspaceAddResult struct {
	Workspace Workspace `json:"workspace"`
}

// WorkspaceDeleteParams permanently removes an inactive workspace (admin only).
type WorkspaceDeleteParams struct {
	WorkspaceID string `json:"workspace_id"`
}

type WorkspaceDeleteResult struct {
	OK bool `json:"ok"`
}

// MemberInviteParams are the params of member.invite (admin only).
// TTLSeconds defaults to 86400 when zero.
type MemberInviteParams struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
	// WorkspaceIDs are granted to the member the code admits. An admin
	// names at least one, and only workspaces they own.
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
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

// MemberRoleParams are the params of member.role (admin only).
type MemberRoleParams struct {
	MemberID string `json:"member_id"`
	Role     string `json:"role"`
}

// MemberRoleResult is the result of member.role.
type MemberRoleResult struct {
	Member Member `json:"member"`
}

// WorkspaceAccessParams name one workspace (workspace.access.list).
type WorkspaceAccessParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceGrant is the wire form of a grant.
type WorkspaceGrant struct {
	MemberID  string `json:"member_id"`
	GrantedBy string `json:"granted_by,omitempty"`
	CreatedAt string `json:"created_at"`
}

// WorkspaceAccessResult is who reaches a workspace: its owner, and every
// member granted it. Super admins reach it without a grant.
type WorkspaceAccessResult struct {
	WorkspaceID   string           `json:"workspace_id"`
	OwnerMemberID string           `json:"owner_member_id"`
	Grants        []WorkspaceGrant `json:"grants"`
}

// WorkspaceAccessGrantParams are the params of workspace.access.grant.
type WorkspaceAccessGrantParams struct {
	WorkspaceID string   `json:"workspace_id"`
	MemberIDs   []string `json:"member_ids"`
}

// WorkspaceAccessRevokeParams are the params of workspace.access.revoke.
type WorkspaceAccessRevokeParams struct {
	WorkspaceID string `json:"workspace_id"`
	MemberID    string `json:"member_id"`
}

// WorkspaceOwnerSetParams are the params of workspace.owner.set.
type WorkspaceOwnerSetParams struct {
	WorkspaceID string `json:"workspace_id"`
	MemberID    string `json:"member_id"`
}
