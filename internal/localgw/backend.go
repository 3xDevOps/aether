package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// sshBackend proxies the Backend surface onto one lazily dialed SSH connection.
// Each call and stream opens its own channel. mu guards the fields and is
// never held across network I/O: a dial through an edge can take most of a
// minute, and every request, Relink and Close would queue behind it.
type sshBackend struct {
	mu   sync.Mutex
	cfg  cli.Config
	conn *cli.Conn
	// dialing is the dial every caller without a connection waits on.
	dialing *dialAttempt
	closed  bool
	// The keepalive's own copy, taken at construction, so a goroutine of
	// a closed backend never reads a setting a test changes.
	keepaliveInterval time.Duration
	keepaliveTimeout  time.Duration
}

// dialAttempt is one dial of cfg; conn and err are set before done closes.
type dialAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	conn   *cli.Conn
	err    error
}

// dialTimeout bounds one dial, a link's server.info call included. The
// edge path's own steps add up to under a minute; a plain address's
// handshake has no deadline of its own.
const dialTimeout = 90 * time.Second

// keepaliveInterval and keepaliveTimeout bound how long a connection whose
// network went away without a reset stays cached: a phone that slept or
// changed networks leaves one, and every call and stream opened on it waits
// for an answer that never comes. Variables so a test can shorten them.
var (
	keepaliveInterval = 15 * time.Second
	keepaliveTimeout  = 10 * time.Second
)

var (
	errClosed   = errors.New("the gateway is closing")
	errRelinked = errors.New("the gateway moved to another server while connecting")
)

// NewSSHBackend returns a Backend that redials once when the connection drops
// under a replay-safe call.
func NewSSHBackend(cfg cli.Config) Backend {
	return &sshBackend{cfg: cfg, keepaliveInterval: keepaliveInterval, keepaliveTimeout: keepaliveTimeout}
}

func (b *sshBackend) Close() error {
	b.mu.Lock()
	b.closed = true
	conn := b.swap(nil)
	b.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// Relink closes the previous connection before it returns, so a stream
// blocked on it ends.
func (b *sshBackend) Relink(cfg cli.Config, conn *cli.Conn) {
	b.mu.Lock()
	b.cfg = cfg
	old := b.swap(conn)
	b.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// swap caches conn, abandoning any dial in progress, and returns the
// connection it replaced. The caller holds mu.
func (b *sshBackend) swap(conn *cli.Conn) *cli.Conn {
	if b.dialing != nil {
		b.dialing.cancel()
		b.dialing = nil
	}
	old := b.conn
	b.conn = conn
	if conn != nil {
		go b.keepalive(conn)
	}
	return old
}

// live returns the shared connection, dialing if needed. Concurrent callers
// share one dial, and each stops waiting when its own ctx ends. Dial errors
// come back classified so every surface reports them identically.
func (b *sshBackend) live(ctx context.Context) (*cli.Conn, error) {
	b.mu.Lock()
	if b.conn != nil {
		conn := b.conn
		b.mu.Unlock()
		return conn, nil
	}
	if b.closed {
		b.mu.Unlock()
		return nil, unreachableError(errClosed)
	}
	d := b.dialing
	if d == nil {
		dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		d = &dialAttempt{done: make(chan struct{}), cancel: cancel}
		b.dialing = d
		go b.dial(dialCtx, d, b.cfg)
	}
	b.mu.Unlock()
	select {
	case <-d.done:
		if d.err != nil {
			return nil, unreachableError(d.err)
		}
		return d.conn, nil
	case <-ctx.Done():
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "request cancelled"}
	}
}

func (b *sshBackend) dial(ctx context.Context, d *dialAttempt, cfg cli.Config) {
	defer d.cancel()
	conn, err := dialLink(ctx, cfg)
	var stale *cli.Conn
	b.mu.Lock()
	if b.dialing == d {
		b.dialing = nil
		if err == nil {
			b.swap(conn)
		}
	} else {
		// Relink or Close abandoned this dial: its config is no longer
		// the one served.
		stale, conn, err = conn, nil, errRelinked
		if b.closed {
			err = errClosed
		}
	}
	d.conn, d.err = conn, err
	b.mu.Unlock()
	close(d.done)
	if stale != nil {
		_ = stale.Close()
	}
}

// keepalive pings conn while it is the cached connection and drops it once
// a ping goes unanswered, which ends every call and stream waiting on it.
func (b *sshBackend) keepalive(conn *cli.Conn) {
	tick := time.NewTicker(b.keepaliveInterval)
	defer tick.Stop()
	for range tick.C {
		b.mu.Lock()
		cached := b.conn == conn
		b.mu.Unlock()
		if !cached {
			return
		}
		if !b.alive(conn) {
			b.invalidate(conn)
			return
		}
	}
}

// invalidate closes conn only if it is still the cached one: a replaced
// connection may carry other live streams.
func (b *sshBackend) invalidate(conn *cli.Conn) {
	b.mu.Lock()
	cached := b.conn == conn
	if cached {
		b.conn = nil
	}
	b.mu.Unlock()
	if cached {
		_ = conn.Close()
	}
}

// unreachableError classifies a transport failure for the SPA, which routes on
// the message prefix. Only unambiguous local failures (DNS, no route, interface
// down) say "network unreachable"; a refused connection or a timeout could be
// either side, so it stays "server unreachable".
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
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: prefix + err.Error(), Data: refusalData(err)}
}

