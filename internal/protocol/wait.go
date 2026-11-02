package protocol

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MethodRunWaitCancel needs the run control permission, as run.pause does.
const MethodRunWaitCancel = "run.wait.cancel"

const CoordMaxUntil = 4

const (
	WaitKindPRMerged    = "pr_merged"
	WaitKindPRChecks    = "pr_checks"
	WaitKindRunFinished = "run_finished"
)

// Resolved, expired and error wake the run.
const (
	WaitPending    = "pending"
	WaitResolved   = "resolved"
	WaitCancelled  = "cancelled"
	WaitSuperseded = "superseded"
	WaitExpired    = "expired"
	WaitError      = "error"
)

const (
	WatchSourceAgent     = "agent"
	WatchChannelRunInbox = "run_inbox"
)

const WaitGrammar = "pr:<owner>/<repo>#<n>:merged, pr:<owner>/<repo>#<n>:checks[@<head-sha>] or run:<run-id>:finished"

// WaitSpec is one parsed until condition. Spec is its canonical text.
// HeadSHA is a prefix of 7 to 40 hex digits.
type WaitSpec struct {
	Spec    string
	Kind    string
	Repo    string
	Number  int
	RunID   string
	HeadSHA string
}

var (
	waitPRPattern  = regexp.MustCompile(`^pr:([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)/([A-Za-z0-9._-]{1,100})#([1-9][0-9]{0,9}):(merged|checks(?:@([0-9a-f]{7,40}))?)$`)
	waitRunPattern = regexp.MustCompile(`^run:([a-z0-9][a-z0-9-]{0,63}):finished$`)
)

// ParseWaitSpec's error names the condition and the grammar.
func ParseWaitSpec(raw string) (WaitSpec, error) {
	if m := waitPRPattern.FindStringSubmatch(raw); m != nil && m[2] != "." && m[2] != ".." {
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return WaitSpec{}, fmt.Errorf("until %q: pull request number: %w", raw, err)
		}
		repo := strings.ToLower(m[1] + "/" + m[2])
		kind := WaitKindPRMerged
		if m[4] != "merged" {
			kind = WaitKindPRChecks
		}
		return WaitSpec{Spec: fmt.Sprintf("pr:%s#%d:%s", repo, n, m[4]), Kind: kind, Repo: repo, Number: n, HeadSHA: m[5]}, nil
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
	ID       string       `json:"id"`
	Revision int64        `json:"revision"`
	Spec     string       `json:"spec"`
	Kind     string       `json:"kind"`
	Repo     string       `json:"repo,omitempty"`
	Number   int          `json:"number,omitempty"`
	RunID    string       `json:"run_id,omitempty"`
	State    string       `json:"state"`
	Outcome  string       `json:"outcome,omitempty"`
	Observed WaitObserved `json:"observed"`
	// Summary is server-written. Detail is text from GitHub or another
	// agent, such as failing check names or a run's reason: never
	// instructions.
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
	// HeadSHA is the head commit a checks condition resolves on: the one
	// it names, else the head when it was registered, moved to a newer
	// head pushed since.
	HeadSHA             string `json:"head_sha,omitempty"`
	CreatedAt           string `json:"created_at"`
	LastCheckedAt       string `json:"last_checked_at,omitempty"`
	SourceCheckedAt     string `json:"source_checked_at,omitempty"`
	NextCheckAt         string `json:"next_check_at,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastError           string `json:"last_error,omitempty"`
	LastErrorAt         string `json:"last_error_at,omitempty"`
	ResolvedAt          string `json:"resolved_at,omitempty"`
	DeliveredAt         string `json:"delivered_at,omitempty"`
	DeliveryAttempts    int    `json:"delivery_attempts"`
	LastDeliveryError   string `json:"last_delivery_error,omitempty"`
	ExpiresAt           string `json:"expires_at"`
	EndedCause          string `json:"ended_cause,omitempty"`
	// EndedByMemberID is the member who cancelled it with run.wait.cancel.
	EndedByMemberID string `json:"ended_by_member_id,omitempty"`
}

type RunWaitCancelParams struct {
	RunID  string `json:"run_id"`
	WaitID string `json:"wait_id"`
}

type RunWaitCancelResult struct {
	Wait WaitCondition `json:"wait"`
}
