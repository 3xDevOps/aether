package sshd

import (
	"context"
	"time"

	"github.com/3xDevOps/Aether/internal/approvals"
	"github.com/3xDevOps/Aether/internal/domain"
)

// ApprovalService is the seam for the presence roster. Satisfied by
// *approvals.Service.
type ApprovalService interface {
	// ConnectionOpened records an authenticated SSH connection.
	ConnectionOpened(member domain.MemberID)
	// ConnectionClosed releases an authenticated SSH connection.
	ConnectionClosed(member domain.MemberID)
	// Heartbeat refreshes a member's presence in a workspace.
	Heartbeat(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) error
	// Roster lists present members, narrowed to a workspace and to the
	// watchers of one run when either is given.
	Roster(workspace domain.WorkspaceID, run domain.RunID) []approvals.Presence
	// TTL is how long presence survives without a heartbeat.
	TTL() time.Duration
}