// callTimeout bounds one control round-trip. A black-holed TCP connection
// (suspend/resume, network switch) never errors on its own.
const callTimeout = 60 * time.Second

// transferTimeout is the server's lifetime for one large control frame: a
// 64 MiB file does not cross a slow link within callTimeout.
const transferTimeout = 15 * time.Minute

func callBound(method string) time.Duration {
	switch method {
	case protocol.MethodAgentInstall:
		return protocol.AgentInstallTimeout + callTimeout
	case protocol.MethodHomeUpload, protocol.MethodHomeDownload, protocol.MethodTerminalFile:
		return transferTimeout
	case protocol.MethodTerminalExec:
		return protocol.TerminalExecTimeout + callTimeout
	}
	return callTimeout
}

// errWedged marks a call that outlived the watchdog; its connection is presumed dead.
var errWedged = errors.New("control call timed out")

// roundTrip runs one call on its own channel. Cancellation and the watchdog
// close the channel to unblock the pending read.
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
	watchdog := time.NewTimer(callBound(method))
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

// callOnce drops the connection on transport failures and wedges so the next
// attempt redials; server refusals and cancellations leave it alone.
func (b *sshBackend) callOnce(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	conn, err := b.live(ctx)
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
		return nil, err
	}
	b.invalidate(conn)
	if errors.Is(err, errWedged) {
		// Coded so Call does not retry: a second wait on a wedged path
		// doubles the worst case.
		return nil, unreachableError(err)
	}
	return nil, err
}

// Call redials and retries once on a transport failure, except for methods
// that may have committed a mutation before the response was lost.
func (b *sshBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	result, err := b.callOnce(ctx, method, params)
	if err == nil {
		return result, nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return nil, perr
	}
	if method == protocol.MethodConfigImport || method == protocol.MethodWorkspaceImport || method == protocol.MethodAgentInstall ||
		method == protocol.MethodHomeMkdir || method == protocol.MethodHomeRename ||
		method == protocol.MethodHomeRemove || method == protocol.MethodHomeUpload ||
		method == protocol.MethodTerminalExec ||
		method == protocol.MethodAssistantPrompt || method == protocol.MethodAssistantAnswer ||
		method == protocol.MethodAssistantConfirm || method == protocol.MethodAssistantStart ||
		method == protocol.MethodAssistantReset || method == protocol.MethodServerSysboxInstall ||
		method == protocol.MethodGitHubOAuthStart || method == protocol.MethodGitHubOAuthCancel ||
		method == protocol.MethodWorkspaceMirrorConfigure || method == protocol.MethodWorkspaceMirrorRefresh ||
		method == protocol.MethodWorkspaceMirrorAdopt || method == protocol.MethodWorkspaceMirrorDisable ||
		strings.HasPrefix(method, "dev.") || strings.HasPrefix(method, "run.git.") || strings.HasPrefix(method, "run.pr.") {
		// A mutation may have committed before the response was lost;
		// do not replay it.
		return nil, unreachableError(err)
	}
	result, err = b.callOnce(ctx, method, params)
	if err == nil {
		return result, nil
	}
	if errors.As(err, &perr) {
		return nil, perr
	}
	return nil, unreachableError(err)
}

