package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// sshBackend proxies the Backend surface onto one SSH connection to the
// linked server, dialed lazily on first use. Control-channel calls share
// one protocol.Client under a mutex; streaming subsystems each open a
// fresh channel on the same connection.
type sshBackend struct {
	cfg cli.Config

	mu     sync.Mutex
	conn   *cli.Conn
	client *protocol.Client
}

// NewSSHBackend returns a Backend that dials cfg lazily on first use and
// redials once when the connection has gone away under a call.
func NewSSHBackend(cfg cli.Config) Backend {
	return &sshBackend{cfg: cfg}
}

// connect returns the live connection, dialing when there is none yet.
// The caller holds b.mu.
func (b *sshBackend) connect() (*cli.Conn, error) {
	if b.conn != nil {
		return b.conn, nil
	}
	conn, err := cli.Dial(b.cfg)
	if err != nil {
		return nil, err
	}
	client, err := conn.Control()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	b.conn, b.client = conn, client
	return conn, nil
}

// live returns the shared connection, dialing when there is none yet.
func (b *sshBackend) live() (*cli.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connect()
}

// dropLocked discards the current connection so the next use redials.
// The caller holds b.mu.
func (b *sshBackend) dropLocked() {
	if b.client != nil {
		_ = b.client.Close()
		b.client = nil
	}
	if b.conn != nil {
		_ = b.conn.Close()
		b.conn = nil
	}
}

// invalidate discards conn if it is still the shared one; a concurrent
// caller may already have replaced it.
func (b *sshBackend) invalidate(conn *cli.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == conn {
		b.dropLocked()
	}
}

// alive probes the SSH connection with a keepalive request. It separates
// "the server answered and refused" (connection fine, surface the
// refusal) from "the connection is gone" (redial) - tearing down a
// healthy connection would kill every live stream riding on it.
func alive(conn *cli.Conn) bool {
	_, _, err := conn.SSH().SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// Call performs one control-channel call. A server-reported failure comes
// back as that *protocol.Error; a transport failure triggers one redial
// and one retry before surfacing as CodeUnavailable.
func (b *sshBackend) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, err := b.callLocked(method, params)
	if err == nil {
		return result, nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return nil, perr
	}
	// Transport failure: the connection is stale (server restart, network
	// drop). Redial once and retry once.
	b.dropLocked()
	result, err = b.callLocked(method, params)
	if err == nil {
		return result, nil
	}
	if errors.As(err, &perr) {
		return nil, perr
	}
	return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "server unreachable: " + err.Error()}
}

// callLocked runs one call on the shared client, dialing when needed. The
// caller holds b.mu.
func (b *sshBackend) callLocked(method string, params json.RawMessage) (json.RawMessage, error) {
	if _, err := b.connect(); err != nil {
		return nil, err
	}
	var callParams any
	if len(params) > 0 {
		callParams = params
	}
	var result json.RawMessage
	if err := b.client.Call(method, callParams, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// stream opens one subsystem channel on the shared connection, dialing
// when needed. When opening fails on a connection that no longer answers
// a keepalive, it redials once and retries once; a failure the server
// answered (an ack refusal) passes through untouched.
func stream[T any](b *sshBackend, open func(*cli.Conn) (T, error)) (T, error) {
	conn, err := b.live()
	if err != nil {
		var zero T
		return zero, err
	}
	out, oerr := open(conn)
	if oerr == nil || alive(conn) {
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

func (b *sshBackend) Shell(req protocol.WorkspaceShellRequest) (cli.Terminal, protocol.WorkspaceShellResponse, error) {
	type shellResult struct {
		term cli.Terminal
		ack  protocol.WorkspaceShellResponse
	}
	out, err := stream(b, func(c *cli.Conn) (shellResult, error) {
		term, ack, err := c.WorkspaceShellStream(req)
		return shellResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.Sync(runID, force) })
}
