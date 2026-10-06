package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/timeline"
)

// TimelineReader is the seam for reading persisted workspace history.
// Satisfied by *timeline.Reader.
type TimelineReader interface {
	// Page returns the next page of history matching f after afterSeq,
	// oldest first, at most limit entries.
	Page(ctx context.Context, f timeline.Filter, afterSeq uint64, limit int) (timeline.Page, error)
	// Before returns the newest page of history matching f before
	// beforeSeq, zero meaning the log head.
	Before(ctx context.Context, f timeline.Filter, beforeSeq uint64, limit int) (timeline.Page, error)
}
