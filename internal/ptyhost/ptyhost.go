// Package ptyhost owns persistent server-side PTY sessions.
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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// WriteGate is the Wave 3 capability-check hook for write-mode attach.
// nil = allow everyone (Wave 1 default). The hook is the whole contract;
// no permission logic lives in ptyhost.
type WriteGate func(ctx context.Context, member domain.MemberID, key SessionKey) error

// Config configures a Host.
type Config struct {
	TranscriptDir string    // <data>/transcripts
	ReplayBytes   int       // scrollback replayed to new attachments; default 1 MiB
	DefaultCols   uint      // 120
	DefaultRows   uint      // 30
	Gate          WriteGate // nil = allow
	// OnTitle is declared for the title scanner and never called yet.
	OnTitle func(key SessionKey, title string)
}

const (
	defaultReplayBytes = 1 << 20
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

// Host manages one persistent PTY session per key.
type Host struct {
	cfg Config

	mu       sync.Mutex
	sessions map[SessionKey]*session
	starting map[SessionKey]struct{}
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
		sessions: make(map[SessionKey]*session),
		starting: make(map[SessionKey]struct{}),
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
// key: it sets the initial geometry, opens the transcript, and pumps PTY
// output to the transcript, the replay buffer, and every attached client.
// The session survives zero attachments; when the agent exits (stdout EOF)
// it enters the ended state and stays queryable until StopSession.
func (h *Host) StartSession(ctx context.Context, key SessionKey, att runtime.Attachment) error {
	// Reserve the key before touching the transcript file so a losing
	// duplicate StartSession can never truncate the winner's transcript.
	if err := h.reserve(key); err != nil {
		return err
	}
	// Replace a session that ended (its process exited) so a fresh process
	// can reuse the key: a run-shell tab whose shell exited must be
	// reopenable. stop() is idempotent and closes the old attachment.
	if prev := h.lookup(key); prev != nil {
		prev.stop()
	}
	path := h.transcriptPath(key)
	var err error
	var seed []byte
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 && key.seedsReplay() {
		seed, err = readCastTail(path, h.cfg.ReplayBytes)
		if err != nil {
			slog.Warn("ptyhost: seed replay from transcript", "path", path, "error", err)
			seed = nil
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		slog.Warn("ptyhost: inspect transcript for replay", "path", path, "error", statErr)
	}
	tr, err := newCastWriter(path, h.cfg.DefaultCols, h.cfg.DefaultRows)
	if err != nil {
		h.unreserve(key)
		return err
	}
	// Initial geometry goes out before the session is attachable, so a
	// concurrent write-attach clamp can never be overwritten by it.
	_ = att.Resize(ctx, h.cfg.DefaultCols, h.cfg.DefaultRows)
	s := &session{
		run:     key,
		att:     att,
		tr:      tr,
		stdin:   att.Stdin(),
		clients: make(map[*client]struct{}),
		ring:    newRing(h.cfg.ReplayBytes),
		cols:    h.cfg.DefaultCols,
		rows:    h.cfg.DefaultRows,
		done:    make(chan struct{}),
	}
	if len(seed) > 0 {
		s.ring.write(seed)
	}
	if h.cfg.OnTitle != nil {
		s.onTitle = func(title string) {
			h.cfg.OnTitle(key, title)
		}
	}

	h.mu.Lock()
	delete(h.starting, key)
	if h.closed {
		h.mu.Unlock()
		_ = tr.close()
		_ = att.Close()
		return errHostClosed
	}
	h.sessions[key] = s
	h.mu.Unlock()

	go s.pump()
	return nil
}

// StopSession closes the session's attachment, flushes and closes the
// transcript, and detaches every client. Idempotent; ErrNoSession only for
// a key never started.
func (h *Host) StopSession(ctx context.Context, key SessionKey) error {
	_ = ctx
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	s.stop()
	return nil
}

// RemoveRunTranscripts removes the agent transcript and every run-shell
// transcript for a run after the scheduler has stopped their sessions.
func (h *Host) RemoveRunTranscripts(ctx context.Context, run domain.RunID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := filepath.Base(string(run))
	patterns := []string{
		filepath.Join(h.cfg.TranscriptDir, name+".cast"),
		filepath.Join(h.cfg.TranscriptDir, name+".*.cast"),
		filepath.Join(h.cfg.TranscriptDir, "run-shell-"+name+"-*.cast"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("ptyhost: find run transcripts: %w", err)
		}
		for _, path := range matches {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("ptyhost: remove run transcript: %w", err)
			}
		}
	}
	return nil
}

// LastOutput reports the wall-clock time of the last byte the agent wrote
// to the session's PTY; false if the key has no session. Bytes that came
// from the server are excluded - an injection banner, and the terminal's
// echo of anything written to the agent's input, injected or typed on an
// attach - so this is a liveness clock for the agent, not for the stream.
func (h *Host) LastOutput(key SessionKey) (time.Time, bool) {
	s := h.lookup(key)
	if s == nil {
		return time.Time{}, false
	}
	return s.lastOutput()
}

// Replay streams the run's recorded terminal output exactly as the agent
// wrote it, decoded from the asciinema transcript. It fails with
// os.ErrNotExist when the run never recorded a transcript - a session that
// was never started, or an artifact from before recording existed. The
// error is returned before anything is written, so a caller can fall back
// to its own refusal when no transcript exists.
func (h *Host) Replay(run domain.RunID) (io.ReadCloser, error) {
	f, err := os.Open(h.transcriptPath(RunSession(run)))
	if err != nil {
		return nil, fmt.Errorf("ptyhost: open transcript: %w", err)
	}
	return &replayReader{f: f, br: bufio.NewReader(f)}, nil
}

// Inject writes an attributed banner to every attachment and the transcript,
// then writes message plus a carriage return to the session's stdin. The
// banner never reaches the agent's input, and neither it nor the echo the
// terminal sends back advances LastOutput. Authorization is the caller's.
func (h *Host) Inject(ctx context.Context, key SessionKey, actorName, actorColor, message string) error {
	_ = ctx
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	return s.inject(actorName, actorColor, message)
}

// Attach connects conn to the session's PTY and blocks until conn's read
// side returns EOF or an error, ctx is done, the session ends (returns nil),
// or the host closes. Reads from conn are keystrokes (discarded when
// readOnly); writes to conn are raw PTY output, starting with a replay of
// the recent scrollback. resize carries [cols, rows] updates (nil = fixed
// geometry). Write-mode attaches are checked against the configured Gate.
func (h *Host) Attach(ctx context.Context, key SessionKey, member domain.MemberID, cols, rows uint, readOnly bool, conn io.ReadWriter, resize <-chan [2]uint) error {
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	if !readOnly && h.cfg.Gate != nil {
		if err := h.cfg.Gate(ctx, member, key); err != nil {
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

func (h *Host) lookup(key SessionKey) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[key]
}

// reserve claims key for an in-flight StartSession under h.mu.
func (h *Host) reserve(key SessionKey) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errHostClosed
	}
	if _, ok := h.starting[key]; ok {
		return fmt.Errorf("ptyhost: session already started for key %s", key)
	}
	// An ended session (its process exited) does not block the key: the
	// restart in StartSession stops and replaces it.
	if prev, ok := h.sessions[key]; ok && prev.isActive() {
		return fmt.Errorf("ptyhost: session already started for key %s", key)
	}
	h.starting[key] = struct{}{}
	return nil
}

func (h *Host) unreserve(key SessionKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.starting, key)
}

func (h *Host) transcriptPath(key SessionKey) string {
	name := strings.ReplaceAll(string(key), ":", "-")
	return filepath.Join(h.cfg.TranscriptDir, name+".cast")
}

// ActiveSessions returns the keys of live sessions with the given prefix.
func (h *Host) ActiveSessions(prefix string) []SessionKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]SessionKey, 0)
	for key, s := range h.sessions {
		if strings.HasPrefix(string(key), prefix) && s.isActive() {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// StopSessionsWithPrefix stops every live session whose key has prefix.
// Repeated calls are safe and never report errors.
func (h *Host) StopSessionsWithPrefix(ctx context.Context, prefix string) {
	_ = ctx
	h.mu.Lock()
	sessions := make([]*session, 0)
	for key, s := range h.sessions {
		if strings.HasPrefix(string(key), prefix) {
			sessions = append(sessions, s)
		}
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
}
