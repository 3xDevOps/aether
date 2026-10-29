package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// WaitCondition is one durable condition a run's waiting report named, as
// the scheduler reads it.
type WaitCondition struct {
	// Spec is the condition as the agent wrote it, such as
	// pr:owner/repo#12:checks.
	Spec      string
	CreatedAt time.Time
	// State is pending, resolved, cancelled, superseded, expired or error.
	State string
	// Summary is the source's last observed status.
	Summary string
	// LastCheckedAt is the last read attempt, SourceCheckedAt the last
	// successful one, NextCheckAt the next attempt.
	LastCheckedAt, SourceCheckedAt, NextCheckAt time.Time
	ConsecutiveFailures                         int
	LastError                                   string
	// ResolvedAt is when the condition left pending, including by expiring
	// or erroring.
	ResolvedAt        time.Time
	DeliveredAt       time.Time
	LastDeliveryError string
}

// WaitConditions reads a run's live conditions and its latest resolved one.
type WaitConditions interface {
	RunWaits(ctx context.Context, run domain.RunID) ([]WaitCondition, error)
}

const (
	waitPending    = "pending"
	waitResolved   = "resolved"
	waitCancelled  = "cancelled"
	waitSuperseded = "superseded"
)

// unreadBound is how long a pending condition's source may go unread
// before the run needs a member.
const unreadBound = time.Hour

// wakeGrace is how long a resolved condition's wake may stay undelivered
// before the run needs a member: past it the agent will not move on its own.
const wakeGrace = 5 * time.Minute

// waitBrokenReasonPrefix starts the reason of a run whose wait resolved and
// whose wake was not delivered within wakeGrace.
const waitBrokenReasonPrefix = "wait broken: "

// classifyWait judges a reported wait by its conditions at now. stranded
// names the conditions the agent will not be woken by: one that left
// pending and whose wake was not delivered within wakeGrace, or one whose
// source went unread for unreadBound, sorted so the store's order does not
// matter. It is empty while another condition can still wake the agent:
// one still pending and read, or one whose wake is on its way. It holds no
// time or source status, so the reason built from it stays put while the
// run needs a member. A condition that left pending before turn started
// backs no wait reported since: its wake, or the message a member sent in
// its place, started a later turn. Nor does one whose wake was delivered:
// the agent has it, possibly in its own report, so the run waits on the
// agent and no turn may ever start past it. Error is that of the
// highest-ranked condition with one.
func classifyWait(conditions []WaitCondition, turn, now time.Time) (wait domain.RunWait, stranded string) {
	rank := map[domain.WaitEvidence]int{domain.WaitUnverified: 0, domain.WaitCondition: 1, domain.WaitSourceUnknown: 2, domain.WaitFault: 3}
	wait.Kind = domain.WaitUnverified
	var details, strandedBy []string
	var oldest time.Time
	neverRead, errRank, live := false, -1, false
	for _, c := range conditions {
		if c.State == waitCancelled || c.State == waitSuperseded || (c.State != waitPending && (c.ResolvedAt.Before(turn) || !c.DeliveredAt.IsZero())) {
			continue
		}
		kind, detail, errText := domain.WaitCondition, c.Spec, ""
		switch {
		case c.State == waitPending && fresh(c, now):
			live = true
			if c.Summary != "" {
				detail += ": " + c.Summary
			}
		case c.State == waitPending && unreadable(c, now):
			kind, errText = domain.WaitFault, c.LastError
			detail = "cannot read " + c.Spec + " for over " + strings.TrimSuffix(unreadBound.String(), "0m0s") + ", so the agent will not be woken"
			strandedBy = append(strandedBy, detail)
		case c.State == waitPending:
			live = true
			kind, errText = domain.WaitSourceUnknown, c.LastError
			detail += ": status unknown"
			if c.SourceCheckedAt.IsZero() {
				detail = c.Spec + ": status never read"
			}
		case now.Sub(c.ResolvedAt) > wakeGrace:
			kind = domain.WaitFault
			detail += " " + ended[c.State] + ", but the agent was not woken"
			strandedBy = append(strandedBy, detail)
			errText = c.LastDeliveryError
			if c.State != waitResolved {
				errText = strings.Trim(c.LastError+"; "+errText, "; ")
			}
		default:
			live = true
			detail += ": " + c.State + ", waking the agent"
			if c.State != waitResolved {
				kind, errText = domain.WaitFault, c.LastError
			}
		}
		if rank[kind] > rank[wait.Kind] {
			wait.Kind = kind
		}
		if c.Summary != "" && c.State != waitPending {
			detail += " (" + c.Summary + ")"
		}
		details = append(details, detail)
		if errText != "" && rank[kind] > errRank {
			wait.Error, errRank = errText, rank[kind]
		}
		switch {
		case c.SourceCheckedAt.IsZero():
			neverRead = true
		case oldest.IsZero() || c.SourceCheckedAt.Before(oldest):
			oldest = c.SourceCheckedAt
		}
	}
	wait.Detail = strings.Join(details, "; ")
	if !live {
		slices.Sort(strandedBy)
		stranded = strings.Join(strandedBy, "; ")
	}
	if !neverRead && !oldest.IsZero() {
		wait.VerifiedAt = &oldest
	}
	return wait, stranded
}

