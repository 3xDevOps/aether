package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeRunOverlap reports the conflict radar's current view for one run:
// which other active runs are touching the same files. It is a warning
// signal - no consumer may block, queue, or lock on it.
const TypeRunOverlap Type = "run.overlap"

// OverlapPeer is one other run touching files the envelope's run also
// touches.
type OverlapPeer struct {
	RunID domain.RunID `json:"run_id"`
	Files []string     `json:"files"`
}

// OverlapPayload is the envelope run's whole overlap set at the moment it
// changed. An empty With means the run's overlaps cleared - the other run
// finished, or one of them reverted the shared file.
type OverlapPayload struct {
	With []OverlapPeer `json:"with,omitempty"`
}

func (OverlapPayload) EventType() Type { return TypeRunOverlap }

// NamedRuns are the peers, so a reader who cannot fully reach one of them
// gets no part of the overlap set.
func (p OverlapPayload) NamedRuns() []domain.RunID {
	ids := make([]domain.RunID, 0, len(p.With))
	for _, peer := range p.With {
		ids = append(ids, peer.RunID)
	}
	return ids
}

func init() { registerPayload[OverlapPayload](TypeRunOverlap) }
