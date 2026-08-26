package events

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
)

var (
	// ErrBusClosed is returned by operations on a closed bus.
	ErrBusClosed = errors.New("events: bus closed")
	// ErrNoLog is returned when replay is requested on a bus without an
	// EventLog.
	ErrNoLog = errors.New("events: replay requested but bus has no event log")
	// ErrNoPayload is returned when publishing an event without a payload.
	ErrNoPayload = errors.New("events: event has no payload")
	// ErrNoWorkspace is returned when publishing an event without a
	// workspace scope. Every event is workspace-scoped: this keeps the
	// persisted log complete, so sequence cursors are durable and replay
	// can return everything live subscribers saw.
	ErrNoWorkspace = errors.New("events: event has no workspace scope")
)

// Filter selects a subset of the event stream. The zero value matches
// every event; each set field narrows the match.
type Filter struct {
	// Workspace matches only events scoped to this workspace.
	Workspace domain.WorkspaceID
	// Run matches only events carrying this run ID.
	Run domain.RunID
	// Types matches only events whose Type is listed. Empty means all.
	Types []Type
}

// Matches reports whether e passes the filter.
func (f Filter) Matches(e Event) bool {
	if f.Workspace != "" && e.WorkspaceID != f.Workspace {
		return false
	}
	if f.Run != "" && e.RunID != f.Run {
		return false
	}
	if len(f.Types) == 0 {
		return true
	}
	for _, t := range f.Types {
		if e.Type == t {
			return true
		}
	}
	return false
}

// SubscribeOptions configures a subscription.
type SubscribeOptions struct {
	// Filter narrows which events the subscription receives.
	Filter Filter
	// Replay, when true, first delivers persisted events with
	// Seq > AfterSeq (matching Filter) from the event log, then continues
	// seamlessly with live events - no gaps, no duplicates. Requires the
	// bus to have an EventLog.
	Replay bool
	// AfterSeq is the replay cursor: the last sequence number the
	// subscriber has already seen. Zero replays from the beginning.
	AfterSeq uint64
	// Buffer is the per-subscriber buffer capacity in events. Zero means
	// DefaultBuffer. When the buffer is full the oldest buffered event is
	// dropped (see Subscription.Dropped); publishers are never blocked.
	Buffer int
}

// DefaultBuffer is the per-subscriber buffer capacity used when
// SubscribeOptions.Buffer is zero.
const DefaultBuffer = 256

// Subscription is one consumer's view of the event stream.
type Subscription interface {
	// Events is the ordered stream of matching events. The channel is
	// closed when the subscription or the bus is closed.
	Events() <-chan Event
	// Dropped returns how many events were discarded because this
	// subscriber consumed too slowly for its buffer. A subscriber that
	// observes a nonzero increase can re-replay from its last seen cursor
	// to recover the gap.
	Dropped() uint64
	// Err reports why the subscription terminated. It is meaningful once
	// Events is closed: nil means a clean shutdown (Close on the
	// subscription or the bus); non-nil means the subscription failed -
	// e.g. a replay read error or the Subscribe context being cancelled
	// mid-replay - and the consumer should re-subscribe from its last
	// seen cursor rather than treat the stream as complete.
	Err() error
	// Close cancels the subscription and closes the Events channel.
	Close() error
}

// Bus is the pub/sub seam. The in-process implementation is the only one
// today; the interface exists so the bus could be externalized later
// without touching consumers.
type Bus interface {
	// Publish assigns the event its sequence cursor, ID, and timestamp
	// (where unset), persists it, and fans it out to matching
	// subscribers. Events must be workspace-scoped (ErrNoWorkspace
	// otherwise), so with an EventLog attached every published event is
	// persisted and sequence cursors survive restarts. Publish never
	// blocks on slow subscribers; concurrent publishes serialize on the
	// log write to keep the sequence gapless and ordered. The returned
	// event carries the assigned fields.
	Publish(ctx context.Context, e Event) (Event, error)
	// Subscribe registers a new subscriber. ctx bounds only the replay
	// phase (log reads); the subscription itself lives until Close.
	Subscribe(ctx context.Context, opts SubscribeOptions) (Subscription, error)
	// Close shuts the bus down, closing every subscription.
	Close() error
}

// EventLog is append-only persistence for workspace-scoped events; it
// backs replay and, later, the workspace timeline. Implementations must
// return events ordered by Seq ascending.
type EventLog interface {
	// Append durably stores e. Seq must already be assigned and unique.
	Append(ctx context.Context, e Event) error
	// Read returns up to limit stored events matching f with
	// afterSeq < Seq <= uptoSeq, ordered by Seq ascending. uptoSeq zero
	// means no upper bound.
	Read(ctx context.Context, f Filter, afterSeq, uptoSeq uint64, limit int) ([]Event, error)
	// LastSeq returns the highest stored sequence number, zero when empty.
	LastSeq(ctx context.Context) (uint64, error)
	// Close releases the log's resources.
	Close() error
}
