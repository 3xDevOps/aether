package localops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/overlay"
)

// Sync session states reported by SyncManager.Status. A session is
// "starting" from the moment sync.start claims the single-session slot
// until the overlay's initial connect finishes, then "running" while the
// overlay loop drives. Terminal states: "stopped" (clean stop or cancel),
// "error", and "conflict" (paused on a sync conflict). Stopped/error
// entries linger for observability and are swept on a later Start once
// older than syncSweepAfter; a conflict is standing user-facing state and
// persists until Stop dismisses it or a new Start for the same run
// replaces it.
const (
	SyncStarting = "starting"
	SyncRunning  = "running"
	SyncStopped  = "stopped"
	SyncConflict = "conflict"
	SyncError    = "error"
)

// Terminal-entry retention: on Start, stopped/error entries older than
// syncSweepAfter are dropped, and at most syncMaxTerminal ended entries
// are retained overall - oldest evicted first, conflicts last.
const (
	syncSweepAfter  = 5 * time.Minute
	syncMaxTerminal = 16
)

// SyncSession is one run's overlay state for sync.status.
type SyncSession struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
	// Conflict describes a paused-on-conflict session; nil otherwise.
	Conflict *string `json:"conflict"`
}

// OverlaySession is the slice of *overlay.Session the manager drives,
// separated so tests can stub the heavyweight mutagen engine.
type OverlaySession interface {
	Start(ctx context.Context, runID string) error
	Run(ctx context.Context) error
	Close()
}

// newOverlaySession builds the real overlay session; tests replace it
// through NewSession.
func newOverlaySession(localDir string, dial overlay.Dialer) (OverlaySession, error) {
	return overlay.NewSession(overlay.Options{LocalDir: localDir, Dial: dial})
}

// syncEntry is one run's overlay: the cancel ends the session's start
// attempt or Run loop, done closes when the session has fully unwound
// (closed, final state recorded), endedAt is when a terminal state was
// recorded (zero while starting/running).
type syncEntry struct {
	cancel   context.CancelFunc
	done     chan struct{}
	state    string
	conflict *string
	endedAt  time.Time
}

// SyncManager owns background live-overlay sessions, one per run, for the
// local gateway's sync.start/stop/status verbs. The overlay engine is
// process-global (one protocol handler registry, one data directory), so
// at most one session may be starting or running at a time; earlier
// sessions must stop, conflict, or fail before the next starts.
type SyncManager struct {
	mu       sync.Mutex
	sessions map[string]*syncEntry
	// NewSession builds one overlay session; the default wraps
	// overlay.NewSession. Tests substitute a stub - mutagen is
	// process-global and far too heavy for handler tests.
	NewSession func(localDir string, dial overlay.Dialer) (OverlaySession, error)
}

// NewSyncManager returns an empty manager.
func NewSyncManager() *SyncManager {
	return &SyncManager{
		sessions:   make(map[string]*syncEntry),
		NewSession: newOverlaySession,
	}
}

// sweepLocked bounds the session map for a long-lived gateway process.
// Stopped/error entries older than syncSweepAfter go immediately; then,
// if more than syncMaxTerminal ended entries remain, the oldest are
// evicted - conflicts last, since a conflict is standing user-facing
// state the user still needs to see. Callers hold m.mu.
func (m *SyncManager) sweepLocked(now time.Time) {
	type ended struct {
		id       string
		endedAt  time.Time
		conflict bool
	}
	var terminal []ended
	for id, e := range m.sessions {
		switch e.state {
		case SyncStopped, SyncError:
			if now.Sub(e.endedAt) > syncSweepAfter {
				delete(m.sessions, id)
				continue
			}
			terminal = append(terminal, ended{id, e.endedAt, false})
		case SyncConflict:
			terminal = append(terminal, ended{id, e.endedAt, true})
		}
	}
	if len(terminal) <= syncMaxTerminal {
		return
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].conflict != terminal[j].conflict {
			return !terminal[i].conflict
		}
		return terminal[i].endedAt.Before(terminal[j].endedAt)
	})
	for _, t := range terminal[:len(terminal)-syncMaxTerminal] {
		delete(m.sessions, t.id)
	}
}

