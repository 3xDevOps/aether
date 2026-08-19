package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/overlap"
)

// OverlapIndex is the seam for the cross-run file overlap index
// (*overlap.Index): the conflict radar's read side.
type OverlapIndex interface {
	// Overlaps returns the current overlap set for every active run that
	// shares a file with another.
	Overlaps(ctx context.Context) ([]overlap.Entry, error)
}
