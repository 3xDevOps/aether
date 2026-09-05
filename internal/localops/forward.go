package localops

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// ForwardSession is one local TCP listener forwarding to a run container.
type ForwardSession struct {
	RunID     string `json:"run_id"`
	Port      int    `json:"port"`
	LocalPort int    `json:"local_port"`
	Conns     int    `json:"conns"`
}

type forwardKey struct {
	runID string
	port  int
}

type forwardEntry struct {
	listener net.Listener
	done     chan struct{}
	conns    map[*forwardConn]struct{}
	stopped  bool
	wg       sync.WaitGroup
}

type forwardConn struct {
	local  net.Conn
	remote io.ReadWriteCloser
}

// ForwardManager owns loopback listeners and their live port-forward streams.
type ForwardManager struct {
	mu       sync.Mutex
	sessions map[forwardKey]*forwardEntry
}

// NewForwardManager returns an empty forward manager.
func NewForwardManager() *ForwardManager {
	return &ForwardManager{sessions: make(map[forwardKey]*forwardEntry)}
}

// Start listens on the requested loopback port and forwards each accepted
// connection through a fresh stream returned by dial.
func (m *ForwardManager) Start(runID string, port int, dial func() (io.ReadWriteCloser, error)) error {
	if runID == "" {
		return errors.New("localops: run_id is required")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("localops: port must be between 1 and 65535")
	}
	if dial == nil {
		return errors.New("localops: dial is required")
	}

	key := forwardKey{runID: runID, port: port}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[key]; ok {
		return fmt.Errorf("localops: forward for run %s port %d is already active", runID, port)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return fmt.Errorf("localops: listen on 127.0.0.1:%d: %w", port, err)
	}
	entry := &forwardEntry{
		listener: listener,
		done:     make(chan struct{}),
		conns:    make(map[*forwardConn]struct{}),
	}
	m.sessions[key] = entry
	go m.accept(entry, dial)
	return nil
}

func (m *ForwardManager) accept(entry *forwardEntry, dial func() (io.ReadWriteCloser, error)) {
	defer func() {
		entry.wg.Wait()
		close(entry.done)
	}()
	for {
		conn, err := entry.listener.Accept()
		if err != nil {
			return
		}
		fc := &forwardConn{local: conn}
		m.mu.Lock()
		if entry.stopped {
			m.mu.Unlock()
			_ = conn.Close()
			continue
		}
		entry.conns[fc] = struct{}{}
		entry.wg.Add(1)
		m.mu.Unlock()
		go m.forward(entry, fc, dial)
	}
}

func (m *ForwardManager) forward(entry *forwardEntry, fc *forwardConn, dial func() (io.ReadWriteCloser, error)) {
	defer entry.wg.Done()
	remote, err := dial()
	if err != nil || remote == nil {
		_ = fc.local.Close()
		m.remove(entry, fc)
		return
	}
	m.mu.Lock()
	if entry.stopped {
		m.mu.Unlock()
		_ = fc.local.Close()
		_ = remote.Close()
		m.remove(entry, fc)
		return
	}
	fc.remote = remote
	m.mu.Unlock()
	pipeForward(fc.local, remote)
	_ = fc.local.Close()
	_ = remote.Close()
	m.remove(entry, fc)
}

func (m *ForwardManager) remove(entry *forwardEntry, fc *forwardConn) {
	m.mu.Lock()
	delete(entry.conns, fc)
	m.mu.Unlock()
}

// Stop closes a listener and all of its active connections.
func (m *ForwardManager) Stop(runID string, port int) error {
	key := forwardKey{runID: runID, port: port}
	m.mu.Lock()
	entry, ok := m.sessions[key]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("localops: no forward for run %s port %d", runID, port)
	}
	entry.stopped = true
	delete(m.sessions, key)
	conns := make([]*forwardConn, 0, len(entry.conns))
	for fc := range entry.conns {
		conns = append(conns, fc)
	}
	m.mu.Unlock()

	_ = entry.listener.Close()
	for _, fc := range conns {
		_ = fc.local.Close()
		if fc.remote != nil {
			_ = fc.remote.Close()
		}
	}
	<-entry.done
	return nil
}

// Close stops every active forward.
func (m *ForwardManager) Close() {
	m.mu.Lock()
	keys := make([]forwardKey, 0, len(m.sessions))
	for key := range m.sessions {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		_ = m.Stop(key.runID, key.port)
	}
}

// Status returns a snapshot of active forwards.
func (m *ForwardManager) Status() []ForwardSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ForwardSession, 0, len(m.sessions))
	for key, entry := range m.sessions {
		out = append(out, ForwardSession{
			RunID:     key.runID,
			Port:      key.port,
			LocalPort: key.port,
			Conns:     len(entry.conns),
		})
	}
	return out
}

func pipeForward(local net.Conn, remote io.ReadWriteCloser) {
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(remote, local)
		if err == nil {
			halfClose(remote)
		}
		done <- err
	}()
	go func() {
		_, err := io.Copy(local, remote)
		if err == nil {
			halfClose(local)
		}
		done <- err
	}()
	if err := <-done; err != nil {
		_ = local.Close()
		_ = remote.Close()
	}
	if err := <-done; err != nil {
		_ = local.Close()
		_ = remote.Close()
	}
}

func halfClose(v any) {
	if c, ok := v.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
}
