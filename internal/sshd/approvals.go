package sshd

import (
	"context"
	"time"

	"github.com/3xDevOps/Aether/internal/approvals"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// ApprovalService is the seam for the shared approval inbox and the
// presence roster. Satisfied by *approvals.Service.
type ApprovalService interface {
	// List returns a workspace's inbox, pending requests only unless all.
	List(ctx context.Context, workspace domain.WorkspaceID, all bool) ([]*store.Approval, error)
	// Decide records by's decision on a pending request. run is the run
	// the caller's steer capability was checked against; a request
	// belonging to another run is refused.
	Decide(ctx context.Context, id string, run domain.RunID, approve bool, by domain.MemberID) (*store.Approval, error)
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
