package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// callTimeout bounds one coordination round trip. The socket is local and
// every method behind it is a small SQLite read or write, so anything
// slower than this is a server the agent should stop waiting on.
const callTimeout = 30 * time.Second

// client speaks coordination wire v1 on the run's unix socket.
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

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
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
