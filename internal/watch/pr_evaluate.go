package watch

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// unreportedAfter is how long an open pull request's head commit may go
	// without a check, or without a required one, before ci says so.
	unreportedAfter = 10 * time.Minute
	// suitesWaitMax is how long after the head commit's last check suite
	// change a suite with no pending check run keeps ci pending.
	suitesWaitMax = time.Hour
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

// evaluatePR reads snap against conditions at now. pr_merged is a run's
// merged. Nothing that happened before since is announced. Keys carry the
// pull request so a run's several pull requests never share one.
func evaluatePR(snap github.Snapshot, conditions []string, since, now time.Time) prReading {
	start := since
	if snap.HeadSince.After(start) {
		start = snap.HeadSince
	}
	active := start
	for _, suite := range snap.Suites {
		if suite.UpdatedAt.After(active) {
			active = suite.UpdatedAt
		}
	}
	age := now.Sub(start)
	checks := snap.Checks()
	if now.Sub(active) >= suitesWaitMax {
		checks = checks.WithoutSuites(false)
	}
	counts := func(c github.Checks) protocol.WatchCheckCounts {
		return protocol.WatchCheckCounts{Passed: c.Passed, Failed: c.Failed, Pending: c.Pending, Cancelled: c.Cancelled}
	}
	observed := &protocol.WatchChecks{WatchCheckCounts: counts(checks), RequiredOnly: checks.RequiredOnly, Missing: checks.Missing}
	// A required pass names the checks the base branch does not require that
	// did not pass, so it does not read as every check passing.
	var unrequired []string
	if all := checks.All; all != nil {
		required := counts(checks)
		observed.WatchCheckCounts, observed.Required = counts(*all), &required
		for _, group := range []struct {
			names []string
			state string
		}{{all.FailedNames, "failed"}, {all.CancelledNames, "cancelled"}, {all.PendingNames, "still running"}} {
			for _, n := range slices.Compact(slices.Sorted(slices.Values(group.names))) {
				unrequired = append(unrequired, n+", non-required, "+group.state)
			}
		}
	}
	ref := "#" + strconv.Itoa(snap.Number)
	key := snap.Repo + ref + ":"
	r := prReading{
		title: fmt.Sprintf("%s%s: %s", snap.Repo, ref, snap.Title),
		observed: protocol.WatchObserved{
			PRState: snap.State, HeadSHA: snap.HeadSHA, ReviewState: reviewState(snap.Reviews),
			Checks: observed,
		},
		busy: checks.Pending > 0 || snap.State == "open" && snap.ReviewRequested,
	}
	r.observed.Summary = prSummary(snap, checks, r.observed.ReviewState)
	open := snap.State == "open"
	ciFinal := checks.Pending == 0
	quiet := checks.WithoutSuites(true)
	unreported := open && age >= unreportedAfter && quiet.Pending == 0 && !quiet.Final
	r.final = !open
	notice := func(condition, k, body string) {
		r.notices = append(r.notices, Notice{Condition: condition, Key: key + k, Title: r.title, Body: body, Link: snap.URL})
	}
	for _, c := range conditions {
		switch c {
		case protocol.WatchCI:
			r.final = r.final && ciFinal
			failed := checks.Pending == 0 && checks.Failed > 0 && after(checks.DoneAt, since)
			missing := ""
			if unreported && len(checks.Missing) > 0 {
				missing = fmt.Sprintf("%s not reported on %s: %s", capitalized(checkWord(checks, len(checks.Missing))), ref, strings.Join(checks.Missing, ", "))
			}
			switch {
			case failed:
				// One notice: a second under the same push topic would replace
				// the failure on a device that has yet to receive it.
				body := fmt.Sprintf("%d %s failed on %s: %s", checks.Failed, checkWord(checks, checks.Failed), ref, names(checks.FailedNames))
				k := checks.Key + ":failed"
				if missing != "" {
					body, k = body+". "+missing, k+":missing"
				}
				notice(c, k, body)
			case missing != "":
				notice(c, checks.Key+":missing", missing)
			case unreported:
				notice(c, checks.Key+":none", fmt.Sprintf("No checks reported on %s in %d minutes", ref, int(unreportedAfter/time.Minute)))
			case !checks.Final || !after(checks.DoneAt, since):
			case checks.Cancelled > 0:
				notice(c, checks.Key+":cancelled", fmt.Sprintf("%d %s cancelled on %s: %s", checks.Cancelled, checkWord(checks, checks.Cancelled), ref, names(checks.CancelledNames)))
			default:
				body := capitalized(checkWord(checks, 2)) + " passed on " + ref
				if len(unrequired) > 0 {
					body += " (" + strings.Join(unrequired, "; ") + ")"
				}
				notice(c, checks.Key+":passed", body)
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
			if snap.State == "closed" && snap.ClosedAt != nil && after(*snap.ClosedAt, since) {
				notice(c, "closed", "PR "+ref+" closed without merging on GitHub")
			}
		}
	}
	return r
}

// after compares at GitHub's one-second resolution.
func after(t, since time.Time) bool { return !t.Before(since.Truncate(time.Second)) }

func names(list []string) string {
	sorted := slices.Sorted(slices.Values(list))
	return strings.Join(slices.Compact(sorted), ", ")
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
	parts := []string{capitalized(snap.State)}
	kind := checkWord(checks, 2)
	switch {
	case checks.Pending > 0:
		parts = append(parts, fmt.Sprintf("%d of %d %s pending", checks.Pending, checks.Passed+checks.Failed+checks.Cancelled+checks.Pending, kind))
	case len(checks.Missing) > 0:
		parts = append(parts, "waiting for required "+strings.Join(checks.Missing, ", ")+" to be reported")
	case checks.Passed+checks.Failed+checks.Cancelled == 0:
		parts = append(parts, "no checks reported on the head commit")
	case checks.Failed > 0:
		parts = append(parts, fmt.Sprintf("%d %s failed", checks.Failed, checkWord(checks, checks.Failed)))
	case checks.Cancelled > 0:
		parts = append(parts, fmt.Sprintf("%d %s cancelled", checks.Cancelled, checkWord(checks, checks.Cancelled)))
	default:
		parts = append(parts, kind+" passed")
	}
	if review != "" {
		parts = append(parts, strings.ReplaceAll(review, "_", " "))
	}
	if snap.WorkflowsUnreadable != "" {
		parts = append(parts, "can't read Actions runs, so a cancelled check still counts ("+snap.WorkflowsUnreadable+")")
	}
	switch {
	case snap.RequiredUnreadable == "":
	case checks.RequiredOnly:
		parts = append(parts, "can't read all of what "+snap.BaseRef+" requires, so its last known required checks count ("+snap.RequiredUnreadable+")")
	default:
		parts = append(parts, "can't read which checks "+snap.BaseRef+" requires, so every check counts ("+snap.RequiredUnreadable+")")
	}
	return strings.Join(parts, " · ")
}

// checkWord is "check" or "checks" for n of c's counted checks, with
// "required" when only required checks count.
func checkWord(c github.Checks, n int) string {
	word := "checks"
	if n == 1 {
		word = "check"
	}
	if c.RequiredOnly {
		word = "required " + word
	}
	return word
}

func capitalized(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// changed reports whether a pull request moved since the last evaluation.
func changed(prev, now protocol.WatchObserved) bool {
	return prev.PRState != now.PRState || prev.HeadSHA != now.HeadSHA || prev.ReviewState != now.ReviewState ||
		!reflect.DeepEqual(prev.Checks, now.Checks)
}

// jitter spreads polls by up to a tenth either way, so watches created
// together do not read GitHub together.
func jitter(d time.Duration) time.Duration {
	return d - d/10 + rand.N(d/5+1)
}
