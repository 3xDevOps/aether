package protocol

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MethodRunWaitCancel ends one of a run's wait conditions. It needs the
// run control permission, as run.pause does.
const MethodRunWaitCancel = "run.wait.cancel"

// CoordMaxUntil bounds the conditions one waiting report registers.
const CoordMaxUntil = 4

// Kinds of a wait condition.
const (
	WaitKindPRMerged    = "pr_merged"
	WaitKindPRChecks    = "pr_checks"
	WaitKindRunFinished = "run_finished"
)

// States of a wait condition. Resolved, expired and error wake the run.
const (
	WaitPending    = "pending"
	WaitResolved   = "resolved"
	WaitCancelled  = "cancelled"
	WaitSuperseded = "superseded"
	WaitExpired    = "expired"
	WaitError      = "error"
)

// WatchSourceAgent is the source of a watch a run registered with
// coord.report until; WatchChannelRunInbox is its delivery to that run's
// inbox.
const (
	WatchSourceAgent     = "agent"
	WatchChannelRunInbox = "run_inbox"
)

// WaitGrammar is the accepted form of an until condition.
const WaitGrammar = "pr:<owner>/<repo>#<n>:merged, pr:<owner>/<repo>#<n>:checks or run:<run-id>:finished"

// WaitSpec is one parsed until condition. Spec is its canonical text.
type WaitSpec struct {
	Spec   string
	Kind   string
	Repo   string
	Number int
	RunID  string
}

var (
	waitPRPattern  = regexp.MustCompile(`^pr:([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)#([1-9][0-9]{0,9}):(merged|checks)$`)
	waitRunPattern = regexp.MustCompile(`^run:([a-z0-9][a-z0-9-]{0,63}):finished$`)
)

// ParseWaitSpec parses one until condition. The error names the condition
// and the grammar.
func ParseWaitSpec(raw string) (WaitSpec, error) {
	if m := waitPRPattern.FindStringSubmatch(raw); m != nil {
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return WaitSpec{}, fmt.Errorf("until %q: pull request number: %w", raw, err)
		}
		repo := strings.ToLower(m[1] + "/" + m[2])
		kind := WaitKindPRMerged
		if m[4] == "checks" {
			kind = WaitKindPRChecks
		}
		return WaitSpec{Spec: fmt.Sprintf("pr:%s#%d:%s", repo, n, m[4]), Kind: kind, Repo: repo, Number: n}, nil
	}
	if m := waitRunPattern.FindStringSubmatch(raw); m != nil {
		return WaitSpec{Spec: raw, Kind: WaitKindRunFinished, RunID: m[1]}, nil
	}
	return WaitSpec{}, fmt.Errorf("until %q is not a condition; expected %s", raw, WaitGrammar)
}

type WaitChecks struct {
	Passed    int `json:"passed"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
	Cancelled int `json:"cancelled"`
	// RequiredOnly is set when only the repository's required checks are
	// counted; otherwise every check on the head commit is.
	RequiredOnly bool `json:"required_only"`
}

// WaitObserved is what the last read of a wait's target found.
type WaitObserved struct {
	PRState   string      `json:"pr_state,omitempty"`
	HeadSHA   string      `json:"head_sha,omitempty"`
	MergedAt  string      `json:"merged_at,omitempty"`
	Checks    *WaitChecks `json:"checks,omitempty"`
	RunStatus string      `json:"run_status,omitempty"`
}

// WaitCondition is one condition a run registered with coord.report until.
// Revision groups the conditions of one report: the first to resolve wakes
// the run once and ends the others.
type WaitCondition struct {
	ID                  string       `json:"id"`
	Revision            int64        `json:"revision"`
	Spec                string       `json:"spec"`
	Kind                string       `json:"kind"`
	Repo                string       `json:"repo,omitempty"`
	Number              int          `json:"number,omitempty"`
	RunID               string       `json:"run_id,omitempty"`
	State               string       `json:"state"`
	Outcome             string       `json:"outcome,omitempty"`
	Observed            WaitObserved `json:"observed"`
	Summary             string       `json:"summary"`
	CreatedAt           string       `json:"created_at"`
	LastCheckedAt       string       `json:"last_checked_at,omitempty"`
	SourceCheckedAt     string       `json:"source_checked_at,omitempty"`
	NextCheckAt         string       `json:"next_check_at,omitempty"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	LastError           string       `json:"last_error,omitempty"`
	LastErrorAt         string       `json:"last_error_at,omitempty"`
	ResolvedAt          string       `json:"resolved_at,omitempty"`
	DeliveredAt         string       `json:"delivered_at,omitempty"`
	DeliveryAttempts    int          `json:"delivery_attempts"`
	LastDeliveryError   string       `json:"last_delivery_error,omitempty"`
	ExpiresAt           string       `json:"expires_at"`
	EndedCause          string       `json:"ended_cause,omitempty"`
}

type RunWaitCancelParams struct {
	RunID  string `json:"run_id"`
	WaitID string `json:"wait_id"`
}

type RunWaitCancelResult struct {
	Wait WaitCondition `json:"wait"`
}
