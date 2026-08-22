package localops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/overlay"
)

// stubSession fakes the overlay engine: Run blocks until the context ends
// or the test injects an outcome through runErr. A non-nil startGate holds
// Start until the gate closes or the context is cancelled, pinning the
// session in the manager's "starting" state.
type stubSession struct {
	dial      overlay.Dialer
	runErr    chan error
	started   chan string
	startErr  error
	startGate chan struct{}
	closed    chan struct{}
}

func newStubSession() *stubSession {
	return &stubSession{
		runErr:  make(chan error, 1),
		started: make(chan string, 1),
		closed:  make(chan struct{}),
	}
}

func (s *stubSession) Start(ctx context.Context, runID string) error {
	if s.startErr != nil {
		return s.startErr
	}
	if s.startGate != nil {
		select {
		case <-s.startGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.started <- runID
	return nil
}

func (s *stubSession) Run(ctx context.Context) error {
	select {
	case err := <-s.runErr:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (s *stubSession) Close() { close(s.closed) }

// stubManager wires a manager to one stub session and a pipe-backed dial.
func stubManager(sess *stubSession) (*SyncManager, *int) {
	m := NewSyncManager()
	dials := 0
	m.NewSession = func(_ string, dial overlay.Dialer) (OverlaySession, error) {
		sess.dial = dial
		return sess, nil
	}
	return m, &dials
}

// pipeDial returns a dial func handing out io.Pipe halves, counting calls.
func pipeDial(count *int) func(string, bool) (io.ReadWriteCloser, error) {
	return func(string, bool) (io.ReadWriteCloser, error) {
		*count++
		r, w := io.Pipe()
		return struct {
			io.Reader
			io.Writer
			io.Closer
		}{r, w, w}, nil
	}
}

// run1Status finds run_1's session in a status snapshot.
func run1Status(t *testing.T, m *SyncManager) SyncSession {
	t.Helper()
	for _, s := range m.Status() {
		if s.RunID == "run_1" {
			return s
		}
	}
	t.Fatalf("run_1 not in status %v", m.Status())
	return SyncSession{}
}

func TestSyncManagerLifecycle(t *testing.T) {
	sess := newStubSession()
	m, dials := stubManager(sess)

	if err := m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case id := <-sess.started:
		if id != "run_1" {
			t.Fatalf("started run %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("session never started")
	}
	if got := run1Status(t, m); got.State != SyncRunning || got.Conflict != nil {
		t.Fatalf("running status = %+v", got)
	}

	// The dial closure carries runID and force through to the stream.
	if _, err := sess.dial(context.Background()); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if *dials != 1 {
		t.Fatalf("dial count = %d", *dials)
	}

	// A second session while one runs is refused, same or other run.
	if err := m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil); err == nil {
		t.Fatal("Start accepted a duplicate session")
	}
	if err := m.Start(t.TempDir(), "run_2", false, pipeDial(dials), nil); err == nil {
		t.Fatal("Start accepted a concurrent session")
	}

	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-sess.closed:
	case <-time.After(time.Second):
		t.Fatal("session never closed")
	}
	if got := run1Status(t, m); got.State != SyncStopped || got.Conflict != nil {
		t.Fatalf("stopped status = %+v", got)
	}

	if err := m.Stop("run_9"); err == nil {
		t.Fatal("Stop accepted an unknown run")
	}
	// Stopping an already-stopped session is idempotent.
	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestSyncManagerReportsConflict(t *testing.T) {
	sess := newStubSession()
	m, dials := stubManager(sess)

	reported := make(chan *overlay.Conflict, 1)
	onConflict := func(runID string, c *overlay.Conflict) {
		if runID != "run_1" {
			t.Errorf("conflict for run %q", runID)
		}
		reported <- c
	}
	if err := m.Start(t.TempDir(), "run_1", true, pipeDial(dials), onConflict); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-sess.started

	sess.runErr <- &overlay.Conflict{SessionID: "sync_1", Files: []string{"f.txt"}}
	select {
	case c := <-reported:
		if c.SessionID != "sync_1" {
			t.Fatalf("conflict = %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("conflict never reported")
	}

	got := run1Status(t, m)
	if got.State != SyncConflict || got.Conflict == nil {
		t.Fatalf("conflict status = %+v", got)
	}

	// Stop dismisses the standing conflict: the entry leaves status.
	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("Stop after conflict: %v", err)
	}
	for _, s := range m.Status() {
		if s.RunID == "run_1" {
			t.Fatalf("dismissed conflict still in status: %+v", s)
		}
	}

	// A conflicted session no longer blocks new ones.
	sess2 := newStubSession()
	m.NewSession = func(_ string, dial overlay.Dialer) (OverlaySession, error) {
		sess2.dial = dial
		return sess2, nil
	}
	if err := m.Start(t.TempDir(), "run_2", false, pipeDial(dials), nil); err != nil {
		t.Fatalf("Start after conflict: %v", err)
	}
	if err := m.Stop("run_2"); err != nil {
		t.Fatalf("Stop run_2: %v", err)
	}
}

func TestSyncManagerRecordsRunError(t *testing.T) {
	sess := newStubSession()
	m, dials := stubManager(sess)

	if err := m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-sess.started
	sess.runErr <- errors.New("session disappeared")

	deadline := time.After(time.Second)
	for {
		if got := run1Status(t, m); got.State == SyncError {
			if got.Conflict == nil || *got.Conflict != "session disappeared" {
				t.Fatalf("error status = %+v", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("state never became error: %+v", run1Status(t, m))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSyncManagerStartFailureLeavesNoSession(t *testing.T) {
	sess := newStubSession()
	sess.startErr = errors.New("no worktree")
	m, dials := stubManager(sess)

	if err := m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil); err == nil {
		t.Fatal("Start succeeded despite session start failure")
	}
	select {
	case <-sess.closed:
	default:
		t.Fatal("failed session was not closed")
	}
	if len(m.Status()) != 0 {
		t.Fatalf("status after failed start = %v", m.Status())
	}
}

// waitForState polls until the run reaches the wanted state, tolerating
// the entry not existing yet.
func waitForState(t *testing.T, m *SyncManager, runID, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		for _, s := range m.Status() {
			if s.RunID == runID && s.State == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("run %s never reached %q: %v", runID, want, m.Status())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestSyncManagerStatusReportsStarting(t *testing.T) {
	sess := newStubSession()
	sess.startGate = make(chan struct{})
	m, dials := stubManager(sess)

	startErr := make(chan error, 1)
	go func() { startErr <- m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil) }()
	waitForState(t, m, "run_1", SyncStarting)

	// A concurrent Start is refused while the first is still starting,
	// for the same run and for any other.
	if err := m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil); err == nil ||
		!strings.Contains(err.Error(), "already starting") {
		t.Fatalf("duplicate Start while starting = %v", err)
	}
	if err := m.Start(t.TempDir(), "run_2", false, pipeDial(dials), nil); err == nil ||
		!strings.Contains(err.Error(), "already starting") {
		t.Fatalf("concurrent Start while starting = %v", err)
	}

	close(sess.startGate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := run1Status(t, m); got.State != SyncRunning {
		t.Fatalf("status after gate = %+v", got)
	}
	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSyncManagerStopDuringStartingCancels(t *testing.T) {
	sess := newStubSession()
	sess.startGate = make(chan struct{}) // never closed: Start blocks until cancel
	m, dials := stubManager(sess)

	startErr := make(chan error, 1)
	go func() { startErr <- m.Start(t.TempDir(), "run_1", false, pipeDial(dials), nil) }()
	waitForState(t, m, "run_1", SyncStarting)

	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("Stop while starting: %v", err)
	}
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("cancelled Start returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("Start never returned after Stop")
	}
	select {
	case <-sess.closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled session was not closed")
	}
	for _, s := range m.Status() {
		if s.RunID == "run_1" {
			t.Fatalf("cancelled start left an entry: %+v", s)
		}
	}
}

// endedEntry fabricates a terminal map entry as the driver goroutine
// would leave it.
func endedEntry(state string, endedAt time.Time) *syncEntry {
	done := make(chan struct{})
	close(done)
	return &syncEntry{cancel: func() {}, done: done, state: state, endedAt: endedAt}
}

func TestSyncManagerSweepsStaleTerminalEntries(t *testing.T) {
	sess := newStubSession()
	m, dials := stubManager(sess)

	old := time.Now().Add(-2 * syncSweepAfter)
	conflictText := "unresolved"
	m.mu.Lock()
	m.sessions["run_old_stopped"] = endedEntry(SyncStopped, old)
	m.sessions["run_old_error"] = endedEntry(SyncError, old)
	fresh := endedEntry(SyncStopped, time.Now())
	m.sessions["run_fresh_stopped"] = fresh
	conflicted := endedEntry(SyncConflict, old)
	conflicted.conflict = &conflictText
	m.sessions["run_conflict"] = conflicted
	m.mu.Unlock()

	if err := m.Start(t.TempDir(), "run_new", false, pipeDial(dials), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("run_new") //nolint:errcheck // test cleanup

	got := map[string]string{}
	for _, s := range m.Status() {
		got[s.RunID] = s.State
	}
	if _, ok := got["run_old_stopped"]; ok {
		t.Fatal("stale stopped entry survived the sweep")
	}
	if _, ok := got["run_old_error"]; ok {
		t.Fatal("stale error entry survived the sweep")
	}
	if got["run_fresh_stopped"] != SyncStopped {
		t.Fatalf("fresh stopped entry swept: %v", got)
	}
	if got["run_conflict"] != SyncConflict {
		t.Fatalf("conflict entry swept despite age: %v", got)
	}
	if got["run_new"] != SyncRunning {
		t.Fatalf("new session state = %v", got)
	}
}

func TestSyncManagerEvictsOldestTerminalEntries(t *testing.T) {
	sess := newStubSession()
	m, dials := stubManager(sess)

	// 20 recent terminal entries (within the sweep window) exceed the
	// retention cap; the 4 oldest non-conflict entries must go. A single
	// conflict, though oldest of all, is evicted last and survives.
	base := time.Now().Add(-time.Minute)
	conflictText := "unresolved"
	m.mu.Lock()
	conflicted := endedEntry(SyncConflict, base.Add(-time.Hour))
	conflicted.conflict = &conflictText
	m.sessions["run_conflict"] = conflicted
	for i := range 19 {
		id := fmt.Sprintf("run_%02d", i)
		m.sessions[id] = endedEntry(SyncStopped, base.Add(time.Duration(i)*time.Second))
	}
	m.mu.Unlock()

	if err := m.Start(t.TempDir(), "run_new", false, pipeDial(dials), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("run_new") //nolint:errcheck // test cleanup

	got := map[string]string{}
	for _, s := range m.Status() {
		got[s.RunID] = s.State
	}
	terminal := 0
	for _, state := range got {
		if state != SyncRunning {
			terminal++
		}
	}
	if terminal != syncMaxTerminal {
		t.Fatalf("retained %d terminal entries, want %d: %v", terminal, syncMaxTerminal, got)
	}
	if got["run_conflict"] != SyncConflict {
		t.Fatalf("conflict entry evicted before stopped ones: %v", got)
	}
	// The oldest stopped entries (run_00..run_03) were evicted first.
	for i := range 4 {
		if _, ok := got[fmt.Sprintf("run_%02d", i)]; ok {
			t.Fatalf("oldest entry run_%02d survived eviction: %v", i, got)
		}
	}
	for i := 4; i < 19; i++ {
		if got[fmt.Sprintf("run_%02d", i)] != SyncStopped {
			t.Fatalf("newer entry run_%02d evicted: %v", i, got)
		}
	}
}
