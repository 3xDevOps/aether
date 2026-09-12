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
	// OnInput reports that member typed into the session, at most once per
	// write attach and only after the keystrokes reached the PTY. Bytes a
	// terminal sends by itself do not count; see input.go. Who is typing is
	// known here and nowhere below: neither the session nor its clients
	// carry a member.
	OnInput func(key SessionKey, member domain.MemberID)
}

// readBufferBytes is one read from an attach. It also bounds what the
// input scanner carries between reads (input.go).
const readBufferBytes = 4096

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
	sessions map[SessionKey]*session // stopped entries are lightweight idempotency sentinels
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
	var modes modeScanner
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 && key.seedsReplay() {
		seed, err = readCastTail(path, h.cfg.ReplayBytes)
		if err != nil {
			slog.Warn("ptyhost: seed replay from transcript", "path", path, "error", err)
			seed = nil
		}
		scanned, scanErr := readCastModes(path)
		if scanErr != nil {
			h.unreserve(key)
			return fmt.Errorf("ptyhost: restore terminal modes: %w", scanErr)
		}
		modes = scanned
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		slog.Warn("ptyhost: inspect transcript for replay", "path", path, "error", statErr)
	}
	tr, err := newCastWriter(path, h.cfg.DefaultCols, h.cfg.DefaultRows)
	if err != nil {
		h.unreserve(key)
		return err
	}
	// Carry recovered modes across this transcript's next rotation too.
	tr.output(modes.preamble())
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
		modes:   modes,
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

// Inject writes message plus submit (the harness's submit sequence, e.g. a
// carriage return) to the session's stdin, then records an attributed
// banner after the complete write succeeds. The banner never reaches the
// agent's input, and neither it nor the terminal's echo advances
// LastOutput. Authorization is the caller's.
func (h *Host) Inject(ctx context.Context, key SessionKey, actorName, actorColor, message, submit string) error {
	_ = ctx
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	return s.inject(actorName, actorColor, message, submit)
}

// ReplayWriter is implemented by an attach conn that wants to be told where
// scrollback replay ends. WriteReplay is called exactly once per attach,
// before any other Write, possibly with an empty slice.
type ReplayWriter interface {
	WriteReplay(p []byte) (int, error)
}

// reportInput hands the first keystroke of a write attach to OnInput. The
// callback records a durable fact, so it runs off the read loop rather
// than making the next keystroke wait for it.
func (h *Host) reportInput(key SessionKey, member domain.MemberID) {
	if h.cfg.OnInput == nil {
		return
	}
	go h.cfg.OnInput(key, member)
}

// AttachClient is what one attach declares about itself: the geometry it
// brings, whether it may write, and whether it follows the session.
type AttachClient struct {
	Member   domain.MemberID
	Cols     uint
	Rows     uint
	ReadOnly bool
	// Follow renders at the session's geometry and imposes none, so a
	// screen too small to hold the agent's can still steer it without
	// reflowing that screen for everyone else watching. A follower is
	// told the size it should draw at, at attach and at every change.
	Follow bool
	// Resume attaches without the scrollback replay. The client already
	// holds this session's screen and is reattaching only to change what
	// it may do, so replaying would redraw what is already correct - and
	// would cost the client the terminal state it built up, which a fresh
	// replay has to reconstruct from a preamble.
	Resume bool
	// Cursor is how much of the session's output this client has already
	// seen, as reported to it by the ack it is resuming from. It is what
	// makes the reattach lossless: the session hands back exactly the
	// bytes produced since, rather than everything or nothing.
	Cursor uint64
}

// ResumeWriter is an attach conn that reports how a resume was answered:
// the cursor this client now holds, and whether the session could still
// serve the one it asked from. A client told it was not resumed has to
// clear its screen before the replay that follows, because that replay is
// the whole scrollback rather than the gap.
type ResumeWriter interface {
	SetResume(cursor uint64, resumed bool)
}

// GeometryWriter is an attach conn that wants the session's PTY size: once
// as the attach joins, which is what an ack reports, and again whenever the
// size changes under it. Out of band from the output the conn also carries,
// so nothing of this reaches the transcript.
type GeometryWriter interface {
	SetGeometry(cols, rows uint)
}

// Attach connects conn to the session's PTY and blocks until conn's read
// side returns EOF or an error, ctx is done, the session ends (returns nil),
// or the host closes. Reads from conn are keystrokes (discarded when
// read-only); writes to conn are raw PTY output, starting with a replay of
// the recent scrollback. resize carries [cols, rows] updates (nil = fixed
// geometry). Write-mode attaches are checked against the configured Gate.
func (h *Host) Attach(ctx context.Context, key SessionKey, a AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	if !a.ReadOnly && h.cfg.Gate != nil {
		if err := h.cfg.Gate(ctx, a.Member, key); err != nil {
			return fmt.Errorf("%w: %v", ErrWriteDenied, err)
		}
	}
	if a.Cols == 0 {
		a.Cols = h.cfg.DefaultCols
	}
	if a.Rows == 0 {
		a.Rows = h.cfg.DefaultRows
	}
	c := newClient(conn, a)
	if err := s.addClient(c); err != nil {
		return err
	}
	defer s.removeClient(c)
	// The size the session is, not the size this client asked for: the ack
	// reports it, so a follower draws what the writers see from its first
	// frame rather than from the first change after it joined.
	c.tellGeometry(s.geometry())
	c.tellResume()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, readBufferBytes)
		var scan inputScanner
		typed := false
		for {
			n, err := conn.Read(buf)
			if c.isClosed() {
				return
			}
			if n > 0 && !a.ReadOnly {
				if !s.writeStdin(buf[:n]) {
					return
				}
				if !typed && scan.typed(buf[:n]) {
					typed = true
					h.reportInput(key, a.Member)
				}
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

// reserve claims key for an in-flight StartSession. The host lock only
// protects the session and reservation maps; session state is checked after
// releasing it so a wedged session cannot stall unrelated host operations.
func (h *Host) reserve(key SessionKey) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errHostClosed
	}
	if _, ok := h.starting[key]; ok {
		h.mu.Unlock()
		return fmt.Errorf("ptyhost: session already started for key %s", key)
	}
	prev := h.sessions[key]
	h.starting[key] = struct{}{}
	h.mu.Unlock()

	// An ended session (its process exited) does not block the key: the
	// restart in StartSession stops and replaces it. Do not hold h.mu while
	// asking the session for its state.
	if prev != nil && prev.isActive() {
		h.unreserve(key)
		return fmt.Errorf("ptyhost: session already started for key %s", key)
	}
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
	var sessions []struct {
		key SessionKey
		s   *session
	}
	for key, s := range h.sessions {
		if strings.HasPrefix(string(key), prefix) {
			sessions = append(sessions, struct {
				key SessionKey
				s   *session
			}{key: key, s: s})
		}
	}
	h.mu.Unlock()

	keys := make([]SessionKey, 0, len(sessions))
	for _, entry := range sessions {
		if entry.s.isActive() {
			keys = append(keys, entry.key)
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
