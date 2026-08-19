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
	// List returns a session's inbox, pending requests only unless all.
	List(ctx context.Context, session domain.SessionID, all bool) ([]*store.Approval, error)
	// Decide records by's decision on a pending request. run is the run
	// the caller's steer capability was checked against; a request
	// belonging to another run is refused.
	Decide(ctx context.Context, id string, run domain.RunID, approve bool, by domain.MemberID) (*store.Approval, error)
	// Heartbeat refreshes a member's presence in a session.
	Heartbeat(ctx context.Context, member domain.MemberID, session domain.SessionID) error
	// Roster lists present members, narrowed to a session and to the
	// watchers of one run when either is given.
	Roster(session domain.SessionID, run domain.RunID) []approvals.Presence
	// TTL is how long presence survives without a heartbeat.
	TTL() time.Duration
}
