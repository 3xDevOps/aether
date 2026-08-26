package protocol

import "strings"

// WorkspaceEnvironment is the wire form of a workspace environment
// definition. An empty custom image selects the server neutral image.
type WorkspaceEnvironment struct {
	CustomImage  string            `json:"custom_image,omitempty"`
	NeutralImage bool              `json:"neutral_image,omitempty"`
	Variables    map[string]string `json:"variables,omitempty"`
	SetupPolicy  SetupPolicy       `json:"setup_policy,omitempty"`
}

// SetupPolicy is the wire form of the pre-launch setup policy.
type SetupPolicy struct {
	Script string `json:"script,omitempty"`
}

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

// MemberRoleParams are the params of member.role (admin only).
type MemberRoleParams struct {
	MemberID string `json:"member_id"`
	Role     string `json:"role"`
}

// MemberRoleResult is the result of member.role.
type MemberRoleResult struct {
	Member Member `json:"member"`
}
