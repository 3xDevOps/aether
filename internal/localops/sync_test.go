package localops

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/overlay"
)

// stubSession fakes the overlay engine: Run blocks until the context ends
// or the test injects an outcome through runErr.
type stubSession struct {
	dial     overlay.Dialer
	runErr   chan error
	started  chan string
	startErr error
	closed   chan struct{}
}

func newStubSession() *stubSession {
	return &stubSession{
		runErr:  make(chan error, 1),
		started: make(chan string, 1),
		closed:  make(chan struct{}),
	}
}

func (s *stubSession) Start(_ context.Context, runID string) error {
	if s.startErr != nil {
		return s.startErr
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

// statusOf finds one run's session in a status snapshot.
func statusOf(t *testing.T, m *SyncManager, runID string) SyncSession {
	t.Helper()
	for _, s := range m.Status() {
		if s.RunID == runID {
			return s
		}
	}
	t.Fatalf("run %s not in status %v", runID, m.Status())
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
	if got := statusOf(t, m, "run_1"); got.State != SyncRunning || got.Conflict != nil {
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
	if got := statusOf(t, m, "run_1"); got.State != SyncStopped || got.Conflict != nil {
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

	if err := m.Stop("run_1"); err != nil {
		t.Fatalf("Stop after conflict: %v", err)
	}
	got := statusOf(t, m, "run_1")
	if got.State != SyncConflict || got.Conflict == nil {
		t.Fatalf("conflict status = %+v", got)
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
		if got := statusOf(t, m, "run_1"); got.State == SyncError {
			if got.Conflict == nil || *got.Conflict != "session disappeared" {
				t.Fatalf("error status = %+v", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("state never became error: %+v", statusOf(t, m, "run_1"))
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
