package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxSubsystemHeaderBytes bounds the single JSON header line the
	// events, attach, and setup subsystems read before their stream
	// begins. Those requests are a handful of short fields; the shared
	// protocol.MaxLineBytes cap is sized for control-channel profile
	// pushes and would let one channel buffer 32 MiB here.
	maxSubsystemHeaderBytes = 4 << 10
	// maxPendingLineBytes bounds one control-channel request line while
	// the caller is still pending: server.info, all a pending member may
	// call, is a ~100 byte line.
	maxPendingLineBytes = 64 << 10
)

// serveControl runs the NDJSON JSON-RPC loop on an aether-control
// subsystem channel: requests in, responses out, strictly in order.
//
// The 32 MiB line budget belongs to approved members (profile.push sends
// base64 blobs up to the profile cap). handleRequest can only refuse a
// pending member after the line has been read, so until the store says
// the caller is approved each line is capped at a request-sized limit.
// The state is re-read before every line rather than once at channel
// open, so an approval unblocks the connection the pending member is
// already holding.
func (s *Server) serveControl(ctx context.Context, member domain.MemberID, ch ssh.Channel) {
	defer func() {
		sendExitStatus(ch, 0)
		_ = ch.Close()
	}()
	capped := &capReader{r: ch, left: maxPendingLineBytes}
	r := bufio.NewReaderSize(capped, 64<<10)
	for {
		if capped.left >= 0 {
			capped.left = maxPendingLineBytes
			if m, merr := s.memberFor(ctx, member); merr == nil && !m.Pending {
				capped.left = -1
			}
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		slot := &afterResponse{}
		resp := s.handleRequest(context.WithValue(ctx, afterResponseKey{}, slot), member, line)
		// A canceled serve context means the server is shutting down (or
		// the channel is tearing down): the connection must die, not
		// answer - a canceled-context store lookup must never surface to
		// the client as an internal rpc error. Any deferred work is
		// dropped with it, which is right for the one caller: a
		// self-update that swapped the binaries has already recorded
		// that, and re-executing a server somebody just asked to stop
		// would be the wrong way to honor it. The new binary starts on
		// the next start.
		if ctx.Err() != nil {
			return
		}
		if respond(ch, resp, slot) != nil {
			return
		}
	}
}

// respond writes one response and then runs whatever the handler deferred,
// in that order.
//
// The deferred work runs even when the write failed. Its only user is the
// server self-update, which has already replaced the binaries by the time
// it gets here: a client that vanished mid-call would otherwise leave the
// server running the old image, reporting the update as applied, and
// holding its one update slot for the rest of the process's life. The
// client can reconnect; a swap with no restart cannot fix itself.
func respond(w io.Writer, resp protocol.Response, slot *afterResponse) error {
	err := writeJSONLine(w, resp)
	if slot.fn != nil {
		slot.fn()
	}
	return err
}

// afterResponse is one request's slot for work that must not run until the
// server has tried to write its response. Only the server self-update uses
// it: it re-executes the binary, and a client that never saw the result
// could not tell a restart from a dropped connection.
type afterResponse struct{ fn func() }

type afterResponseKey struct{}

// deferUntilResponded registers fn to run once this request's response has
// been written, reporting whether there was a slot to register it in. A
// handler reached from anywhere but the control loop gets false and
// decides for itself.
func deferUntilResponded(ctx context.Context, fn func()) bool {
	slot, ok := ctx.Value(afterResponseKey{}).(*afterResponse)
	if !ok {
		return false
	}
	slot.fn = fn
	return true
}

func writeJSONLine(w io.Writer, v any) error {
	out, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(out, '\n'))
	return err
}

func (s *Server) handleRequest(ctx context.Context, member domain.MemberID, line []byte) protocol.Response {
	req, resp, valid := protocol.ParseRequest(line)
	if !valid {
		return resp
	}
	handler, ok := methodHandlers[req.Method]
	if !ok {
		resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + req.Method}
		return resp
	}
	// Re-validate the caller per request. Pending members may call only
	// server.info; everything else is denied until an admin approves them.
	m, err := s.memberFor(ctx, member)
	if err != nil {
		resp.Error = rpcError(err)
		return resp
	}
	if m.Pending && req.Method != protocol.MethodServerInfo {
		resp.Error = &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: "membership pending admin approval; ask an admin to run member.approve " + string(member),
		}
		return resp
	}
	result, rpcErr := handler(s, ctx, member, req.Params)
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &protocol.Error{Code: protocol.CodeInternal, Message: "marshal result: " + err.Error()}
		return resp
	}
	resp.Result = raw
	return resp
}

type methodHandler func(s *Server, ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error)

var methodHandlers = map[string]methodHandler{}

// registerMethod adds a control-channel handler. Call from init() so later
// waves can add methods without editing a central map.
func registerMethod(name string, h methodHandler) {
	if name == "" || h == nil {
		panic("sshd: registerMethod requires a name and handler")
	}
	if _, dup := methodHandlers[name]; dup {
		panic("sshd: duplicate method handler: " + name)
	}
	methodHandlers[name] = h
}

func init() {
	registerMethod(protocol.MethodServerInfo, (*Server).serverInfo)
	registerMethod(protocol.MethodWorkspaceList, (*Server).workspaceList)
	registerMethod(protocol.MethodWorkspaceGet, (*Server).workspaceGet)
	registerMethod(protocol.MethodMemberApprove, (*Server).memberApprove)
	registerMethod(protocol.MethodMemberList, (*Server).memberList)
	registerMethod(protocol.MethodRunList, (*Server).runList)
	registerMethod(protocol.MethodRunGet, (*Server).runGet)
	registerMethod(protocol.MethodRunPull, (*Server).runPull)
}

// decodeParams is the control channel's spelling of protocol.DecodeParams:
// the handlers report a bad body as "invalid params: ...".
func decodeParams[T any](raw json.RawMessage) (T, *protocol.Error) {
	p, err := protocol.DecodeParams[T](raw)
	if err != nil {
		return p, invalidParams("invalid params: " + err.Error())
	}
	return p, nil
}

func invalidParams(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: msg}
}