// alive tells a refusal on a healthy connection from a dead one; tearing down
// a healthy connection would kill every stream riding on it.
func (b *sshBackend) alive(conn *cli.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), b.keepaliveTimeout)
	defer cancel()
	return conn.Ping(ctx) == nil
}

// stream opens one subsystem channel. Errors the server answered pass through
// (attach/sync ack refusals are untyped, hence the keepalive probe); only a
// dead connection is invalidated, redialed and retried once.
func stream[T any](ctx context.Context, b *sshBackend, open func(*cli.Conn) (T, error)) (T, error) {
	conn, err := b.live(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	out, oerr := open(conn)
	var perr *protocol.Error
	if oerr == nil || errors.As(oerr, &perr) || b.alive(conn) {
		return out, oerr
	}
	b.invalidate(conn)
	conn, err = b.live(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return open(conn)
}

func (b *sshBackend) Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	return stream(ctx, b, func(c *cli.Conn) (io.ReadCloser, error) { return c.EventsStream(req) })
}

func (b *sshBackend) Attach(ctx context.Context, req protocol.AttachRequest) (webgate.Terminal, protocol.AttachResponse, error) {
	type attachResult struct {
		term webgate.Terminal
		ack  protocol.AttachResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (attachResult, error) {
		term, ack, err := c.AttachStream(req)
		if term == nil {
			return attachResult{ack: ack}, err
		}
		return attachResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) ACP(ctx context.Context, req protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
	type acpResult struct {
		stream io.ReadWriteCloser
		ack    protocol.ACPStreamResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (acpResult, error) {
		s, ack, err := c.ACPStream(req)
		if s == nil {
			return acpResult{ack: ack}, err
		}
		return acpResult{stream: s, ack: ack}, err
	})
	return out.stream, out.ack, err
}

func (b *sshBackend) Assistant(ctx context.Context, req protocol.AssistantStreamRequest) (io.ReadCloser, protocol.ACPStreamResponse, error) {
	type assistantResult struct {
		stream io.ReadCloser
		ack    protocol.ACPStreamResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (assistantResult, error) {
		s, ack, err := c.AssistantStream(req)
		if s == nil {
			return assistantResult{ack: ack}, err
		}
		return assistantResult{stream: s, ack: ack}, err
	})
	return out.stream, out.ack, err
}

func (b *sshBackend) Terminal(ctx context.Context, req protocol.TerminalRequest) (webgate.Terminal, protocol.TerminalResponse, error) {
	type terminalResult struct {
		term webgate.Terminal
		ack  protocol.TerminalResponse
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (terminalResult, error) {
		term, ack, err := c.TerminalStream(req)
		if term == nil {
			return terminalResult{ack: ack}, err
		}
		return terminalResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	return stream(context.Background(), b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.Sync(runID, force) })
}

func (b *sshBackend) Forward(target string, port uint32) (io.ReadWriteCloser, error) {
	return stream(context.Background(), b, func(c *cli.Conn) (io.ReadWriteCloser, error) {
		return c.Forward(target, port)
	})
}

func (b *sshBackend) BrowserFrames(ctx context.Context, req protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	return stream(ctx, b, func(c *cli.Conn) (io.ReadCloser, error) { return c.BrowserFrames(ctx, req) })
}

func (b *sshBackend) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	type capture struct {
		stream   io.ReadCloser
		artifact protocol.DevArtifact
	}
	out, err := stream(ctx, b, func(c *cli.Conn) (capture, error) {
		source, artifact, err := c.Artifact(ctx, req)
		return capture{stream: source, artifact: artifact}, err
	})
	return out.stream, out.artifact, err
}

var (
	_ webgate.DevelopmentBackend = (*sshBackend)(nil)
	_ webgate.ACPBackend         = (*sshBackend)(nil)
	_ webgate.AssistantBackend   = (*sshBackend)(nil)
)
