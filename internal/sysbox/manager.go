package sysbox

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// logLimit is how much installer output the manager keeps, from the end,
// where the failure is.
const logLimit = 8 << 10

const notRoot = "the server does not run as root; on the host run: sudo aether-server sysbox install"

// Manager runs the installer in the background for the control channel, one
// install at a time, and remembers how the last one ended.
type Manager struct {
	// Recheck runs after a successful install so the server picks the new
	// runtime for its next container.
	Recheck func()

	root    func() bool
	status  func(context.Context) (string, error)
	install func(context.Context, *tail) error

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// broken is set by an install that left a registered runtime failing
	// the bind-mount probe. It is atomic because the Docker runtime reads
	// it while holding its own lock.
	broken atomic.Bool
	busy   atomic.Bool

	mu     sync.Mutex
	state  string
	reason string
	log    *tail
}

// NewManager returns a manager that installs through the embedded script.
func NewManager() *Manager {
	m := &Manager{
		root:   func() bool { return os.Geteuid() == 0 },
		status: Status,
		install: func(ctx context.Context, out *tail) error {
			return Install(ctx, out)
		},
		log: &tail{},
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

func (m *Manager) Start(context.Context) error { return nil }

// Close stops a running install and waits for it.
func (m *Manager) Close() error {
	m.cancel()
	m.wg.Wait()
	return nil
}

// Status reports the daemon's runtimes, or the install in progress or the
// last one's outcome while Sysbox is still missing.
func (m *Manager) Status(ctx context.Context) protocol.SysboxStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(ctx)
}

// statusLocked ranks what it knows: an install in progress, then Docker
// listing sysbox-runc, then how the last install ended, and only then a
// plain off. An error from Docker must not hide the last install's outcome.
func (m *Manager) statusLocked(ctx context.Context) protocol.SysboxStatus {
	if m.state == protocol.SysboxInstalling {
		return m.snapshot()
	}
	if m.broken.Load() {
		return m.snapshot()
	}
	state, err := m.status(ctx)
	switch {
	case err == nil && state == protocol.SysboxOn:
		return protocol.SysboxStatus{State: protocol.SysboxOn, Log: m.log.String()}
	case m.state == protocol.SysboxUnsupported || m.state == protocol.SysboxFailed:
		return m.snapshot()
	case err != nil:
		return protocol.SysboxStatus{State: protocol.SysboxOff, Reason: err.Error(), Log: m.log.String()}
	case !m.root():
		return protocol.SysboxStatus{State: protocol.SysboxOff, Reason: notRoot}
	}
	return protocol.SysboxStatus{State: protocol.SysboxOff}
}

// Unusable reports whether new containers must not use a registered Sysbox:
// an install is still checking it, or the last one saw it fail its probe.
func (m *Manager) Unusable() bool { return m.busy.Load() || m.broken.Load() }

// Installing reports whether an install is running, which holds back a
// server self-update: a restart would cut it off.
func (m *Manager) Installing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == protocol.SysboxInstalling
}

func (m *Manager) snapshot() protocol.SysboxStatus {
	return protocol.SysboxStatus{State: m.state, Reason: m.reason, Log: m.log.String()}
}

// Install starts the installer unless Sysbox is on, the server cannot run it,
// or one is already running, and returns the status either way.
func (m *Manager) Install(ctx context.Context) protocol.SysboxStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.statusLocked(ctx); st.State == protocol.SysboxOn || st.State == protocol.SysboxInstalling || !m.root() {
		return st
	}
	m.state, m.reason, m.log = protocol.SysboxInstalling, "", &tail{}
	m.busy.Store(true)
	log := m.log
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := m.install(m.ctx, log)
		m.finish(err)
	}()
	return m.snapshot()
}

func (m *Manager) finish(err error) {
	var unsupported *UnsupportedError
	var broken *BrokenError
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.busy.Store(false)
	if m.Recheck != nil {
		defer m.Recheck()
	}
	switch {
	case err == nil:
		m.broken.Store(false)
		m.state, m.reason = protocol.SysboxOn, ""
	case errors.As(err, &broken):
		m.broken.Store(true)
		m.state, m.reason = protocol.SysboxFailed, broken.Reason
	case errors.As(err, &unsupported):
		m.state, m.reason = protocol.SysboxUnsupported, unsupported.Reason
	default:
		m.state, m.reason = protocol.SysboxFailed, err.Error()
	}
}

// tail keeps the last logLimit bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > logLimit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-logLimit:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
