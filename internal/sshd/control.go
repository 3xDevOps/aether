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
		resp := s.handleRequest(ctx, member, line)
		// A canceled serve context means the server is shutting down (or
		// the channel is tearing down): the connection must die, not
		// answer - a canceled-context store lookup must never surface to
		// the client as an internal rpc error.
		if ctx.Err() != nil {
			return
		}
		if writeJSONLine(ch, resp) != nil {
			return
		}
	}
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
