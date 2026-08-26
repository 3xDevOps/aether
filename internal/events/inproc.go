package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// replayBatch is how many events a replaying subscription reads from the
// log per query.
const replayBatch = 512

// InProc is the in-process Bus. Fan-out is per-subscriber buffered queues
// with a drop-oldest slow-consumer policy: Publish never blocks, a laggard
// loses its oldest buffered events, and the loss is observable via
// Subscription.Dropped so the consumer can re-replay from its cursor.
type InProc struct {
	log EventLog

	mu     sync.Mutex
	seq    uint64
	subs   map[*inprocSub]struct{}
	closed bool
}

// NewInProc builds an in-process bus. log may be nil, in which case
// nothing is persisted and replay is unavailable. With a log, sequence
// numbering continues after the highest persisted cursor.
func NewInProc(ctx context.Context, log EventLog) (*InProc, error) {
	b := &InProc{log: log, subs: make(map[*inprocSub]struct{})}
	if log != nil {
		last, err := log.LastSeq(ctx)
		if err != nil {
			return nil, fmt.Errorf("events: read last sequence: %w", err)
		}
		b.seq = last
	}
	return b, nil
}

// Publish implements Bus. Events are persisted to the log before fan-out;
// a persistence failure fails the publish so replay can never miss an
// event that live subscribers saw. Sequence assignment, the log write, and
// fan-out happen under one lock, so concurrent publishes (and Subscribe)
// serialize on the disk write - a deliberate trade for a gapless, ordered
// cursor.
func (b *InProc) Publish(ctx context.Context, e Event) (Event, error) {
	if e.Payload == nil {
		return Event{}, ErrNoPayload
	}
	if e.WorkspaceID == "" {
		return Event{}, ErrNoWorkspace
	}
	if e.Type == "" {
		e.Type = e.Payload.EventType()
	} else if e.Type != e.Payload.EventType() {
		return Event{}, fmt.Errorf("events: type %q does not match payload type %q", e.Type, e.Payload.EventType())
	}
	if e.ID == "" {
		e.ID = newEventID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Event{}, ErrBusClosed
	}
	e.Seq = b.seq + 1
	if b.log != nil {
		if err := b.log.Append(ctx, e); err != nil {
			// The driver can report an error (e.g. a context
			// cancellation) after the row actually committed. Re-sync
			// the cursor from the log so the next publish never reuses
			// a persisted seq, which would fail the primary key
			// constraint on every subsequent publish.
			syncCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if last, lastErr := b.log.LastSeq(syncCtx); lastErr == nil && last > b.seq {
				b.seq = last
			}
			cancel()
			return Event{}, fmt.Errorf("events: persist event: %w", err)
		}
	}
	b.seq = e.Seq
	for s := range b.subs {
		if s.filter.Matches(e) {
			s.enqueue(e)
		}
	}
	return e, nil
}

// Subscribe implements Bus. The subscriber is registered (and starts
// buffering live events) before any replay reads happen; replay is bounded
// at the registration cursor, so the handoff from replayed to live events
// has no gaps and no duplicates.
func (b *InProc) Subscribe(ctx context.Context, opts SubscribeOptions) (Subscription, error) {
	if opts.Replay && b.log == nil {
		return nil, ErrNoLog
	}
	buf := opts.Buffer
	if buf <= 0 {
		buf = DefaultBuffer
	}
	s := &inprocSub{
		bus:    b,
		filter: opts.Filter,
		max:    buf,
		out:    make(chan Event),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrBusClosed
	}
	joinSeq := b.seq
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	go s.deliver(ctx, opts, joinSeq)
	return s, nil
}

// Close implements Bus.
func (b *InProc) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := make([]*inprocSub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = make(map[*inprocSub]struct{})
	b.mu.Unlock()

	for _, s := range subs {
		s.shutdown()
	}
	return nil
}

func (b *InProc) remove(s *inprocSub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

type inprocSub struct {
	bus    *InProc
	filter Filter
	max    int

	mu     sync.Mutex
	queue  []Event
	closed bool
	err    error

	dropped atomic.Uint64
	out     chan Event
	wake    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (s *inprocSub) Events() <-chan Event { return s.out }

func (s *inprocSub) Dropped() uint64 { return s.dropped.Load() }

func (s *inprocSub) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// fail records the terminal error surfaced by Err. Called by the deliver
// goroutine before the Events channel closes.
func (s *inprocSub) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *inprocSub) Close() error {
	s.bus.remove(s)
	s.shutdown()
	return nil
}

func (s *inprocSub) shutdown() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
	})
}

// enqueue appends e to the subscriber's buffer, dropping the oldest
// buffered event when full. Called with the bus lock held; never blocks.
func (s *inprocSub) enqueue(e Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.queue) >= s.max {
		s.queue = s.queue[1:]
		s.dropped.Add(1)
	}
	s.queue = append(s.queue, e)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *inprocSub) dequeue() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return Event{}, false
	}
	e := s.queue[0]
	s.queue = s.queue[1:]
	return e, true
}

// deliver runs in its own goroutine: it streams the replay phase (if any)
// from the log, then pumps buffered live events to the out channel. Only
// this goroutine sends on out, so out closes exactly once, after the last
// send.
func (s *inprocSub) deliver(ctx context.Context, opts SubscribeOptions, joinSeq uint64) {
	defer close(s.out)
	if opts.Replay {
		if err := s.replay(ctx, opts.Filter, opts.AfterSeq, joinSeq); err != nil {
			if !errors.Is(err, errSubClosed) {
				s.fail(err)
			}
			_ = s.Close()
			return
		}
	}
	for {
		e, ok := s.dequeue()
		if !ok {
			select {
			case <-s.wake:
				continue
			case <-s.done:
				// Drain what was buffered before close so a bus
				// shutdown does not truncate already-delivered
				// history mid-stream; consumers still see the
				// channel close promptly.
				for {
					rest, restOK := s.dequeue()
					if !restOK {
						return
					}
					select {
					case s.out <- rest:
					default:
						return
					}
				}
			}
		}
		select {
		case s.out <- e:
		case <-s.done:
			return
		}
	}
}

// errSubClosed signals that replay stopped because the subscription was
// closed - a clean termination, not surfaced via Err.
var errSubClosed = errors.New("events: subscription closed")

// replay streams persisted events with afterSeq < Seq <= joinSeq to the
// subscriber. Live events buffered meanwhile all have Seq > joinSeq, so
// the subsequent live phase continues without gaps or duplicates. A nil
// return means replay completed; errSubClosed means the subscription was
// closed mid-replay; any other error is terminal and surfaced via Err.
func (s *inprocSub) replay(ctx context.Context, f Filter, afterSeq, joinSeq uint64) error {
	cursor := afterSeq
	for cursor < joinSeq {
		batch, err := s.bus.log.Read(ctx, f, cursor, joinSeq, replayBatch)
		if err != nil {
			return fmt.Errorf("events: replay read: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		for _, e := range batch {
			select {
			case s.out <- e:
			case <-s.done:
				return errSubClosed
			case <-ctx.Done():
				return fmt.Errorf("events: replay: %w", ctx.Err())
			}
			cursor = e.Seq
		}
	}
	return nil
}
