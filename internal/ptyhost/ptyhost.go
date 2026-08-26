// Package ptyhost owns the persistent server-side PTY session of each run.
//
// A Host adopts the runtime.Attachment opened by the scheduler and keeps the
// agent's terminal alive independently of any connected client (tmux
// semantics): clients attach and detach freely without the agent noticing,
// new clients get a scrollback replay, concurrent write-capable clients share
// the terminal with tmux-style geometry clamping, and members can inject
// attributed instructions. All output is recorded incrementally as an
// asciinema cast v2 transcript.
package ptyhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// WriteGate is the Wave 3 capability-check hook for write-mode attach.
// nil = allow everyone (Wave 1 default). The hook is the whole contract;
// no permission logic lives in ptyhost.
type WriteGate func(ctx context.Context, member domain.MemberID, run domain.RunID) error

// Config configures a Host.
type Config struct {
	TranscriptDir string    // <data>/transcripts
	ReplayBytes   int       // scrollback replayed to new attachments; default 65536
	DefaultCols   uint      // 120
	DefaultRows   uint      // 30
	Gate          WriteGate // nil = allow
}

const (
	defaultReplayBytes = 65536
	defaultCols        = 120
	defaultRows        = 30
)

var (
	ErrNoSession    = errors.New("ptyhost: no session for run")
	ErrSessionEnded = errors.New("ptyhost: session ended")
	ErrWriteDenied  = errors.New("ptyhost: write access denied")
)

var errHostClosed = errors.New("ptyhost: host closed")

// drainTimeout bounds how long a cleanly ending Attach waits for its write
// loop to flush buffered output to a client that has stopped reading.
const drainTimeout = 5 * time.Second

// Host manages one persistent PTY session per run.
type Host struct {
	cfg Config

	mu       sync.Mutex
	sessions map[domain.RunID]*session
	starting map[domain.RunID]struct{}
	closed   bool
}

// New validates cfg, fills defaults, and creates the transcript directory.
func New(cfg Config) (*Host, error) {
	if cfg.TranscriptDir == "" {
		return nil, errors.New("ptyhost: config: transcript dir is required")
	}
	if cfg.ReplayBytes <= 0 {
		cfg.ReplayBytes = defaultReplayBytes
	}
	if cfg.DefaultCols == 0 {
		cfg.DefaultCols = defaultCols
	}
	if cfg.DefaultRows == 0 {
		cfg.DefaultRows = defaultRows
	}
	if err := os.MkdirAll(cfg.TranscriptDir, 0o755); err != nil {
		return nil, fmt.Errorf("ptyhost: create transcript dir: %w", err)
	}
	return &Host{
		cfg:      cfg,
		sessions: make(map[domain.RunID]*session),
		starting: make(map[domain.RunID]struct{}),
	}, nil
}

// Close stops all sessions and flushes their transcripts.
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	all := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, s)
	}
	h.mu.Unlock()
	for _, s := range all {
		s.stop()
	}
	return nil
}

// StartSession takes ownership of att and starts the persistent session for
// run: it sets the initial geometry, opens the transcript, and pumps PTY
// output to the transcript, the replay buffer, and every attached client.
// The session survives zero attachments; when the agent exits (stdout EOF)
// it enters the ended state and stays queryable until StopSession.
func (h *Host) StartSession(ctx context.Context, run domain.RunID, att runtime.Attachment) error {
	// Reserve the run before touching the transcript file so a losing
	// duplicate StartSession can never truncate the winner's transcript.
	if err := h.reserve(run); err != nil {
		return err
	}
	tr, err := newCastWriter(h.transcriptPath(run), h.cfg.DefaultCols, h.cfg.DefaultRows)
	if err != nil {
		h.unreserve(run)
		return err
	}
	// Initial geometry goes out before the session is attachable, so a
	// concurrent write-attach clamp can never be overwritten by it.
	_ = att.Resize(ctx, h.cfg.DefaultCols, h.cfg.DefaultRows)
	s := &session{
		run:     run,
		att:     att,
		tr:      tr,
		stdin:   att.Stdin(),
		clients: make(map[*client]struct{}),
		ring:    newRing(h.cfg.ReplayBytes),
		cols:    h.cfg.DefaultCols,
		rows:    h.cfg.DefaultRows,
		done:    make(chan struct{}),
	}

	h.mu.Lock()
	delete(h.starting, run)
	if h.closed {
		h.mu.Unlock()
		_ = tr.close()
		_ = att.Close()
		return errHostClosed
	}
	h.sessions[run] = s
	h.mu.Unlock()

	go s.pump()
	return nil
}