// Start launches a background overlay between localDir and the run's
// worktree. dial opens the sync stream (fresh stream per connection
// attempt); onConflict, when non-nil, is called once if the session
// pauses on a conflict, after the state is recorded. Start fails when the
// run already has a session starting or running, or any other session is;
// Stop on a still-starting session cancels the start attempt.
func (m *SyncManager) Start(localDir, runID string, force bool, dial func(runID string, force bool) (io.ReadWriteCloser, error), onConflict func(runID string, c *overlay.Conflict)) error {
	m.mu.Lock()
	m.sweepLocked(time.Now())
	for id, e := range m.sessions {
		if e.state != SyncStarting && e.state != SyncRunning {
			continue
		}
		state := e.state
		m.mu.Unlock()
		if id == runID {
			return fmt.Errorf("localops: sync for run %s is already %s", runID, state)
		}
		return fmt.Errorf("localops: a sync for run %s is already %s; stop it first", id, state)
	}

	// Claim the single-session slot with a cancellable placeholder before
	// building the session: overlay construction and Start dial the run's
	// worktree over the network, far too slow to sit under m.mu.
	ctx, cancel := context.WithCancel(context.Background())
	entry := &syncEntry{cancel: cancel, done: make(chan struct{}), state: SyncStarting}
	m.sessions[runID] = entry
	m.mu.Unlock()

	sess, err := m.NewSession(localDir, func(context.Context) (io.ReadWriteCloser, error) {
		return dial(runID, force)
	})
	if err == nil {
		if err = sess.Start(ctx, runID); err != nil {
			sess.Close()
		}
	}
	if err != nil {
		cancel()
		m.mu.Lock()
		delete(m.sessions, runID)
		m.mu.Unlock()
		close(entry.done)
		return err
	}

	m.mu.Lock()
	if ctx.Err() != nil {
		// Stop raced the start attempt: the session came up but is
		// unwanted. Tear it down and report the cancellation.
		entry.state = SyncStopped
		entry.endedAt = time.Now()
		m.mu.Unlock()
		sess.Close()
		cancel()
		close(entry.done)
		return fmt.Errorf("localops: sync for run %s was stopped while starting", runID)
	}
	entry.state = SyncRunning
	m.mu.Unlock()

	go func() {
		defer close(entry.done)
		runErr := sess.Run(ctx)
		canceled := ctx.Err() != nil
		cancel()
		sess.Close()

		state, detail := SyncStopped, (*string)(nil)
		var conflict *overlay.Conflict
		switch {
		case errors.As(runErr, &conflict):
			state = SyncConflict
			text := conflict.Error()
			detail = &text
		case runErr != nil && !canceled:
			state = SyncError
			text := runErr.Error()
			detail = &text
		}
		m.mu.Lock()
		entry.state, entry.conflict = state, detail
		entry.endedAt = time.Now()
		m.mu.Unlock()
		if conflict != nil && onConflict != nil {
			onConflict(runID, conflict)
		}
	}()
	return nil
}

// Stop ends the run's overlay and waits for its teardown; on a session
// still starting it cancels the start attempt. Stopping a session that
// already ended (conflict, error, clean stop) is not an error; an unknown
// run is. A standing conflict is dismissed: its entry leaves the map.
func (m *SyncManager) Stop(runID string) error {
	m.mu.Lock()
	entry, ok := m.sessions[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("localops: no sync session for run %s", runID)
	}
	entry.cancel()
	<-entry.done
	m.mu.Lock()
	if cur := m.sessions[runID]; cur == entry && entry.state == SyncConflict {
		delete(m.sessions, runID)
	}
	m.mu.Unlock()
	return nil
}

// Status snapshots every session this manager has started; callers sort
// if they care about order.
func (m *SyncManager) Status() []SyncSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SyncSession, 0, len(m.sessions))
	for id, e := range m.sessions {
		out = append(out, SyncSession{RunID: id, State: e.state, Conflict: e.conflict})
	}
	return out
}
