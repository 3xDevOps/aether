package watch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// prSnapshots reads one pull request as it stands. Satisfied by *PRs.
type prSnapshots interface {
	Snapshot(ctx context.Context, member domain.MemberID, repo string, number int) (github.Snapshot, error)
}

// Snapshot reads repo#number with member's GitHub credentials, as
// Evaluate does.
func (p *PRs) Snapshot(ctx context.Context, member domain.MemberID, repo string, number int) (github.Snapshot, error) {
	snap, _, err := p.snapshot(ctx, member, strings.ToLower(repo), number)
	return snap, err
}

// readWaitPR reads a pr_merged or pr_checks wait. A pull request GitHub
// says is gone or that the member cannot read with their login resolves it
// in the error state; any other failure is retried.
func (s *Service) readWaitPR(ctx context.Context, w *store.RunWait, now time.Time) (waitReading, error) {
	ref := fmt.Sprintf("%s#%d", w.Repo, w.Number)
	r := waitReading{link: "https://github.com/" + w.Repo + "/pull/" + strconv.Itoa(w.Number)}
	src, ok := s.pr.(prSnapshots)
	if !ok {
		return r, fmt.Errorf("%w: this server does not read pull requests", errWaitUnreadable)
	}
	snap, err := src.Snapshot(ctx, w.MemberID, w.Repo, w.Number)
	if err != nil {
		var pe *prError
		if errors.As(err, &pe) && (!pe.transient || pe.noLogin) {
			return r, fmt.Errorf("%w: %s", errWaitUnreadable, pe.msg)
		}
		return r, err
	}
	if snap.URL != "" {
		r.link = snap.URL
	}
	checks := snap.Checks()
	cancelled := 0
	for _, run := range snap.CurrentRuns() {
		if run.Status == "completed" && run.Conclusion == "cancelled" {
			cancelled++
		}
	}
	r.observed = protocol.WaitObserved{
		PRState: snap.State, HeadSHA: snap.HeadSHA,
		Checks: &protocol.WaitChecks{Passed: checks.Passed, Failed: checks.Failed - cancelled, Pending: checks.Pending, Cancelled: cancelled},
	}
	merged := snap.MergedAt != nil
	if merged {
		r.observed.PRState = "merged"
		r.observed.MergedAt = snap.MergedAt.UTC().Format(time.RFC3339)
	}
	wait := PRPollIdle
	if checks.Pending > 0 {
		wait = PRPollBusy
	}
	r.next = now.Add(jitter(wait))
	counts := checkCounts(r.observed.Checks)
	switch w.Kind {
	case protocol.WaitKindPRMerged:
		switch {
		case merged:
			r.done, r.outcome, r.summary = true, "merged", ref+" merged"
		case snap.State == "closed":
			r.done, r.outcome, r.summary = true, "closed", ref+" closed without merging"
		default:
			r.summary = ref + " is open, " + counts
		}
	case protocol.WaitKindPRChecks:
		short := snap.HeadSHA
		if len(short) > 7 {
			short = short[:7]
		}
		on := " on head commit " + short + " (all checks)"
		switch {
		case checks.Final && checks.Failed > cancelled:
			r.done, r.outcome = true, "failed"
			r.summary = counts + on + "; failed: " + strings.Join(failedChecks(snap), ", ")
		case checks.Final && cancelled > 0:
			r.done, r.outcome, r.summary = true, "cancelled", counts+on
		case checks.Final:
			r.done, r.outcome, r.summary = true, "passed", counts+on
		case snap.State == "closed" && !merged:
			r.done, r.outcome, r.summary = true, "closed", ref+" closed without merging before its checks finished: "+counts+on
		case checks.Passed+checks.Failed+checks.Pending == 0:
			r.summary = "no checks on head commit " + short + " yet"
		default:
			r.summary = counts + on
		}
	}
	if r.done {
		r.state = protocol.WaitResolved
	}
	return r, nil
}

func checkCounts(c *protocol.WaitChecks) string {
	parts := []string{fmt.Sprintf("%d passed", c.Passed)}
	if c.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", c.Failed))
	}
	if c.Cancelled > 0 {
		parts = append(parts, fmt.Sprintf("%d cancelled", c.Cancelled))
	}
	if c.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", c.Pending))
	}
	return "checks " + strings.Join(parts, ", ")
}

// waitBody is the inbox message that wakes a run on w: what resolved, how,
// what Aether read and what to do next. It is written by the server, and
// is the same text on every attempt.
func waitBody(w *store.RunWait, link string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Aether: your wait %s %s", w.Spec, w.WaitState)
	if w.Outcome != "" && w.Outcome != w.WaitState {
		fmt.Fprintf(&b, ": %s", w.Outcome)
	}
	b.WriteString(".\n")
	if w.Summary != "" {
		fmt.Fprintf(&b, "Observed: %s.\n", w.Summary)
	}
	if link != "" {
		fmt.Fprintf(&b, "Link: %s\n", link)
	}
	fmt.Fprintf(&b, "Next: %s\n", nextStep(w))
	b.WriteString("Any other condition of this wait was cancelled. To wait again, report waiting with --until.")
	return b.String()
}

func nextStep(w *store.RunWait) string {
	switch {
	case w.WaitState == protocol.WaitExpired:
		return "nothing resolved in time; check the condition yourself, then continue or report again"
	case w.WaitState == protocol.WaitError:
		return "Aether cannot read the target; check its name and your access, or report blocked if a person must fix it"
	case w.Kind == protocol.WaitKindRunFinished && (w.Outcome == "completed" || w.Outcome == "merged" || w.Outcome == "reported-success"):
		return "that run finished; read its result and continue"
	case w.Kind == protocol.WaitKindRunFinished:
		return "that run ended " + w.Outcome + " without finishing its work; decide whether to retry, take it over or report"
	}
	switch w.Outcome {
	case "merged":
		return "the pull request merged; continue with the work that waited on it"
	case "closed":
		return "the pull request was closed without merging; decide whether to reopen it or report the change of plan"
	case "passed":
		return "the checks passed; continue"
	case "failed":
		return "read the failing checks, fix them and push, then wait on the checks again"
	}
	return "the checks were cancelled; rerun them or push again, then wait on the checks again"
}