var ended = map[string]string{waitResolved: "resolved", "expired": "expired", "error": "errored"}

// unreadable reports a pending condition whose source has not been read
// for unreadBound since its last successful read or, never read, since it
// was registered: the engine keeps retrying, but the wake it waits for
// cannot be trusted to come.
func unreadable(c WaitCondition, now time.Time) bool {
	since := c.SourceCheckedAt
	if since.IsZero() {
		since = c.CreatedAt
	}
	return !since.IsZero() && now.Sub(since) > unreadBound
}

// fresh reports a pending condition whose source was read without error
// within twice its poll interval.
func fresh(c WaitCondition, now time.Time) bool {
	interval := c.NextCheckAt.Sub(c.LastCheckedAt)
	return c.ConsecutiveFailures == 0 && c.LastError == "" && !c.SourceCheckedAt.IsZero() &&
		interval > 0 && now.Sub(c.SourceCheckedAt) <= 2*interval
}

// conditionsUnread strands a wait whose condition store failed every read
// for unreadBound since the wait was reported.
var conditionsUnread = "cannot read this run's wait conditions for over " + strings.TrimSuffix(unreadBound.String(), "0m0s") + ", so the agent will not be woken"

// runWaitLocked judges the reported wait of a run that is not paused. A
// condition store that cannot be read leaves the wait unknown, never
// unverified, so it does not go stale. Once its reads have failed for
// unreadBound since the later of the first failure and the wait's report,
// the wait is stranded like an unread source. Caller holds s.mu.
func (s *Scheduler) runWaitLocked(ctx context.Context, entry *supervised) (domain.RunWait, string) {
	if s.cfg.WaitConditions == nil {
		return domain.RunWait{Kind: domain.WaitUnverified}, ""
	}
	now := time.Now().UTC()
	conditions, err := s.cfg.WaitConditions.RunWaits(ctx, entry.runID)
	if err == nil {
		entry.waitsFailingSince = time.Time{}
	} else {
		if entry.waitsFailingSince.IsZero() {
			entry.waitsFailingSince = now
		}
		wait := domain.RunWait{Kind: domain.WaitSourceUnknown, Detail: "conditions unreadable",
			Error: fmt.Sprintf("scheduler: read the run's wait conditions: %v", err)}
		since := entry.waitsFailingSince
		if entry.idleReportAt.After(since) {
			since = entry.idleReportAt
		}
		if now.Sub(since) > unreadBound {
			wait.Kind, wait.Detail = domain.WaitFault, conditionsUnread
			return wait, conditionsUnread
		}
		return wait, ""
	}
	return classifyWait(conditions, entry.turnStartedAt, now)
}

// Wait is the evidence behind run's reported wait while it shows activity,
// nil when it shows none.
func (s *Scheduler) Wait(run domain.RunID, activity domain.RunActivity) *domain.RunWait {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[run]; entry != nil && !entry.paused && entry.activity == activity && entry.wait != nil {
		wait := *entry.wait
		return &wait
	}
	return nil
}

// WaitChanged re-derives run's activity after one of its wait conditions
// changed state or delivery.
func (s *Scheduler) WaitChanged(ctx context.Context, run domain.RunID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return nil
	}
	return s.refreshActivityLocked(ctx, entry)
}

// verifiedStep is how far a wait's last source read must move before the
// run's status is published again to show it.
const verifiedStep = time.Minute

// readSince reports a judgement whose source was read at least
// verifiedStep after the one shown.
func readSince(next, shown *domain.RunWait) bool {
	if next == nil || next.VerifiedAt == nil || shown == nil {
		return false
	}
	return shown.VerifiedAt == nil || next.VerifiedAt.Sub(*shown.VerifiedAt) >= verifiedStep
}

// sameWait reports two judgements that differ at most in when the source
// was last read.
func sameWait(a, b *domain.RunWait) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Kind == b.Kind && a.Detail == b.Detail && a.Error == b.Error
}
