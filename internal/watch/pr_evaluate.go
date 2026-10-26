package watch

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// prReading is what one pull request's snapshot means for a watch's
// conditions. busy asks for the short poll: checks pending or a review
// requested.
type prReading struct {
	title    string
	observed protocol.WatchObserved
	notices  []Notice
	final    bool
	busy     bool
}

// evaluatePR reads snap against conditions. pr_merged is a run's merged.
// Nothing that happened before since is announced; closed needs no time
// because watch.create refuses a pull request that is not open. Keys carry
// the pull request so a run's several pull requests never share one.
func evaluatePR(snap github.Snapshot, conditions []string, since time.Time) prReading {
	checks := snap.Checks()
	ref := "#" + strconv.Itoa(snap.Number)
	key := snap.Repo + ref + ":"
	r := prReading{
		title: fmt.Sprintf("%s%s: %s", snap.Repo, ref, snap.Title),
		observed: protocol.WatchObserved{
			PRState: snap.State, HeadSHA: snap.HeadSHA, ReviewState: reviewState(snap.Reviews),
			Checks: &protocol.WatchChecks{Passed: checks.Passed, Failed: checks.Failed, Pending: checks.Pending},
		},
		busy: checks.Pending > 0 || snap.State == "open" && snap.ReviewRequested,
	}
	r.observed.Summary = prSummary(snap, checks, r.observed.ReviewState)
	open := snap.State == "open"
	ciFinal := checks.Final || checks.Passed+checks.Failed+checks.Pending == 0
	r.final = !open
	notice := func(condition, k, body string) {
		r.notices = append(r.notices, Notice{Condition: condition, Key: key + k, Title: r.title, Body: body, Link: snap.URL})
	}
	for _, c := range conditions {
		switch c {
		case protocol.WatchCI:
			r.final = r.final && ciFinal
			if !checks.Final || !after(checksDone(snap), since) {
				continue
			}
			if failed := failedChecks(snap); len(failed) > 0 {
				notice(c, checks.Key+":failed", fmt.Sprintf("%d %s failed on %s: %s", len(failed), plural(len(failed), "check", "checks"), ref, strings.Join(failed, ", ")))
			} else {
				notice(c, checks.Key+":passed", "Checks passed on "+ref)
			}
		case protocol.WatchReview:
			for _, review := range snap.Reviews {
				if body := reviewLine(review, ref); body != "" && review.SubmittedAt != nil && after(*review.SubmittedAt, since) {
					notice(c, "review:"+strconv.FormatInt(review.ID, 10), body)
				}
			}
		case protocol.WatchMerged, protocol.WatchPRMerged:
			if snap.MergedAt != nil && after(*snap.MergedAt, since) {
				notice(c, "merged", "PR "+ref+" merged on GitHub")
			}
		case protocol.WatchClosed:
			if snap.State == "closed" {
				notice(c, "closed", "PR "+ref+" closed without merging on GitHub")
			}
		}
	}
	return r
}

// after compares at GitHub's one-second resolution.
func after(t, since time.Time) bool { return !t.Before(since.Truncate(time.Second)) }

// checksDone is when the last check on the head commit finished.
func checksDone(snap github.Snapshot) time.Time {
	var last time.Time
	for _, run := range snap.CurrentRuns() {
		if run.CompletedAt != nil && run.CompletedAt.After(last) {
			last = *run.CompletedAt
		}
	}
	for _, status := range snap.Statuses {
		if status.UpdatedAt.After(last) {
			last = status.UpdatedAt
		}
	}
	return last
}

func failedChecks(snap github.Snapshot) []string {
	var names []string
	for _, run := range snap.CurrentRuns() {
		if run.Status == "completed" && run.Conclusion != "success" && run.Conclusion != "neutral" && run.Conclusion != "skipped" {
			names = append(names, run.Name)
		}
	}
	for _, status := range snap.Statuses {
		if status.State != "success" && status.State != "pending" {
			names = append(names, status.Context)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func reviewLine(r github.Review, ref string) string {
	who := r.User
	if who == "" {
		who = "A deleted account"
	}
	switch r.State {
	case "APPROVED":
		return who + " approved " + ref
	case "CHANGES_REQUESTED":
		return who + " requested changes on " + ref
	case "COMMENTED":
		if strings.TrimSpace(r.Body) != "" {
			return who + " commented on " + ref
		}
	}
	return ""
}

// reviewState is the pull request's review decision from each reviewer's
// latest approving or change-requesting review: changes_requested wins,
// then approved, then commented when any review exists.
func reviewState(reviews []github.Review) string {
	latest := map[string]string{}
	commented := false
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED":
			latest[r.User] = r.State
		case "DISMISSED":
			delete(latest, r.User)
		case "COMMENTED":
			commented = true
		}
	}
	states := slices.Collect(maps.Values(latest))
	switch {
	case slices.Contains(states, "CHANGES_REQUESTED"):
		return "changes_requested"
	case slices.Contains(states, "APPROVED"):
		return "approved"
	case commented:
		return "commented"
	}
	return ""
}

func prSummary(snap github.Snapshot, checks github.Checks, review string) string {
	parts := []string{strings.ToUpper(snap.State[:1]) + snap.State[1:]}
	switch {
	case checks.Passed+checks.Failed+checks.Pending == 0:
		parts = append(parts, "no checks on the head commit, so ci will not notify")
	case checks.Pending > 0:
		parts = append(parts, fmt.Sprintf("%d of %d checks pending", checks.Pending, checks.Passed+checks.Failed+checks.Pending))
	case checks.Failed > 0:
		parts = append(parts, fmt.Sprintf("%d %s failed", checks.Failed, plural(checks.Failed, "check", "checks")))
	default:
		parts = append(parts, "checks passed")
	}
	if review != "" {
		parts = append(parts, strings.ReplaceAll(review, "_", " "))
	}
	if snap.WorkflowsUnreadable != "" {
		parts = append(parts, "can't read Actions runs, so a cancelled check counts as failed ("+snap.WorkflowsUnreadable+")")
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// changed reports whether a pull request moved since the last evaluation.
func changed(prev, now protocol.WatchObserved) bool {
	return prev.PRState != now.PRState || prev.HeadSHA != now.HeadSHA || prev.ReviewState != now.ReviewState ||
		(prev.Checks == nil) != (now.Checks == nil) || prev.Checks != nil && *prev.Checks != *now.Checks
}

// jitter spreads polls by up to a tenth either way, so watches created
// together do not read GitHub together.
func jitter(d time.Duration) time.Duration {
	return d - d/10 + rand.N(d/5+1)
}
