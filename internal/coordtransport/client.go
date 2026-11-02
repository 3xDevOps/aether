package coordtransport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// callMargin covers local dial, SQLite, and response framing work around a
// server-side inbox long poll or the two-minute evidence capture budget. The
// ceiling keeps malformed or future wait values from turning one request into
// an unbounded client call.
const (
	callDefaultTimeout  = 30 * time.Second
	callMargin          = 3 * time.Second
	callReportTimeout   = 2*time.Minute + callMargin
	callDeadlineCeiling = callReportTimeout
	// callConfirmedTimeout covers the member's 10 minute answer to an
	// assistant confirmation and then the call itself.
	callConfirmedTimeout = 25 * time.Minute
)

// ConfirmedMethods wait for the member to decide the assistant's card for
// them, so a call outlasts the card; internal/assistant/tools checks every
// method it confirms is here.
var ConfirmedMethods = []string{protocol.MethodRunKill, protocol.MethodRunClose, protocol.MethodEnvSave, protocol.MethodRunLaunch, protocol.MethodRunInject,
	protocol.MethodRunInputRespond, protocol.MethodRunRoomPost,
	protocol.MethodRunRelaunch, protocol.MethodRunModeSwitch, protocol.MethodRunProtect, protocol.MethodRunArchive, protocol.MethodRunHandoff,
	protocol.MethodRunPRCreate, protocol.MethodRunPRMerge,
	protocol.MethodMemoryAdd, protocol.MethodMemoryPin, protocol.MethodMemoryUnpin, protocol.MethodTemplateLaunch, protocol.MethodLaunchDefaultsSet,
	protocol.MethodWatchDelete, protocol.MethodAssistantForget}

func callTimeout(method string, params any) time.Duration {
	timeout := callDefaultTimeout
	switch method {
	case protocol.MethodCoordReport:
		timeout = callReportTimeout
	case protocol.MethodCoordHookStatus:
		if params != nil {
			timeout = callMargin + time.Duration(pollWaitSeconds(params))*time.Second
		}
	case protocol.MethodCoordInbox:
		timeout = callMargin + time.Duration(pollWaitSeconds(params))*time.Second
	case protocol.MethodDevTerminalWait, protocol.MethodDevTerminalStop, protocol.MethodDevBrowserReset:
		timeout = 30*time.Second + callMargin
	case protocol.MethodDevBrowserNavigate, protocol.MethodDevBrowserAction, protocol.MethodDevBrowserWait:
		// Browser operations allow 30 seconds plus the companion's five-second
		// response margin before the outer socket framing margin.
		timeout = 35*time.Second + callMargin
	case protocol.MethodDevBrowserOpen, protocol.MethodDevTerminalScreenshot:
		// First launch includes image acquisition and companion readiness.
		timeout = 90*time.Second + callMargin
	}
	if slices.Contains(ConfirmedMethods, method) {
		timeout = callConfirmedTimeout
	}
	if timeout > callDeadlineCeiling && !longPoll(method) && timeout != callConfirmedTimeout {
		return callDeadlineCeiling
	}
	return timeout
}

// longPoll reports whether the server may hold the request open for the
// caller's wait_seconds, which is what exempts it from the deadline ceiling.
func longPoll(method string) bool {
	return method == protocol.MethodCoordInbox || method == protocol.MethodCoordHookStatus
}

// pollWaitSeconds clamps the caller's wait to the same bound the server
// enforces, so a malformed or future value cannot stretch the client call.
func pollWaitSeconds(params any) int {
	wait := 0
	switch p := params.(type) {
	case protocol.CoordInboxParams:
		wait = p.WaitSeconds
	case *protocol.CoordInboxParams:
		if p != nil {
			wait = p.WaitSeconds
		}
	case protocol.CoordHookStatusParams:
		wait = p.WaitSeconds
	case *protocol.CoordHookStatusParams:
		if p != nil {
			wait = p.WaitSeconds
		}
	}
	if wait < 0 {
		return 0
	}
	if wait > protocol.CoordMaxInboxWaitSeconds {
		return protocol.CoordMaxInboxWaitSeconds
	}
	return wait
}

// Call makes one coordination request on socket and decodes its result
// into result, which may be nil. It preserves the coordination wire-v3
// framing used by the CLI, MCP bridge, and lifecycle reporter. ctx bounds
// the round trip.
func Call(ctx context.Context, socket, method string, params, result any) error {
	return (&client{socket: socket}).call(ctx, method, params, result)
}

// client speaks coordination wire v3 on the run's unix socket.
//
// It dials per tool call and closes the connection again: a server restart
// rebinds the socket to a new inode, so a held connection would have to be
// re-dialled anyway, and a connection that lives for exactly one request
// stays inside the server's per-run cap and idle timeout. Across a restart
// the call itself is retried; see retry.go.
type client struct{ socket string }

