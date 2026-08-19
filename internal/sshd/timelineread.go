package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/timeline"
)

// TimelineReader is the seam for reading persisted session history.
// Satisfied by *timeline.Reader.
type TimelineReader interface {
	// Page returns the next page of history matching f after afterSeq,
	// oldest first, at most limit entries.
	Page(ctx context.Context, f timeline.Filter, afterSeq uint64, limit int) (timeline.Page, error)
}
