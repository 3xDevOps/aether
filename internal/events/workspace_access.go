package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeWorkspaceAccess reports that a member was granted or revoked a
// workspace, or became its owner. It is published in that workspace.
const TypeWorkspaceAccess Type = "workspace.access"

// Workspace access actions.
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

func init() { registerPayload[WorkspaceAccessPayload](TypeWorkspaceAccess) }