// call sends one request, retrying across a server restart where that is
// safe, and decodes its response. A failure that outlives the retry window
// - a missing socket, a refused connection, an EOF or broken pipe -
// becomes CodeUnavailable, whose message says whether the request may
// already have applied. Errors the server itself returned pass through with
// their own code and message, so the agent sees the wire contract verbatim.
func (c *client) call(ctx context.Context, method string, params, result any) error {
	req := protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return internalError(method, err)
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		return internalError(method, err)
	}
	if strings.HasPrefix(method, "dev.") && len(line)+1 > 64<<10 {
		return &coordError{Code: protocol.CodeInvalidParams, Message: method + ": request exceeds 64 KiB"}
	}
	line = append(line, '\n')
	safe := repeatable(method, req.Params)
	timeout := callTimeout(method, params)
	wait := 0
	if longPoll(method) && params != nil {
		wait = pollWaitSeconds(params)
	}

	started, delay := time.Now(), retryInitialDelay
	var missingSince time.Time
	everSent := false
	for attempts := 0; ; attempts++ {
		if attempts > 0 && wait > 0 {
			// A retried long poll waits only for what is left of the
			// caller's wait, so restarts do not stretch the call.
			remaining := max(0, wait-int(math.Ceil(time.Since(started).Seconds())))
			if line, err = withWait(req, remaining); err != nil {
				return internalError(method, err)
			}
			timeout = callMargin + time.Duration(remaining)*time.Second
		}
		sent, err := c.attempt(ctx, timeout, method, line, result)
		if err == nil {
			return nil
		}
		everSent = everSent || sent
		if !retryable(err) {
			var f *transportFailure
			if errors.As(err, &f) {
				if everSent {
					return unavailableSent(method, f.cause)
				}
				return unavailable(method, f.cause)
			}
			return err
		}
		if everSent && !safe {
			return ambiguous(method, err)
		}
		var window time.Duration
		if errors.Is(err, fs.ErrNotExist) {
			if missingSince.IsZero() {
				missingSince = time.Now()
			}
			window = missingSocketWindow - time.Since(missingSince)
		} else {
			missingSince = time.Time{}
			window = retryWindow - time.Since(started)
		}
		if window <= 0 || ctx.Err() != nil {
			if everSent {
				return unavailableSent(method, err)
			}
			return unavailableUnsent(method, err)
		}
		select {
		case <-ctx.Done():
			if everSent {
				return unavailableSent(method, err)
			}
			return unavailableUnsent(method, err)
		case <-time.After(min(delay, window)):
		}
		delay = min(delay*2, retryMaxDelay)
	}
}

// withWait returns req's request line with its params' wait_seconds set to
// seconds.
func withWait(req protocol.Request, seconds int) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(req.Params, &fields); err != nil {
		return nil, err
	}
	fields["wait_seconds"] = json.RawMessage(strconv.Itoa(seconds))
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	req.Params = raw
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// attempt makes one dial-write-read round trip. sent is false only when the
// request provably never reached the server as a complete line.
func (c *client) attempt(ctx context.Context, timeout time.Duration, method string, line []byte, result any) (sent bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return false, &transportFailure{cause: err}
	}
	defer conn.Close() //nolint:errcheck // read side of a connection being discarded
	// A cancelled context has to end the round trip, not just stop anyone
	// waiting on it: a read left to finish would take delivery of a batch
	// on behalf of a caller that has already walked away.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	if deadline, ok := ctx.Deadline(); ok {
		if derr := conn.SetDeadline(deadline); derr != nil {
			return false, unavailable(method, derr)
		}
	}
	// A failed Write delivered less than the whole line, and the server
	// acts only on a newline-terminated line.
	if _, werr := conn.Write(line); werr != nil {
		return false, &transportFailure{cause: werr}
	}
	var responseReader io.Reader = conn
	if strings.HasPrefix(method, "dev.") {
		// The general protocol also transports large configuration files; a
		// development control response never borrows that larger budget.
		responseReader = io.LimitReader(conn, 64<<10)
	}
	raw, err := protocol.ReadLine(bufio.NewReader(responseReader))
	if err != nil {
		return true, &transportFailure{cause: err, timedOut: ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded)}
	}
	var resp protocol.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return true, internalError(method, fmt.Errorf("decode response: %w", err))
	}
	if resp.Error != nil {
		ce := &coordError{Code: resp.Error.Code, Message: resp.Error.Message, Data: resp.Error.Data}
		if ce.Code == protocol.CodeUnavailable && strings.HasSuffix(ce.Message, protocol.CoordRunClosingSuffix) {
			// The server refused before acting, while shutting down.
			return false, &transportFailure{cause: ce}
		}
		return true, ce
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return true, internalError(method, fmt.Errorf("decode result: %w", err))
		}
	}
	return true, nil
}

// coordError is a coordination failure carrying the Aether error code that
// classifies it. The code stays out of the MCP envelope's own code field:
// the JSON-RPC layer under MCP reserves that range for transport states
// (-32004 there means "the server is closing"), and an Aether code put
// there would tear the session down instead of telling the agent what
// happened.
type coordError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *coordError) Error() string {
	return fmt.Sprintf("%s [aether error %d]", e.Message, e.Code)
}

func unavailable(method string, cause error) *coordError {
	return &coordError{
		Code:    protocol.CodeUnavailable,
		Message: fmt.Sprintf("%s: coordination is not reachable: %v", method, cause),
	}
}

func internalError(method string, cause error) *coordError {
	return &coordError{
		Code:    protocol.CodeInternal,
		Message: fmt.Sprintf("%s: %v", method, cause),
	}
}

// ProtocolError is err as the *protocol.Error it carries when Call returned
// a coordination failure, else err unchanged.
func ProtocolError(err error) error {
	var ce *coordError
	if errors.As(err, &ce) {
		return &protocol.Error{Code: ce.Code, Message: ce.Message, Data: ce.Data}
	}
	return err
}

// ErrorCode classifies an error returned by Call. A zero result means the
// failure was local to the transport (for example JSON encoding), rather than
// a coordination protocol error.
func ErrorCode(err error) int {
	var ce *coordError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}
