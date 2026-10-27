package domain

import "time"

// A run's own child-run setting; "" follows its owner's.
const (
	ChildrenOn  = "on"
	ChildrenOff = "off"
)

// ChildReservation states. A reservation is queued until a slot frees,
// launching while the scheduler provisions it, and started once its run
// row exists; cancelled and refused never got one.
type ChildState string

const (
	ChildQueued    ChildState = "queued"
	ChildLaunching ChildState = "launching"
	ChildStarted   ChildState = "started"
	ChildCancelled ChildState = "cancelled"
	ChildRefused   ChildState = "refused"
)

// ChildLimits bound one tree of child runs. Busy children are provisioning
// or working; queued ones wait for a slot.
type ChildLimits struct {
	MaxLive  int
	MaxDepth int
	MaxTotal int
}

// MaxChildLimits are the largest values an admin may set.
var MaxChildLimits = ChildLimits{MaxLive: 64, MaxDepth: 8, MaxTotal: 2000}

// ChildReservation is one child run a run asked for. RunID is minted at
// reservation, before the run row exists. TreeKey groups the runs that
// share one allowance: the swarm's ID for swarm runs and their
// descendants, otherwise the root run's ID.
type ChildReservation struct {
	RunID          RunID
	ParentRunID    RunID
	RootRunID      RunID
	TreeKey        string
	MissionID      MissionID
	WorkspaceID    WorkspaceID
	OwnerID        MemberID
	AccountID      MemberID
	Harness        string
	Mode           LaunchMode
	Model          string
	Purpose        string
	Task           string
	Depth          int
	BaseCommit     string
	BaseBranch     string
	Fallback       AccountFallback
	IdempotencyKey string
	// RequestHash fingerprints the start request as the agent sent it, so
	// a retry must repeat it exactly.
	RequestHash string
	State       ChildState
	Detached    bool
	Error       string
	CreatedAt   time.Time
	StartedAt   *time.Time
	// EndNotified is set once the parent was told the child ended without a
	// report.
	EndNotified bool
}
