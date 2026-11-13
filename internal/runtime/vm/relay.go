package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
)

// relayDialTimeout bounds reaching a served socket through the helper.
const relayDialTimeout = 10 * time.Second

// relay listens on a host socket for a socket the helper serves in a VM.
// It listens only once the VM's socket accepts, so the host socket appearing
// means what it does for a container's bind-mounted one: the server in the
// VM is up.
type relay struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	ln     net.Listener
	// err is why the host socket could not be made once the VM's was up.
	err error
}

// relay replaces key's relay with one from hostPath to port; a VM has one.
// It tries the host socket at once, so a path that cannot take one fails the
// caller rather than its wait.
func (e *engine) relay(key string, port uint32, hostPath string) error {
	e.stopRelay(key)
	_ = os.Remove(hostPath)
	probe, err := net.Listen("unix", hostPath)
	if err != nil {
		return fmt.Errorf("vm runtime: listen for the socket of VM %s: %w", key, err)
	}
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	r := &relay{cancel: cancel, done: make(chan struct{})}
	e.mu.Lock()
	e.relays[key] = r
	e.mu.Unlock()
	vsock := e.path(key, vsockSocket)
	go func() {
		defer close(r.done)
		if !awaitServed(ctx, vsock, port) {
			return
		}
		_ = os.Remove(hostPath)
		ln, err := net.Listen("unix", hostPath)
		r.mu.Lock()
		r.ln, r.err = ln, err
		r.mu.Unlock()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			_ = ln.Close()
			return
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go relayConn(ctx, conn, vsock, port)
		}
	}()
	return nil
}

// relayErr is why key's relay has no host socket, if it failed to make one.
func (e *engine) relayErr(key string) error {
	e.mu.Lock()
	r := e.relays[key]
	e.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return fmt.Errorf("vm runtime: listen for the socket of VM %s: %w", key, r.err)
	}
	return nil
}

func awaitServed(ctx context.Context, vsock string, port uint32) bool {
	for {
		dialCtx, cancel := context.WithTimeout(ctx, relayDialTimeout)
		conn, err := guest.DialServed(dialCtx, vsock, port)
		cancel()
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func relayConn(ctx context.Context, conn net.Conn, vsock string, port uint32) {
	defer func() { _ = conn.Close() }()
	dialCtx, cancel := context.WithTimeout(ctx, relayDialTimeout)
	vm, err := guest.DialServed(dialCtx, vsock, port)
	cancel()
	if err != nil {
		return
	}
	defer func() { _ = vm.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = vm.Close() })
	defer stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); halfCopy(vm, conn) }()
	go func() { defer wg.Done(); halfCopy(conn, vm) }()
	wg.Wait()
}

// halfCopy copies src to dst and then closes dst for writing, so each side
// sees the other's end of input.
func halfCopy(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if hc, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = hc.CloseWrite()
	} else {
		_ = dst.Close()
	}
}

// close stops the relay and removes its host socket.
func (r *relay) close() {
	r.cancel()
	r.mu.Lock()
	if r.ln != nil {
		_ = r.ln.Close()
	}
	r.mu.Unlock()
	<-r.done
}

// stopRelay closes and forgets key's relay, if it has one.
func (e *engine) stopRelay(key string) {
	e.mu.Lock()
	r := e.relays[key]
	delete(e.relays, key)
	e.mu.Unlock()
	if r != nil {
		r.close()
	}
}
