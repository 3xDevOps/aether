// Package coord is conflict coordination: the bounded channel two agents
// use to settle a file overlap the conflict radar found, without waiting
// for a human.
//
// The radar (internal/overlap) says runs A and B are both editing the
// same file. This package injects one advisory notice into both agents'
// terminals, gives each run a private unix socket under the server data
// directory, and serves exactly three methods on it - coord.status,
// coord.send, coord.inbox. The mount is the authentication: whoever
// connects on a run's socket is that run, so no token ever enters a
// container.
//
// Nothing here blocks, locks, or arbitrates. A send is authorized only
// against a live radar overlap (or its grace window), is size-capped,
// rate-limited, depth-capped, and stamped into the workspace timeline;
// beyond that the agents are on their own, and the radar chips remain the
// human safety net.
package coord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// DefaultGrace is how long a peer stays messageable after its overlap
// clears, so a reply already being written still lands.
const DefaultGrace = 10 * time.Minute

var (
	// ErrDisabled is returned by the host-side lifecycle calls when the
	// conflict-coordination kill switch is off.
	ErrDisabled = errors.New("coord: conflict coordination is disabled")
	// ErrClosed is returned once the service has been closed.
	ErrClosed = errors.New("coord: service closed")
)

// Runs resolves the runs and members a message is attributed to;
// satisfied by store.Store.
type Runs interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	ListActiveRuns(ctx context.Context) ([]*domain.Run, error)
}

// Peers is the conflict radar's read side and the sole authorization
// source for sends; satisfied by *overlap.Index.
type Peers interface {
	Overlaps(ctx context.Context) ([]overlap.Entry, error)
}

// Injector writes an attributed banner into a run's terminal and its
// transcript; satisfied by *ptyhost.Host.
type Injector interface {
	Inject(ctx context.Context, key ptyhost.SessionKey, actorName, actorColor, message string) error
}

// Config wires the service. Dir, Store, Mail, Bus, and Peers are
// required; PTY may be nil, which degrades to no notices.
type Config struct {
	// Dir is the coordination state root, <data>/coord.
	Dir string
	// Store resolves runs and members.
	Store Runs
	// Mail persists the mailbox.
	Mail store.MessageStore
	// Bus carries the radar's overlap changes in and timeline entries out.
	Bus events.Bus
	// Peers is the radar index sends are authorized against.
	Peers Peers
	// PTY injects the overlap notice into a run's terminal.
	PTY Injector
	// Disabled is the conflict-coordination kill switch. When set, no
	// notice, listener, directory, mailbox write, or timeline entry
	// happens, and every coord.* call fails CodeUnavailable.
	Disabled bool
	// Grace overrides DefaultGrace.
	Grace time.Duration
	// now overrides the clock in tests.
	now func() time.Time
	// idle overrides idleTimeout in tests.
	idle time.Duration
}

// Service is the coordination mailbox plus the per-run socket listeners.
type Service struct {
	cfg   Config
	radar *radar
	now   func() time.Time

	// serveCtx bounds every connection handler and the radar consumer; it
	// is cancelled by Close.
	serveCtx context.Context
	stop     context.CancelFunc

	mu           sync.Mutex
	listeners    map[socketKey]*net.UnixListener
	buckets      map[domain.RunID]*bucket
	inboxBuckets map[domain.RunID]*bucket
	peers        map[domain.RunID]map[domain.RunID]bool
	noticed      map[domain.RunID]map[domain.RunID]bool
	sub          events.Subscription
	closed       bool

	wg sync.WaitGroup
}

// socketKey identifies one listener: a run and the wire-version socket
// name it is bound to.
type socketKey struct {
	run  domain.RunID
	name string
}

// New builds the service; call Start to recover listeners and begin
// consuming radar events.
func New(cfg Config) (*Service, error) {
	if cfg.Dir == "" || cfg.Store == nil || cfg.Mail == nil || cfg.Bus == nil || cfg.Peers == nil {
		return nil, errors.New("coord: config: Dir, Store, Mail, Bus, and Peers are required")
	}
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.idle <= 0 {
		cfg.idle = idleTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		cfg:          cfg,
		radar:        newRadar(cfg.Peers, cfg.Grace, cfg.now),
		now:          cfg.now,
		serveCtx:     ctx,
		stop:         cancel,
		listeners:    make(map[socketKey]*net.UnixListener),
		buckets:      make(map[domain.RunID]*bucket),
		inboxBuckets: make(map[domain.RunID]*bucket),
		peers:        make(map[domain.RunID]map[domain.RunID]bool),
		noticed:      make(map[domain.RunID]map[domain.RunID]bool),
	}, nil
}

// Start recovers the host-side listeners left by the previous process and,
// while coordination is enabled, begins consuming the radar's overlap
// changes. ctx bounds only the setup; the service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	if err := s.recoverListeners(ctx); err != nil {
		return err
	}
	if s.cfg.Disabled {
		return nil
	}
	sub, err := s.cfg.Bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunOverlap}},
	})
	if err != nil {
		return fmt.Errorf("coord: subscribe to overlap changes: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = sub.Close()
		return ErrClosed
	}
	s.sub = sub
	// Counted under the same lock as the closed check, so a concurrent
	// Close cannot finish its Wait before the consumer is tracked.
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.consume(s.serveCtx, sub)
	}()
	return nil
}

// Close stops the listeners and the consumer and waits for them to
// return. The socket files stay on disk: they are the record of which
// runs were provisioned, and with which wire version, that the next start
// recovers from. Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sub := s.sub
	listeners := make([]*net.UnixListener, 0, len(s.listeners))
	for k, l := range s.listeners {
		listeners = append(listeners, l)
		delete(s.listeners, k)
	}
	s.mu.Unlock()

	s.stop()
	if sub != nil {
		_ = sub.Close()
	}
	var errs []error
	for _, l := range listeners {
		if err := l.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.wg.Wait()
	return errors.Join(errs...)
}

// consume folds the radar's overlap changes into the grace bookkeeping and
// the notice injector.
//
// There is no replay machinery here because nothing depends on seeing
// every event: the next change re-announces the whole set, authorization
// always re-reads the live index, and a grace window runs from the last
// instant the peers were seen overlapping, so discovering a clearing late
// cannot hand out a window longer than the grace period. A dropped event
// therefore costs at most one notice.
func (s *Service) consume(ctx context.Context, sub events.Subscription) {
	for e := range sub.Events() {
		p, ok := e.Payload.(events.OverlapPayload)
		if !ok || e.RunID == "" {
			continue
		}
		current := make(map[domain.RunID][]string, len(p.With))
		for _, peer := range p.With {
			current[peer.RunID] = peer.Files
		}
		s.radar.observe(e.RunID, current)
		s.notify(ctx, e.RunID, p.With)
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// unavailable is the kill switch's answer: every coord.* method fails
// before it touches the mailbox, the radar, or the timeline.
func unavailable(method string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: method + ": conflict coordination is disabled",
	}
}

// removeFile deletes path, tolerating an already-absent file.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
