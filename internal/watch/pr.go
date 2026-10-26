package watch

import (
	"context"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ErrTransient marks a PRSource failure that may pass, such as GitHub
// being unreachable or rate limiting. watch.create still creates the watch,
// in the error state, and retries it; any other Validate error refuses it.
var ErrTransient = errors.New("watch: transient failure")

// Notice is one notification an evaluation found due. Key identifies what
// it announces, so the same Condition and Key are recorded once per watch.
type Notice struct {
	Condition string
	Key       string
	Title     string
	Body      string
	Link      string
}

// PRCheck is one evaluation of the pull request conditions of a watch: a
// pr target's, or the pr_merged and ci conditions of a run target, whose
// pull request is the one the run recorded. Previous is what the last
// evaluation observed.
type PRCheck struct {
	Member     domain.MemberID
	Target     protocol.WatchTarget
	Conditions []string
	Previous   protocol.WatchObserved
	// Since is when the watch was created: nothing that happened before it
	// is announced.
	Since time.Time
}

// PRResult is what a PRCheck read. Final means the pull request reached a
// state no condition can change from (merged or closed). NextCheck is when
// to read it again when no event arrives first; zero leaves it to events.
type PRResult struct {
	Title     string
	Observed  protocol.WatchObserved
	Notices   []Notice
	Final     bool
	NextCheck time.Time
}

// PRSource evaluates pull request conditions with the watch owner's own
// GitHub credentials. Until one is set with Service.UsePRSource, pr targets
// and the pr_merged and ci conditions are refused as not supported yet.
type PRSource interface {
	// Validate checks that member can read target with their GitHub
	// credentials and returns its title. An error wrapping ErrTransient
	// creates the watch in the error state; any other refuses it.
	Validate(ctx context.Context, member domain.MemberID, target protocol.WatchTarget) (string, error)
	// Evaluate reads the pull request as it stands now.
	Evaluate(ctx context.Context, check PRCheck) (PRResult, error)
}
