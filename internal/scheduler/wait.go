package scheduler

import (
	"context"
	"fmt"
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
// source went unread for unreadBound. It holds no time or source status, so
// the reason built from it stays put while the run needs a member. Error is
// that of the highest-ranked condition with one.
func classifyWait(conditions []WaitCondition, now time.Time) (wait domain.RunWait, stranded string) {
	rank := map[domain.WaitEvidence]int{domain.WaitUnverified: 0, domain.WaitCondition: 1, domain.WaitSourceUnknown: 2, domain.WaitFault: 3}
	wait.Kind = domain.WaitUnverified
	var details []string
	var oldest time.Time
	neverRead, errRank := false, -1
	for _, c := range conditions {
		if c.State == waitCancelled || c.State == waitSuperseded {
			continue
		}
		kind, detail, errText := domain.WaitCondition, c.Spec, ""
		switch {
		case c.State == waitPending && fresh(c, now):
			if c.Summary != "" {
				detail += ": " + c.Summary
			}
		case c.State == waitPending && unreadable(c, now):
			kind, errText = domain.WaitFault, c.LastError
			detail = "cannot read " + c.Spec + " for over " + strings.TrimSuffix(unreadBound.String(), "0m0s") + ", so the agent will not be woken"
			stranded = strings.TrimPrefix(stranded+"; "+detail, "; ")
		case c.State == waitPending:
			kind, errText = domain.WaitSourceUnknown, c.LastError
			detail += ": status unknown"
			if c.SourceCheckedAt.IsZero() {
				detail = c.Spec + ": status never read"
			}
		case !c.DeliveredAt.IsZero():
			detail += ": " + c.State + ", woke the agent"
			if c.State != waitResolved {
				kind, errText = domain.WaitFault, c.LastError
			}
		case now.Sub(c.ResolvedAt) > wakeGrace:
			kind = domain.WaitFault
			detail += " " + ended[c.State] + ", but the agent was not woken"
			stranded = strings.TrimPrefix(stranded+"; "+detail, "; ")
			errText = c.LastDeliveryError
			if c.State != waitResolved {
				errText = strings.Trim(c.LastError+"; "+errText, "; ")
			}
		default:
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

// runWaitLocked judges the reported wait of a run that is not paused. A
// condition store that cannot be read leaves the wait unknown, never
// unverified, so it does not go stale. Caller holds s.mu.
func (s *Scheduler) runWaitLocked(ctx context.Context, run domain.RunID) (domain.RunWait, string) {
	if s.cfg.WaitConditions == nil {
		return domain.RunWait{Kind: domain.WaitUnverified}, ""
	}
	conditions, err := s.cfg.WaitConditions.RunWaits(ctx, run)
	if err != nil {
		return domain.RunWait{Kind: domain.WaitSourceUnknown, Detail: "conditions unreadable",
			Error: fmt.Sprintf("scheduler: read the run's wait conditions: %v", err)}, ""
	}
	return classifyWait(conditions, time.Now().UTC())
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

// sameWait reports two judgements that differ at most in when the source
// was last read.
func sameWait(a, b *domain.RunWait) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Kind == b.Kind && a.Detail == b.Detail && a.Error == b.Error
}
