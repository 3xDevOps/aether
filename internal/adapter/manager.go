package adapter

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// RunLookup resolves run rows; satisfied by store.Store.
type RunLookup interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	// ListActiveRuns returns runs whose status is non-terminal.
	ListActiveRuns(ctx context.Context) ([]*domain.Run, error)
}

// OutputTap opens read-only PTY output streams; satisfied by
// *ptyhost.Host.
type OutputTap interface {
	TapOutput(run domain.RunID) (io.ReadCloser, error)
}

// Manager attaches adapters to running headless runs. It subscribes to
// run-status events on the bus; when a headless run whose harness has an
// adapter enters running, it taps the run's PTY output and pumps it
// through a LineNormalizer into the adapter, publishing the resulting
// typed payloads on the bus under the run's workspace and run IDs. Runs
// without an adapter (or non-headless runs) are left alone: layer-1
// observability (PTY + diffs) is always sufficient.
type Manager struct {
	bus  events.Bus
	runs RunLookup
	pty  OutputTap

	mu     sync.Mutex
	active map[domain.RunID]io.Closer
	closed bool

	sub events.Subscription
	wg  sync.WaitGroup
}

// NewManager builds a Manager; call Start to begin consuming events.
func NewManager(bus events.Bus, runs RunLookup, pty OutputTap) *Manager {
	return &Manager{bus: bus, runs: runs, pty: pty, active: make(map[domain.RunID]io.Closer)}
}

// Start subscribes to run-status events, attaches taps to already-running
// headless runs (server-restart recovery reattaches sessions without
// publishing a running transition), and begins consuming. ctx bounds only
// the setup; the manager runs until Close.
func (m *Manager) Start(ctx context.Context) error {
	sub, err := m.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunStatus}},
	})
	if err != nil {
		return err
	}
	m.sub = sub
	m.scan(ctx)
	m.wg.Add(1)
	go m.loop()
	return nil
}

// scan attaches taps to every currently running run; attach dedups and
// filters mode/harness itself. Used at startup and after subscription
// drops, so a lost running transition is never permanent.
func (m *Manager) scan(ctx context.Context) {
	runs, err := m.runs.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("adapter: scan active runs failed", "error", err)
		return
	}
	for _, r := range runs {
		if r.Status == domain.RunRunning {
			m.attach(r.WorkspaceID, r.ID)
		}
	}
}

// Close detaches every tap, closes the subscription, and waits for the
// pumps to finish. Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	taps := make([]io.Closer, 0, len(m.active))
	for _, t := range m.active {
		taps = append(taps, t)
	}
	m.mu.Unlock()
	if m.sub != nil {
		_ = m.sub.Close()
	}
	for _, t := range taps {
		_ = t.Close()
	}
	m.wg.Wait()
	return nil
}

func (m *Manager) loop() {
	defer m.wg.Done()
	var dropped uint64
	for e := range m.sub.Events() {
		if d := m.sub.Dropped(); d > dropped {
			dropped = d
			// Buffer overflow may have discarded a running transition;
			// recover it from the store.
			m.scan(context.Background())
		}
		p, ok := e.Payload.(events.RunStatusPayload)
		if !ok || p.To != domain.RunRunning {
			continue
		}
		m.attach(e.WorkspaceID, e.RunID)
	}
}

// attach taps a run entering running, once: a run cycling through
// needs-attention and back keeps its original tap (the PTY session, and
// with it the tap, lives across that transition).
func (m *Manager) attach(workspace domain.WorkspaceID, run domain.RunID) {
	m.mu.Lock()
	if m.closed || m.active[run] != nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	ctx := context.Background()
	r, err := m.runs.GetRun(ctx, run)
	if err != nil {
		slog.Warn("adapter: resolve run failed", "run", run, "error", err)
		return
	}
	if r.Mode != domain.LaunchHeadless {
		return
	}
	a, ok := ForHarness(r.Harness)
	if !ok {
		return // no adapter for this harness: graceful degradation
	}
	tap, err := m.pty.TapOutput(run)
	if err != nil {
		slog.Warn("adapter: tap pty output failed", "run", run, "error", err)
		return
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = tap.Close()
		return
	}
	m.active[run] = tap
	m.mu.Unlock()

	m.wg.Add(1)
	go m.pump(workspace, run, a, tap)
}

// pump reads the tap to EOF (the PTY session ending), normalizing chunks
// into lines and publishing what the adapter makes of them.
func (m *Manager) pump(workspace domain.WorkspaceID, run domain.RunID, a Adapter, tap io.ReadCloser) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.active, run)
		m.mu.Unlock()
		_ = tap.Close()
	}()
	var norm LineNormalizer
	buf := make([]byte, 32*1024)
	for {
		n, err := tap.Read(buf)
		if n > 0 {
			for _, line := range norm.Feed(buf[:n]) {
				m.publish(workspace, run, a.ConsumeLine(line))
			}
		}
		if err != nil {
			// The final line of a run may lack a terminator.
			if line, ok := norm.Flush(); ok {
				m.publish(workspace, run, a.ConsumeLine(line))
			}
			return
		}
	}
}

func (m *Manager) publish(workspace domain.WorkspaceID, run domain.RunID, payloads []events.Payload) {
	for _, p := range payloads {
		e := events.Event{WorkspaceID: workspace, RunID: run, Payload: p}
		if _, err := m.bus.Publish(context.Background(), e); err != nil {
			slog.Warn("adapter: publish event failed",
				"type", p.EventType(), "run", run, "error", err)
		}
	}
}
