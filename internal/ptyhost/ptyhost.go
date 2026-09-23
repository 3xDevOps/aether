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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const resumeIDBytes = 32
const historyCursorKeyFile = ".history-cursor-key"

func newResumeID() (string, error) {
	var raw [resumeIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

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
	ErrNoSession       = errors.New("ptyhost: no session for run")
	ErrSessionEnded    = errors.New("ptyhost: session ended")
	ErrWriteDenied     = errors.New("ptyhost: write access denied")
	ErrInvalidRunID    = errors.New("ptyhost: invalid run id")
	ErrSessionReplaced = errors.New("ptyhost: session was replaced")
	// ErrSnapshotPending means one bounded, deduplicated background repair is
	// rebuilding a missing, stale, or invalid screen checkpoint.
	ErrSnapshotPending = errors.New("ptyhost: terminal snapshot repair pending")
	// ErrSnapshotUnavailable means repair finished without an authoritative
	// snapshot. Callers may retry only after the recording changes.
	ErrSnapshotUnavailable = errors.New("ptyhost: terminal snapshot unavailable")
	errAttachNoAdmission   = errors.New("ptyhost: attach commit did not admit client")
	errAttachRepeated      = errors.New("ptyhost: attach admission called more than once")
)

func validateRunID(run domain.RunID) error {
	id := string(run)
	if id == "" || len(id) > 128 {
		return ErrInvalidRunID
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return ErrInvalidRunID
		}
	}
	if id[0] == '.' || id[0] == '-' || strings.Contains(id, "..") {
		return ErrInvalidRunID
	}
	return nil
}

var errHostClosed = errors.New("ptyhost: host closed")

// drainTimeout bounds how long a cleanly ending Attach waits for its write
// loop to flush buffered output to a client that has stopped reading.
const drainTimeout = 5 * time.Second

// Host manages one persistent PTY session per key.
type Host struct {
	historyCursorKey [32]byte
	cfg              Config

	mu             sync.Mutex
	sessions       map[SessionKey]*session // stopped entries are lightweight idempotency sentinels
	starting       map[SessionKey]struct{}
	snapshots      map[SessionKey]*snapshotResult
	nextGeneration uint64
	closed         bool
}

