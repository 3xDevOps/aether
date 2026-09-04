package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// sshBackend proxies the Backend surface onto one SSH connection to the
// linked server, dialed lazily on first use. Every call and stream opens
// its own channel on that connection, so no request head-of-line blocks
// another; the mutex guards only connection (re)establishment.
type sshBackend struct {
	cfg cli.Config

	mu   sync.Mutex
	conn *cli.Conn
}

// NewSSHBackend returns a Backend that dials cfg lazily on first use and
// redials once when the connection has gone away under a call.
func NewSSHBackend(cfg cli.Config) Backend {
	return &sshBackend{cfg: cfg}
}

// Close releases the shared SSH connection, if it has been opened.
func (b *sshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	conn := b.conn
	b.conn = nil
	return conn.Close()
}

// live returns the shared connection, dialing when there is none yet. A
// dial failure comes back already classified so every surface that dials
// (Call, Events, Attach, Terminal, Sync) reports it identically.
func (b *sshBackend) live() (*cli.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		return b.conn, nil
	}
	conn, err := cli.Dial(b.cfg)
	if err != nil {
		return nil, unreachableError(err)
	}
	b.conn = conn
	return conn, nil
}

// invalidate discards conn if it is still the shared one. The pointer
// comparison means a concurrent redial is not repeated and a connection
// that is no longer the cached one is never closed - other live streams
// may still be riding on its replacement.
func (b *sshBackend) invalidate(conn *cli.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == conn {
		_ = conn.Close()
		b.conn = nil
	}
}

// unreachableError classifies a transport failure for the SPA, which
// routes on the message prefix: "network unreachable" tells the user to
// fix their own connection, "server unreachable" tells them to check the
// server. Only unambiguous local failures (DNS, no route, interface down)
// earn the first. A refused connection or a timeout cannot distinguish a
// down server from a firewall or a dropped link, and a wrong guess sends
// the user to fix the wrong thing, so ambiguity stays "server
// unreachable". Both keep CodeUnavailable, which the API maps to 503.
func unreachableError(err error) *protocol.Error {
	prefix := "server unreachable: "
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.ENETDOWN) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) {
		prefix = "network unreachable: "
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: prefix + err.Error()}
}

// callTimeout bounds one control round-trip end to end. A black-holed TCP
// connection (laptop suspend/resume, network switch) never errors on its
// own; the watchdog turns that silent wedge into a retryable
// CodeUnavailable instead of pinning the caller until kernel TCP timeout.
const callTimeout = 60 * time.Second

// errWedged reports a control call that outlived the watchdog; the
// connection it ran on is presumed dead.
var errWedged = errors.New("control call timed out")

// roundTrip runs one call on a dedicated control channel, honoring ctx.
// The round-trip runs in its own goroutine; cancellation and the watchdog
// close the channel, which unblocks the blocked read, so the caller never
// waits on unbounded I/O. The channel is always closed on return.
func roundTrip(ctx context.Context, client *protocol.Client, method string, params json.RawMessage) (json.RawMessage, error) {
	var callParams any
	if len(params) > 0 {
		callParams = params
	}
	type outcome struct {
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		var result json.RawMessage
		err := client.Call(method, callParams, &result)
		done <- outcome{result: result, err: err}
	}()
	watchdog := time.NewTimer(callTimeout)
	defer watchdog.Stop()
	select {
	case out := <-done:
		_ = client.Close()
		return out.result, out.err
	case <-ctx.Done():
		_ = client.Close()
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "request cancelled"}
	case <-watchdog.C:
		_ = client.Close()
		return nil, errWedged
	}
}

// callOnce runs one call on a fresh control channel of the shared
// connection. Transport failures and wedges drop the connection so the
// next attempt redials; server refusals and cancellations leave it alone.
func (b *sshBackend) callOnce(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	conn, err := b.live()
	if err != nil {
		return nil, err
	}
	client, err := conn.Control()
	if err != nil {
		b.invalidate(conn)
		return nil, err
	}
	result, err := roundTrip(ctx, client, method, params)
	if err == nil {
		return result, nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		// The server answered and refused, or the caller cancelled;
		// the connection is fine either way.
		return nil, err
	}
	b.invalidate(conn)
	if errors.Is(err, errWedged) {
		// Coded so Call does not retry: a second 60s wait on a wedged
		// path would double the worst case for nothing. A wedge is
		// ambiguous, so it classifies as "server unreachable".
		return nil, unreachableError(err)
	}
	return nil, err
}

// Call performs one control call on its own channel. A server-reported
// failure comes back as that *protocol.Error; a transport failure
// triggers one redial and one retry before surfacing as CodeUnavailable.
func (b *sshBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	result, err := b.callOnce(ctx, method, params)
	if err == nil {
		return result, nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return nil, perr
	}
	// Transport failure: the connection was stale (server restart,
	// network drop) and callOnce already dropped it. Redial once and
	// retry once.
	result, err = b.callOnce(ctx, method, params)
	if err == nil {
		return result, nil
	}
	if errors.As(err, &perr) {
		return nil, perr
	}
	return nil, unreachableError(err)
}

// alive probes the SSH connection with a keepalive request. It separates
// "the server answered and refused" (connection fine, surface the
// refusal) from "the connection is gone" (redial) - tearing down a
// healthy connection would kill every live stream riding on it.
func alive(conn *cli.Conn) bool {
	_, _, err := conn.SSH().SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// stream opens one subsystem channel on the shared connection, dialing
// when needed. A failure the server answered - a *protocol.Error, or any
// error on a connection that still answers a keepalive (attach/sync ack
// refusals are untyped) - passes through untouched. Only a dead connection
// is invalidated (a no-op when a concurrent caller already replaced it; a
// conn that is no longer the cached one is never closed), then redialed once
// and the open retried once.
func stream[T any](b *sshBackend, open func(*cli.Conn) (T, error)) (T, error) {
	conn, err := b.live()
	if err != nil {
		var zero T
		return zero, err
	}
	out, oerr := open(conn)
	var perr *protocol.Error
	if oerr == nil || errors.As(oerr, &perr) || alive(conn) {
		return out, oerr
	}
	b.invalidate(conn)
	conn, err = b.live()
	if err != nil {
		var zero T
		return zero, err
	}
	return open(conn)
}

func (b *sshBackend) Events(req protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.EventsStream(req) })
}

func (b *sshBackend) Attach(req protocol.AttachRequest) (cli.Terminal, protocol.AttachResponse, error) {
	type attachResult struct {
		term cli.Terminal
		ack  protocol.AttachResponse
	}
	out, err := stream(b, func(c *cli.Conn) (attachResult, error) {
		term, ack, err := c.AttachStream(req)
		return attachResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Terminal(req protocol.TerminalRequest) (cli.Terminal, protocol.TerminalResponse, error) {
	type terminalResult struct {
		term cli.Terminal
		ack  protocol.TerminalResponse
	}
	out, err := stream(b, func(c *cli.Conn) (terminalResult, error) {
		term, ack, err := c.TerminalStream(req)
		return terminalResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.Sync(runID, force) })
}
