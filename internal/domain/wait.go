package domain

import (
	"strings"
	"time"
)

// WaitEvidence is what Aether can tell about a wait an agent reported from
// the conditions it named (aether-internal report --until).
type WaitEvidence string

const (
	// WaitCondition: every condition is pending with a fresh source read,
	// or resolved and its wake is on the way or delivered.
	WaitCondition WaitEvidence = "condition"
	// WaitSourceUnknown: a pending condition's source has not been read
	// successfully within twice its poll interval.
	WaitSourceUnknown WaitEvidence = "source_unknown"
	// WaitFault: a condition errored or expired, or resolved and its wake
	// was not delivered.
	WaitFault WaitEvidence = "system_fault"
	// WaitUnverified: the agent named no condition.
	WaitUnverified WaitEvidence = "unverified"
)

// RunWait is the evidence behind a live run's reported wait. Detail and
// Error are server text; Error is the source's or delivery's error verbatim.
// VerifiedAt is the oldest last successful source read among the
// conditions, nil when one was never read.
type RunWait struct {
	Kind       WaitEvidence `json:"kind"`
	Detail     string       `json:"detail,omitempty"`
	VerifiedAt *time.Time   `json:"verified_at,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// Text is the one line every surface shows for the wait.
func (w *RunWait) Text(now time.Time) string {
	if w == nil {
		return ""
	}
	if w.Kind == WaitUnverified {
		return "not verified: the wait names no condition Aether can check"
	}
	text := w.Detail
	if w.Error != "" {
		text += ": " + w.Error
	}
	if w.Kind == WaitCondition && w.VerifiedAt != nil {
		text += " (checked " + Ago(now.Sub(*w.VerifiedAt)) + ")"
	}
	return text
}

// Ago is d to the minute: "just now", "12m ago", "1h5m ago".
func Ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	text := strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
	if hours, ok := strings.CutSuffix(text, "h0m"); ok {
		text = hours + "h"
	}
	return text + " ago"
}
