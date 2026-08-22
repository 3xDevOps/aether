package localops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/3xDevOps/Aether/internal/overlay"
)

// Sync session states reported by SyncManager.Status.
const (
	SyncRunning  = "running"
	SyncStopped  = "stopped"
	SyncConflict = "conflict"
	SyncError    = "error"
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

// syncEntry is one run's overlay: the cancel ends the session's Run loop,
// done closes when the driver goroutine has fully unwound (session
// closed, final state recorded).
type syncEntry struct {
	cancel   context.CancelFunc
	done     chan struct{}
	state    string
	conflict *string
}

// SyncManager owns background live-overlay sessions, one per run, for the
// local gateway's sync.start/stop/status verbs. The overlay engine is
// process-global (one protocol handler registry, one data directory), so
// at most one session may be running at a time; earlier sessions must
// stop, conflict, or fail before the next starts.
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

// Start launches a background overlay between localDir and the run's
// worktree. dial opens the sync stream (fresh stream per connection
// attempt); onConflict, when non-nil, is called once if the session
// pauses on a conflict, after the state is recorded. Start fails when the
// run already has a running session or any other session is running.
func (m *SyncManager) Start(localDir, runID string, force bool, dial func(runID string, force bool) (io.ReadWriteCloser, error), onConflict func(runID string, c *overlay.Conflict)) error {
	m.mu.Lock()
	for id, e := range m.sessions {
		if e.state != SyncRunning {
			continue
		}
		m.mu.Unlock()
		if id == runID {
			return fmt.Errorf("localops: sync for run %s is already running", runID)
		}
		return fmt.Errorf("localops: a sync for run %s is already running; stop it first", id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess, err := m.NewSession(localDir, func(context.Context) (io.ReadWriteCloser, error) {
		return dial(runID, force)
	})
	if err != nil {
		cancel()
		m.mu.Unlock()
		return err
	}
	if err := sess.Start(ctx, runID); err != nil {
		sess.Close()
		cancel()
		m.mu.Unlock()
		return err
	}
	entry := &syncEntry{cancel: cancel, done: make(chan struct{}), state: SyncRunning}
	m.sessions[runID] = entry
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
		m.mu.Unlock()
		if conflict != nil && onConflict != nil {
			onConflict(runID, conflict)
		}
	}()
	return nil
}

// Stop ends the run's overlay and waits for its teardown. Stopping a
// session that already ended (conflict, error, clean stop) is not an
// error; an unknown run is.
func (m *SyncManager) Stop(runID string) error {
	m.mu.Lock()
	entry, ok := m.sessions[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("localops: no sync session for run %s", runID)
	}
	entry.cancel()
	<-entry.done
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
