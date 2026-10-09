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
// Each call and stream opens its own channel; mu guards only (re)dialing.
type sshBackend struct {
	cfg cli.Config

	mu   sync.Mutex
	conn *cli.Conn
}

// NewSSHBackend returns a Backend that redials once when the connection drops
// under a replay-safe call.
func NewSSHBackend(cfg cli.Config) Backend {
	return &sshBackend{cfg: cfg}
}

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

func (b *sshBackend) Relink(cfg cli.Config, conn *cli.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		_ = b.conn.Close()
	}
	b.cfg = cfg
	b.conn = conn
}

// live returns the shared connection, dialing if needed. Dial errors come back
// classified so every surface reports them identically.
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

// invalidate closes conn only if it is still the cached one: a replaced
// connection may carry other live streams.
func (b *sshBackend) invalidate(conn *cli.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == conn {
		_ = conn.Close()
		b.conn = nil
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
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: prefix + err.Error()}
}

// callTimeout bounds one control round-trip. A black-holed TCP connection
// (suspend/resume, network switch) never errors on its own.
const callTimeout = 60 * time.Second

func callBound(method string) time.Duration {
	if method == protocol.MethodAgentInstall {
		return protocol.AgentInstallTimeout + callTimeout
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
func alive(conn *cli.Conn) bool {
	_, _, err := conn.SSH().SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// stream opens one subsystem channel. Errors the server answered pass through
// (attach/sync ack refusals are untyped, hence the keepalive probe); only a
// dead connection is invalidated, redialed and retried once.
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

func (b *sshBackend) Events(_ context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadCloser, error) { return c.EventsStream(req) })
}

func (b *sshBackend) Attach(_ context.Context, req protocol.AttachRequest) (webgate.Terminal, protocol.AttachResponse, error) {
	type attachResult struct {
		term webgate.Terminal
		ack  protocol.AttachResponse
	}
	out, err := stream(b, func(c *cli.Conn) (attachResult, error) {
		term, ack, err := c.AttachStream(req)
		if term == nil {
			return attachResult{ack: ack}, err
		}
		return attachResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) ACP(_ context.Context, req protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error) {
	type acpResult struct {
		stream io.ReadWriteCloser
		ack    protocol.ACPStreamResponse
	}
	out, err := stream(b, func(c *cli.Conn) (acpResult, error) {
		s, ack, err := c.ACPStream(req)
		if s == nil {
			return acpResult{ack: ack}, err
		}
		return acpResult{stream: s, ack: ack}, err
	})
	return out.stream, out.ack, err
}

func (b *sshBackend) Terminal(_ context.Context, req protocol.TerminalRequest) (webgate.Terminal, protocol.TerminalResponse, error) {
	type terminalResult struct {
		term webgate.Terminal
		ack  protocol.TerminalResponse
	}
	out, err := stream(b, func(c *cli.Conn) (terminalResult, error) {
		term, ack, err := c.TerminalStream(req)
		if term == nil {
			return terminalResult{ack: ack}, err
		}
		return terminalResult{term: term, ack: ack}, err
	})
	return out.term, out.ack, err
}

func (b *sshBackend) Sync(runID string, force bool) (io.ReadWriteCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadWriteCloser, error) { return c.Sync(runID, force) })
}

func (b *sshBackend) Forward(target string, port uint32) (io.ReadWriteCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadWriteCloser, error) {
		return c.Forward(target, port)
	})
}

func (b *sshBackend) BrowserFrames(ctx context.Context, req protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	return stream(b, func(c *cli.Conn) (io.ReadCloser, error) { return c.BrowserFrames(ctx, req) })
}

func (b *sshBackend) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	type capture struct {
		stream   io.ReadCloser
		artifact protocol.DevArtifact
	}
	out, err := stream(b, func(c *cli.Conn) (capture, error) {
		source, artifact, err := c.Artifact(ctx, req)
		return capture{stream: source, artifact: artifact}, err
	})
	return out.stream, out.artifact, err
}

var (
	_ webgate.DevelopmentBackend = (*sshBackend)(nil)
	_ webgate.ACPBackend         = (*sshBackend)(nil)
)
