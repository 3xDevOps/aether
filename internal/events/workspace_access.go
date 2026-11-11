package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeWorkspaceAccess reports that a member was granted or revoked a
// workspace, or became its owner. It is published in that workspace.
const TypeWorkspaceAccess Type = "workspace.access"

// TypeMemberAccess tells the member a workspace.access event is about. It
// is published under ServerScope and streamed to them alone, so it reaches
// a member who just lost the workspace and with it that event.
const TypeMemberAccess Type = "member.access"

const (
	AccessGranted = "granted"
	AccessRevoked = "revoked"
	AccessOwner   = "owner"
)

type WorkspaceAccessPayload struct {
	WorkspaceID domain.WorkspaceID `json:"workspace_id"`
	MemberID    domain.MemberID    `json:"member_id"`
	Action      string             `json:"action"`
}

func (WorkspaceAccessPayload) EventType() Type { return TypeWorkspaceAccess }

type MemberAccessPayload WorkspaceAccessPayload

func (MemberAccessPayload) EventType() Type { return TypeMemberAccess }

func (p MemberAccessPayload) Recipient() domain.MemberID { return p.MemberID }

func init() {
	registerPayload[WorkspaceAccessPayload](TypeWorkspaceAccess)
	registerPayload[MemberAccessPayload](TypeMemberAccess)
}