// StopSession closes the run's attachment, flushes and closes the
// transcript, and detaches every client. Idempotent; ErrNoSession only for
// a run never started.
func (h *Host) StopSession(ctx context.Context, run domain.RunID) error {
	_ = ctx
	s := h.lookup(run)
	if s == nil {
		return ErrNoSession
	}
	s.stop()
	return nil
}

// LastOutput reports the wall-clock time of the last byte read from the
// run's PTY; false if the run has no session.
func (h *Host) LastOutput(run domain.RunID) (time.Time, bool) {
	s := h.lookup(run)
	if s == nil {
		return time.Time{}, false
	}
	return s.lastOutput()
}

// Inject writes an attributed banner to every attachment and the transcript,
// then writes message plus a carriage return to the agent's stdin. The
// banner never reaches the agent's input. Authorization is the caller's.
func (h *Host) Inject(ctx context.Context, run domain.RunID, actorName, actorColor, message string) error {
	_ = ctx
	s := h.lookup(run)
	if s == nil {
		return ErrNoSession
	}
	return s.inject(actorName, actorColor, message)
}

// Attach connects conn to the run's PTY session and blocks until conn's read
// side returns EOF or an error, ctx is done, the session ends (returns nil),
// or the host closes. Reads from conn are keystrokes (discarded when
// readOnly); writes to conn are raw PTY output, starting with a replay of
// the recent scrollback. resize carries [cols, rows] updates (nil = fixed
// geometry). Write-mode attaches are checked against the configured Gate.
func (h *Host) Attach(ctx context.Context, run domain.RunID, member domain.MemberID, cols, rows uint, readOnly bool, conn io.ReadWriter, resize <-chan [2]uint) error {
	s := h.lookup(run)
	if s == nil {
		return ErrNoSession
	}
	if !readOnly && h.cfg.Gate != nil {
		if err := h.cfg.Gate(ctx, member, run); err != nil {
			return fmt.Errorf("%w: %v", ErrWriteDenied, err)
		}
	}
	if cols == 0 {
		cols = h.cfg.DefaultCols
	}
	if rows == 0 {
		rows = h.cfg.DefaultRows
	}
	c := newClient(conn, readOnly, cols, rows)
	if err := s.addClient(c); err != nil {
		return err
	}
	defer s.removeClient(c)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if c.isClosed() {
				return
			}
			if n > 0 && !readOnly && !s.writeStdin(buf[:n]) {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	writeDone := make(chan error, 1)
	go func() { writeDone <- c.writeLoop() }()

	for {
		select {
		case <-ctx.Done():
			c.close(ctx.Err())
			return ctx.Err()
		case <-readDone:
			c.close(nil)
			return nil
		case werr := <-writeDone:
			return werr
		case <-c.done:
			if cerr := c.getErr(); cerr != nil {
				return cerr
			}
			// Closed cleanly (session ended or host stopped): give the
			// write loop a bounded chance to drain the remaining output,
			// still honoring ctx - a client that stopped reading must
			// never pin this handler.
			t := time.NewTimer(drainTimeout)
			defer t.Stop()
			select {
			case werr := <-writeDone:
				return werr
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		case sz, ok := <-resize:
			if !ok {
				resize = nil
				continue
			}
			s.resizeClient(c, sz[0], sz[1])
		}
	}
}

func (h *Host) lookup(run domain.RunID) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[run]
}

// reserve claims run for an in-flight StartSession under h.mu.
func (h *Host) reserve(run domain.RunID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errHostClosed
	}
	if _, ok := h.starting[run]; ok {
		return fmt.Errorf("ptyhost: session already started for run %s", run)
	}
	if prev, ok := h.sessions[run]; ok && !prev.isStopped() {
		return fmt.Errorf("ptyhost: session already started for run %s", run)
	}
	h.starting[run] = struct{}{}
	return nil
}

func (h *Host) unreserve(run domain.RunID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.starting, run)
}

func (h *Host) transcriptPath(run domain.RunID) string {
	return filepath.Join(h.cfg.TranscriptDir, string(run)+".cast")
}
