package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
)

func callTimeout(method string, params any) time.Duration {
	timeout := callDefaultTimeout
	switch method {
	case protocol.MethodCoordReport:
		timeout = callReportTimeout
	case protocol.MethodCoordInbox:
		timeout = callMargin
		wait := 0
		switch p := params.(type) {
		case protocol.CoordInboxParams:
			wait = p.WaitSeconds
		case *protocol.CoordInboxParams:
			if p != nil {
				wait = p.WaitSeconds
			}
		}
		if wait < 0 {
			wait = 0
		}
		if wait > protocol.CoordMaxInboxWaitSeconds {
			wait = protocol.CoordMaxInboxWaitSeconds
		}
		timeout += time.Duration(wait) * time.Second
	}
	if timeout > callDeadlineCeiling && method != protocol.MethodCoordInbox {
		return callDeadlineCeiling
	}
	return timeout
}

// Call makes one coordination request on socket and decodes its result
// into result, which may be nil. It is the same framing the MCP tools use,
// exposed for the in-container callers that are not tools at all - the
// status reporter ("aether-server report") calls run.report with it. ctx
// bounds the round trip.
func Call(ctx context.Context, socket, method string, params, result any) error {
	return (&client{socket: socket}).call(ctx, method, params, result)
}

// client speaks coordination wire v3 on the run's unix socket.
//
// It dials per tool call and closes the connection again. That is the whole
// reconnection strategy: a server restart rebinds the socket to a new inode,
// so a held connection would have to be re-dialled anyway; the server caps
// how many connections one run may hold and drops silent ones after an idle
// timeout, and a connection that lives for exactly one request is inside
// both limits without any bookkeeping.
type client struct{ socket string }

// call sends one request and reads its response. Every local socket failure
// - a missing socket, a refused connection, an EOF or broken pipe from a
// server that restarted or refused the connection over its per-run cap -
// becomes CodeUnavailable, which is what the agent needs to know: not
// reachable now, keep working, try again. Errors the server itself returned
// pass through with their own code and message, so the agent sees the wire
// contract verbatim.
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

	ctx, cancel := context.WithTimeout(ctx, callTimeout(method, params))
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return unavailable(method, err)
	}
	defer conn.Close() //nolint:errcheck // read side of a connection being discarded
	// A cancelled context has to end the round trip, not just stop anyone
	// waiting on it: a read left to finish would take delivery of a batch
	// on behalf of a caller that has already walked away.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	if deadline, ok := ctx.Deadline(); ok {
		if derr := conn.SetDeadline(deadline); derr != nil {
			return unavailable(method, derr)
		}
	}
	if _, werr := conn.Write(append(line, '\n')); werr != nil {
		return unavailable(method, werr)
	}
	raw, err := protocol.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return unavailable(method, err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return internalError(method, fmt.Errorf("decode response: %w", err))
	}
	if resp.Error != nil {
		return &coordError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return internalError(method, fmt.Errorf("decode result: %w", err))
		}
	}
	return nil
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

// ErrorCode classifies an error returned by Call. A zero result means the
// failure was local to the bridge (for example JSON encoding), rather than a
// coordination protocol error.
func ErrorCode(err error) int {
	var ce *coordError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}