func syncHistoryCursorKeyDirectory(dir string) error {
	// Windows cannot flush a read-only directory handle.
	if goruntime.GOOS == "windows" {
		return nil
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	return errors.Join(syncErr, closeErr)
}

func loadHistoryCursorKey(dir string) ([32]byte, error) {
	path := filepath.Join(dir, historyCursorKeyFile)
	read := func() ([32]byte, error) {
		var key [32]byte
		info, err := os.Lstat(path)
		if err != nil {
			return key, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return key, errors.New("ptyhost: history cursor key has insecure permissions")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return key, err
		}
		if len(data) != len(key) {
			return key, errors.New("ptyhost: history cursor key has invalid length")
		}
		copy(key[:], data)
		return key, nil
	}
	if key, err := read(); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, err
	}

	var generated [32]byte
	if _, err := rand.Read(generated[:]); err != nil {
		return generated, fmt.Errorf("ptyhost: generate history cursor key: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+historyCursorKeyFile+"-*")
	if err != nil {
		return generated, fmt.Errorf("ptyhost: create history cursor key: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return generated, fmt.Errorf("ptyhost: secure history cursor key: %w", err)
	}
	if _, err = tmp.Write(generated[:]); err != nil {
		_ = tmp.Close()
		return generated, fmt.Errorf("ptyhost: write history cursor key: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return generated, fmt.Errorf("ptyhost: sync history cursor key: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return generated, fmt.Errorf("ptyhost: close history cursor key: %w", err)
	}
	if err = os.Link(tmpPath, path); err == nil {
		if err = syncHistoryCursorKeyDirectory(dir); err != nil {
			return generated, fmt.Errorf("ptyhost: sync history cursor key directory: %w", err)
		}
		return generated, nil
	} else if !errors.Is(err, os.ErrExist) {
		return generated, fmt.Errorf("ptyhost: publish history cursor key: %w", err)
	}
	key, err := read()
	if err != nil {
		return generated, fmt.Errorf("ptyhost: load history cursor key: %w", err)
	}
	return key, nil
}

type snapshotResult struct {
	done     chan struct{}
	snapshot ScreenSnapshot
	err      error
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
	if err := validateScreenDimensions(cfg.DefaultCols, cfg.DefaultRows); err != nil {
		return nil, fmt.Errorf("ptyhost: config screen size: %w", err)
	}
	if err := os.MkdirAll(cfg.TranscriptDir, 0o755); err != nil {
		return nil, fmt.Errorf("ptyhost: create transcript dir: %w", err)
	}
	h := &Host{
		cfg:       cfg,
		sessions:  make(map[SessionKey]*session),
		starting:  make(map[SessionKey]struct{}),
		snapshots: make(map[SessionKey]*snapshotResult),
	}
	historyCursorKey, err := loadHistoryCursorKey(cfg.TranscriptDir)
	if err != nil {
		return nil, err
	}
	h.historyCursorKey = historyCursorKey
	return h, nil
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
	var stopErr error
	for _, s := range all {
		stopErr = errors.Join(stopErr, s.stop())
	}
	return stopErr
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
	epoch, err := newTerminalEpoch()
	if err != nil {
		h.unreserve(key)
		return fmt.Errorf("ptyhost: generate terminal epoch: %w", err)
	}
	resumeID := string(epoch)
	if prev := h.lookup(key); prev != nil {
		if stopErr := prev.stop(); stopErr != nil {
			h.unreserve(key)
			return fmt.Errorf("ptyhost: stop previous session: %w", stopErr)
		}
	}

	path := h.transcriptPath(key)
	_, isRun := key.Run()
	var seed []byte
	var modes modeScanner
	var screen *terminalScreen
	var recoveredHistory []castSegment
	position := TerminalPosition{Epoch: epoch}
	recoveredTranscript := false
	initialCols, initialRows := h.cfg.DefaultCols, h.cfg.DefaultRows
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 && key.seedsReplay() {
		if isRun {
			recovered, segments, recoveredPosition, _, checkpointErr := loadCurrentCheckpoint(path, false)
			if checkpointErr != nil {
				h.unreserve(key)
				repair := h.startSnapshotRepair(key, path)
				select {
				case <-repair.done:
					if repair.err != nil {
						return repair.err
					}
					// The repair can finish before this call waits. The error
					// above describes the checkpoint that repair replaced.
					if err = h.reserve(key); err != nil {
						return err
					}
					recovered, segments, recoveredPosition, _, checkpointErr = loadCurrentCheckpoint(path, false)
					if checkpointErr != nil {
						h.unreserve(key)
						return errors.Join(ErrSnapshotUnavailable, fmt.Errorf("ptyhost: repaired checkpoint remains invalid: %w", checkpointErr))
					}
				default:
					return ErrSnapshotPending
				}
			}
			screen, modes, recoveredHistory = recovered.screen, recovered.modes, segments
			position = recoveredPosition
			resumeID = string(position.Epoch)
			recoveredTranscript = true
		} else {
			// Member terminals have no durable screen checkpoint. Recover only a
			// fixed recent suffix under a new epoch, forcing a full replay for old
			// clients rather than claiming continuity we cannot prove.
			seed, err = readRecentCast(path, h.cfg.ReplayBytes)
			if err != nil {
				slog.Warn("ptyhost: seed recent terminal replay", "path", path, "error", err)
				seed = nil
			}
		}
		if isRun {
			seed, err = readRecentCast(path, h.cfg.ReplayBytes)
			if err != nil {
				slog.Warn("ptyhost: seed recent run replay", "path", path, "error", err)
				seed = nil
			}
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		slog.Warn("ptyhost: inspect transcript for replay", "path", path, "error", statErr)
	}
	if screen == nil {
		screen, err = newTerminalScreen(initialCols, initialRows)
		if err != nil {
			h.unreserve(key)
			return err
		}
		if len(seed) > 0 {
			screen.write(seed)
			modes.scan(seed)
			recoveredTranscript = true
		}
	}
	initialCols, initialRows = screen.cols, screen.rows

	tr, err := newCastWriter(path, initialCols, initialRows)
	if err != nil {
		screen.dispose()
		h.unreserve(key)
		return err
	}
	var history []castSegment
	if isRun && len(recoveredHistory) > 0 {
		history = recoveredHistory
		relocateCheckpointSegments(history, path)
	}
	if recoveredTranscript {
		tr.seed(makeScreenSnapshot(screen, modes, position).Data)
	}
	// Initial geometry goes out before the session is attachable, so a
	// concurrent write-attach clamp can never be overwritten by it.
	_ = att.Resize(ctx, initialCols, initialRows)
	s := &session{
		run:          key,
		resumeID:     resumeID,
		att:          att,
		tr:           tr,
		history:      history,
		checkpoint:   checkpointPath(path),
		stdin:        att.Stdin(),
		clients:      make(map[*client]struct{}),
		ring:         newRingAt(h.cfg.ReplayBytes, position),
		cols:         initialCols,
		rows:         initialRows,
		acceptedCols: initialCols,
		acceptedRows: initialRows,
		geoTold:      [2]uint{initialCols, initialRows},
		done:         make(chan struct{}),
		modes:        modes,
		screen:       screen,
	}
	if len(seed) > 0 {
		if isRun {
			if TerminalSequence(len(seed)) <= position.Sequence {
				s.ring.seed(seed, position)
			}
		} else {
			s.ring.write(seed)
		}
	}
	if h.cfg.OnTitle != nil {
		s.onTitle = func(title string) {
			h.cfg.OnTitle(key, title)
		}
	}
	if isRun {
		if err := s.checkpointNow(); err != nil {
			_ = tr.close()
			screen.dispose()
			h.unreserve(key)
			return err
		}
	}
	h.mu.Lock()
	delete(h.starting, key)
	if h.closed {
		h.mu.Unlock()
		_ = tr.close()
		screen.dispose()
		_ = os.Remove(checkpointPath(path))
		_ = att.Close()
		return errHostClosed
	}
	h.nextGeneration++
	if h.nextGeneration == 0 {
		h.nextGeneration++
	}
	s.generation = h.nextGeneration
	h.sessions[key] = s
	delete(h.snapshots, key)
	h.mu.Unlock()

	go s.pump()
	if isRun {
		go s.checkpointLoop()
	}
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
	return s.stop()
}

// RemoveRunTranscripts removes the agent transcript and every run-shell
// transcript for a run after the scheduler has stopped their sessions.
func (h *Host) RemoveRunTranscripts(ctx context.Context, run domain.RunID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRunID(run); err != nil {
		return fmt.Errorf("%w: %q", err, run)
	}
	name := string(run)
	transcript := filepath.Join(h.cfg.TranscriptDir, name+".cast")
	archives, err := removablePriorCastPaths(ctx, transcript)
	if err != nil {
		return fmt.Errorf("ptyhost: find run transcript history: %w", err)
	}
	paths := append(archives, transcript, checkpointPath(transcript))
	patterns := []string{
		filepath.Join(h.cfg.TranscriptDir, "run-shell-"+name+"-*.cast"),
		filepath.Join(h.cfg.TranscriptDir, "run-shell-"+name+"-*.screen"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("ptyhost: find run transcripts: %w", err)
		}
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ptyhost: remove run transcript: %w", err)
		}
	}
	h.mu.Lock()
	for key := range h.snapshots {
		if key == RunSession(run) || strings.HasPrefix(string(key), "run-shell:"+string(run)+":") {
			delete(h.snapshots, key)
		}
	}
	var purge []*session
	for key, s := range h.sessions {
		if key == RunSession(run) || strings.HasPrefix(string(key), "run-shell:"+string(run)+":") {
			purge = append(purge, s)
		}
	}
	h.mu.Unlock()
	for _, s := range purge {
		s.purgeSnapshot()
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

// Replay streams all of a run's recorded terminal output exactly as the agent
// wrote it, decoded from every asciinema transcript incarnation. It fails with
// os.ErrNotExist when the run never recorded a transcript - a session that was
// never started, or an artifact from before recording existed. The error is
// returned before anything is written, so a caller can fall back to its own
// refusal when no transcript exists.
func (h *Host) Replay(run domain.RunID) (io.ReadCloser, int, error) {
	if err := validateRunID(run); err != nil {
		return nil, 0, fmt.Errorf("%w: %q", err, run)
	}
	return openFullCastReplay(h.transcriptPath(RunSession(run)))
}

// ReplayWindow is a bounded recent terminal suffix and its proven boundary.
// Position is zero and Complete is false without a valid v2 checkpoint.
type ReplayWindow struct {
	Reader   io.ReadCloser
	Bytes    int
	Cols     uint
	Rows     uint
	Position TerminalPosition
	Complete bool
}

// RecentReplay reads only a fixed-size tail window from newest segments.
func (h *Host) RecentReplay(run domain.RunID, maxBytes int) (ReplayWindow, error) {
	if err := validateRunID(run); err != nil {
		return ReplayWindow{}, fmt.Errorf("%w: %q", err, run)
	}
	if maxBytes <= 0 {
		return ReplayWindow{}, errors.New("ptyhost: recent replay limit must be positive")
	}
	path := h.transcriptPath(RunSession(run))
	data, err := readRecentCast(path, maxBytes)
	if err != nil {
		return ReplayWindow{}, err
	}
	window := ReplayWindow{Reader: io.NopCloser(bytes.NewReader(data)), Bytes: len(data)}
	if checkpoint, checkpointErr := decodeCheckpoint(checkpointPath(path)); checkpointErr == nil && checkpoint.Version == screenCheckpointVersion {
		if _, boundaryErr := validateCheckpointSegments(path, checkpoint, true); boundaryErr == nil {
			window.Cols, window.Rows = checkpoint.Cols, checkpoint.Rows
			window.Position = TerminalPosition{Epoch: checkpoint.Epoch, Sequence: checkpoint.Sequence}
			window.Complete = true
			return window, nil
		}
	}
	if header, headerErr := newestCastHeader(path); headerErr == nil {
		window.Cols, window.Rows = header.Width, header.Height
	}
	return window, nil
}

func (h *Host) startSnapshotRepair(key SessionKey, path string) *snapshotResult {
	h.mu.Lock()
	if existing := h.snapshots[key]; existing != nil {
		h.mu.Unlock()
		return existing
	}
	result := &snapshotResult{done: make(chan struct{})}
	h.snapshots[key] = result
	h.mu.Unlock()

	go func() {
		snapshot, err := repairColdSnapshot(path)
		if err != nil {
			err = errors.Join(ErrSnapshotUnavailable, fmt.Errorf("ptyhost: repair terminal snapshot: %w", err))
		}
		h.mu.Lock()
		result.snapshot = snapshot
		result.err = err
		close(result.done)
		h.mu.Unlock()
	}()
	return result
}

// Snapshot returns the authoritative compact terminal state when immediately
// available. A missing, stale, or invalid cold checkpoint starts one
// background repair and returns ErrSnapshotPending without scanning the cast
// on the caller goroutine.
func (h *Host) Snapshot(run domain.RunID) (snapshot ScreenSnapshot, err error) {
	if err := validateRunID(run); err != nil {
		return ScreenSnapshot{}, fmt.Errorf("%w: %q", err, run)
	}
	key := RunSession(run)
	h.mu.Lock()
	if s := h.sessions[key]; s != nil {
		h.mu.Unlock()
		return s.snapshot()
	}
	if cached := h.snapshots[key]; cached != nil {
		h.mu.Unlock()
		select {
		case <-cached.done:
			return cloneScreenSnapshot(cached.snapshot), cached.err
		default:
			return ScreenSnapshot{}, ErrSnapshotPending
		}
	}
	h.mu.Unlock()

	path := h.transcriptPath(key)
	recovered, _, position, _, loadErr := loadCurrentCheckpoint(path, false)
	if loadErr == nil {
		snapshot = makeScreenSnapshot(recovered.screen, recovered.modes, position)
		recovered.screen.dispose()
		result := &snapshotResult{done: make(chan struct{}), snapshot: cloneScreenSnapshot(snapshot)}
		close(result.done)
		h.mu.Lock()
		if h.snapshots[key] == nil {
			h.snapshots[key] = result
		}
		h.mu.Unlock()
		return snapshot, nil
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return ScreenSnapshot{}, errors.Join(ErrSnapshotUnavailable, os.ErrNotExist)
	} else if statErr != nil {
		return ScreenSnapshot{}, errors.Join(ErrSnapshotUnavailable, statErr)
	}
	h.startSnapshotRepair(key, path)
	return ScreenSnapshot{}, ErrSnapshotPending
}

func (h *Host) transcriptPath(key SessionKey) string {
	name := strings.ReplaceAll(string(key), ":", "-")
	return filepath.Join(h.cfg.TranscriptDir, name+".cast")
}

// flushLiveTranscript makes output already accepted by a live session visible
// to history. Holding the session lock keeps end/stop from detaching and
// closing the writer between the nil check and the flush.
func (s *session) flushLiveTranscript() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tr == nil {
		return nil
	}
	return s.tr.flush()
}

// Inject writes message plus submit (the harness's submit sequence, e.g. a
// carriage return) to the session's stdin, then records an attributed
// banner after the complete write succeeds. The banner never reaches the
// agent's input, and neither it nor the terminal's echo advances
// LastOutput. Authorization is the caller's.
func (h *Host) Inject(ctx context.Context, key SessionKey, actorName, actorColor, message, submit string) error {
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	return s.inject(ctx, actorName, actorColor, message, submit)
}

// ReplayWriter is implemented by an attach conn that needs the replay byte
// count before streaming it. WriteReplay is called exactly once per attach,
// before any other Write, and must consume replay before returning.
type ReplayWriter interface {
	WriteReplay(replay io.Reader, bytes int) error
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
	Member domain.MemberID
	Cols   uint
	Rows   uint
	// SessionGeneration fences a shell reservation to the exact PTY process
	// it admitted. Zero accepts the current process for non-reserved attaches.
	SessionGeneration uint64
	ReadOnly          bool
	// Screen asks for a compact current-screen replay for a run rather than
	// the complete recorded transcript. Snapshot is the compatibility name
	// retained for older in-process callers.
	Screen   bool
	Snapshot bool
	// Follow renders at the session's geometry and imposes none, so a
	// screen too small to hold the agent's can still steer it without
	// reflowing that screen for everyone else watching. A follower is
	// told the size it should draw at, at attach and at every change.
	Follow bool
	// Resume attaches without the scrollback replay. The client already
	// holds the session's screen and its terminal state, so it is sent
	// exactly the bytes that arrived while it was away.
	Resume bool
	// Position atomically identifies the terminal incarnation and byte
	// boundary this client already holds. Cursor and ResumeID remain as
	// compatibility fields for older in-process callers.
	Position TerminalPosition
	// Cursor is how much of the session's output this client has already
	// seen, as reported to it by the ack it is resuming from. It is what
	// makes the reattach lossless: the session hands back exactly what it
	// missed, rather than everything or nothing.
	Cursor uint64
	// ResumeID identifies the PTY process incarnation that produced Cursor.
	// Resume is honored only when this matches the current session.
	ResumeID          string
	ControlSessionID  string
	ControlGeneration uint64
	// Authorize completes the write authorization synchronously before the
	// client joins the session. It must be bounded.
	Authorize func() error
	// Commit receives admit, which it must call exactly once to register the
	// client. Commit must be bounded, must not otherwise re-enter this session,
	// and must not return an error after admit succeeds.
	Commit func(admit func() error) error
	// OnAttached runs after the client has joined successfully, but before the
	// replay, output, or geometry is written. It commits resources reserved
	// while authorization was in flight.
	OnAttached func()
	// OnControlReady runs after admission and before OnAttached or any stream
	// bytes. setReadOnly updates this client's live input eligibility and
	// geometry contribution without detaching its output stream.
	OnControlReady func(setReadOnly func(bool) error)
	// InputGuard is checked immediately before and after every client read.
	// It fences stale buffered input after a lease is taken over.
	InputGuard func() error
	// InputAdmission wraps the actual PTY acceptance. The callback must invoke
	// accept while the caller's generation-aware authorization lock is held;
	// validating before calling it is insufficient.
	InputAdmission func(accept func() error) error
}

// ShellTabReservation is the scheduler's ownership token for a shell tab
// created while an attach is being admitted. Adopt commits the tab to any
// successful attachment; Rollback is a no-op after adoption and otherwise
// removes only the shell owned by this reservation.
type ShellTabReservation interface {
	// Generation identifies the exact PTY process reserved for attachment.
	Generation() uint64
	Adopt()
	Rollback(context.Context) error
}

// ResumeWriter is an attach conn that reports how a resume was answered:
// the cursor this client now holds, whether the session could still serve
// the one it asked from, and the current PTY process incarnation. A client
// told it was not resumed has to clear its screen before the replay that
// follows, because that replay is the whole scrollback rather than the gap.
type ResumeWriter interface {
	SetResume(cursor uint64, resumed bool, resumeID string)
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
// read-only); writes to conn are raw PTY output, starting with the complete
// run transcript or the recent scrollback for other session types. resize
func (h *Host) Attach(ctx context.Context, key SessionKey, a AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	s := h.lookup(key)
	if s == nil {
		return ErrNoSession
	}
	if a.SessionGeneration != 0 && s.generation != a.SessionGeneration {
		return ErrSessionReplaced
	}
	if a.Cols == 0 {
		a.Cols = h.cfg.DefaultCols
	}
	if a.Rows == 0 {
		a.Rows = h.cfg.DefaultRows
	}
	if err := validateScreenDimensions(a.Cols, a.Rows); err != nil {
		return fmt.Errorf("ptyhost: attach screen size: %w", err)
	}
	if !a.ReadOnly {
		if h.cfg.Gate != nil {
			if err := h.cfg.Gate(ctx, a.Member, key); err != nil {
				return fmt.Errorf("%w: %v", ErrWriteDenied, err)
			}
		}
		if a.Authorize != nil {
			if err := a.Authorize(); err != nil {
				return err
			}
		}
	}
	c := newClient(conn, a)
	var admitMu sync.Mutex
	admitCalls := 0
	admitSucceeded := false
	admitRepeated := false
	var admitErr error
	admit := func() error {
		admitMu.Lock()
		defer admitMu.Unlock()
		if admitCalls != 0 {
			admitRepeated = true
			return errAttachRepeated
		}
		admitCalls = 1
		admitErr = s.addClientCommitted(ctx, c)
		if admitErr == nil {
			admitSucceeded = true
		}
		return admitErr
	}
	admissionState := func() (calls int, succeeded, repeated bool, err error) {
		admitMu.Lock()
		defer admitMu.Unlock()
		return admitCalls, admitSucceeded, admitRepeated, admitErr
	}
	if a.Commit != nil {
		commitErr := a.Commit(admit)
		calls, succeeded, repeated, admissionErr := admissionState()
		if commitErr != nil {
			if succeeded {
				s.removeClient(c)
			}
			return commitErr
		}
		if repeated {
			if succeeded {
				s.removeClient(c)
			}
			return errAttachRepeated
		}
		if calls == 0 {
			return errAttachNoAdmission
		}
		if !succeeded {
			if admissionErr != nil {
				return admissionErr
			}
			return errAttachNoAdmission
		}
	} else if err := admit(); err != nil {
		return err
	}
	if _, succeeded, _, _ := admissionState(); !succeeded {
		return errAttachNoAdmission
	}
	if a.OnControlReady != nil {
		a.OnControlReady(func(readOnly bool) error {
			if !readOnly {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !s.clientAttached(c) {
					return ErrNoSession
				}
				if h.cfg.Gate != nil {
					if err := h.cfg.Gate(ctx, a.Member, key); err != nil {
						return fmt.Errorf("%w: %v", ErrWriteDenied, err)
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			return s.setClientReadOnly(c, readOnly)
		})
	}
	if a.OnAttached != nil {
		a.OnAttached()
	}
	defer s.removeClient(c)
	// The size the session is, not the size this client asked for: the ack
	// reports it, so a follower draws what the writers see from its first
	// frame rather than from the first change after it joined.
	c.tellGeometry(c.replayCols, c.replayRows)
	s.mu.Lock()
	resumeID := s.resumeID
	s.mu.Unlock()
	c.tellResume(resumeID)

	readErr := make(chan error, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, readBufferBytes)
		var scan inputScanner
		typed := false
		report := func(err error) {
			if err != nil && !errors.Is(err, io.EOF) {
				readErr <- err
			}
		}
		for {
			if a.InputGuard != nil {
				if err := a.InputGuard(); err != nil {
					report(err)
					return
				}
			}
			n, err := conn.Read(buf)
			if a.InputGuard != nil {
				if guardErr := a.InputGuard(); guardErr != nil {
					report(guardErr)
					return
				}
			}
			if c.isClosed() {
				return
			}
			if n > 0 && s.clientWritable(c) {
				accept := func() error {
					return s.writeClientStdinContext(ctx, c, buf[:n])
				}
				if a.InputAdmission != nil {
					if admissionErr := a.InputAdmission(accept); admissionErr != nil {
						report(admissionErr)
						return
					}
				} else if writeErr := accept(); writeErr != nil {
					report(writeErr)
					return
				}
				if !typed && scan.typed(buf[:n]) {
					typed = true
					h.reportInput(key, a.Member)
				}
			}
			if err != nil {
				report(err)
				return
			}
		}
	}()
	writeDone := make(chan error, 1)
	go func() { writeDone <- c.writeLoop() }()

	for {
		select {
		case <-ctx.Done():
			// Do not close conn here: the handler must send a reason-specific
			// exit status before it owns the final channel closure. The
			// handler's close then unblocks a transport read still in flight.
			c.close(ctx.Err())
			return ctx.Err()
		case <-readDone:
			select {
			case readErr := <-readErr:
				c.close(readErr)
				return readErr
			default:
				c.close(nil)
				return nil
			}
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

func (s *session) clientWritable(c *client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && !s.ended && !c.readOnly
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

// SessionGeneration identifies the current process stored at key. It is
// immutable for that process, so Attach can reject a reservation that raced a
// replacement under the same public session key.
func (h *Host) SessionGeneration(key SessionKey) uint64 {
	h.mu.Lock()
	s := h.sessions[key]
	h.mu.Unlock()
	if s == nil {
		return 0
	}
	return s.generation
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
		_ = s.stop()
	}
}
