package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxSubsystemHeaderBytes bounds the single JSON header line the
	// events, attach, and setup subsystems read before their stream
	// begins. Those requests are a handful of short fields; the shared
	// protocol.MaxLineBytes cap is sized for control-channel configuration
	// imports and would let one channel buffer 96 MiB here.
	maxSubsystemHeaderBytes = 4 << 10
	// maxPendingLineBytes bounds one control-channel request line while
	// the caller is still pending: server.info, all a pending member may
	// call, is a ~100 byte line.
	maxPendingLineBytes = 64 << 10
)

// serveControl runs the NDJSON JSON-RPC loop on an aether-control
// subsystem channel: requests in, responses out, strictly in order.
//
// The 96 MiB line budget belongs to approved members (config.import sends
// base64 blobs up to its request cap). handleRequest can only refuse a
// pending member after the line has been read, so until the store says
// the caller is approved each line is capped at a request-sized limit.
// Pending state is re-read before each line, so approval unblocks the
// connection the member already holds.
func (s *Server) serveControl(ctx context.Context, member domain.MemberID, ch ssh.Channel, abortConn func()) {
	defer func() {
		sendExitStatus(ch, 0)
		_ = ch.Close()
	}()
	input := &controlFrameReader{
		server: s, member: member, ctx: ctx, abort: abortConn,
		capped: capReader{r: ch},
	}
	r := bufio.NewReaderSize(input, maxPendingLineBytes)
	for ctx.Err() == nil {
		if !s.serveControlFrame(ctx, member, ch, r, input) {
			return
		}
	}
}

// Each call owns the line, decoded request, handler result and admission.
// Nothing large is retained by the persistent channel between calls.
func (s *Server) serveControlFrame(ctx context.Context, member domain.MemberID, ch ssh.Channel, r *bufio.Reader, input *controlFrameReader) bool {
	input.capped.left = int64(maxPendingLineBytes - r.Buffered())
	if !input.approved {
		m, err := s.memberFor(ctx, member)
		input.approved = err == nil && !m.Pending
	}
	input.begin(r.Buffered() > 0)
	defer input.finish()
	line, err := protocol.ReadLine(r)
	if err != nil || ctx.Err() != nil {
		return false
	}
	input.readDone()
	if len(bytes.TrimSpace(line)) == 0 {
		return true
	}
	slot := &afterResponse{}
	resp := s.handleRequest(context.WithValue(ctx, afterResponseKey{}, slot), member, line)
	// Do not answer or restart for an update after channel/server teardown.
	if ctx.Err() != nil {
		return false
	}
	return respond(ch, resp, slot) == nil
}

var errControlFrameBusy = errors.New("sshd: too many expanded control frames")

// capReader stops at the small budget even for approved members. Probe one
// additional byte before admission, so an idle channel (including one parked
// exactly at the small budget) never reserves a large-frame slot. The probe
// cannot cause ReadLine to grow its buffer until admission succeeds.
type controlFrameReader struct {
	server   *Server
	member   domain.MemberID
	ctx      context.Context
	abort    func()
	capped   capReader
	approved bool
	expanded bool

	mu       sync.Mutex
	timer    *time.Timer
	started  time.Time
	progress time.Time
	reading  bool
	active   bool
}

func (r *controlFrameReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	var n int
	var err error
	if r.capped.left == 0 && r.approved {
		n, err = r.capped.r.Read(p[:1])
		if n > 0 {
			r.recordProgress()
			if !r.server.claimControlFrame(r.member) {
				return 0, errControlFrameBusy
			}
			r.expanded = true
			r.capped.left = -1
		}
	} else {
		n, err = r.capped.Read(p)
		if n > 0 {
			r.recordProgress()
		}
	}
	return n, err
}

func (s *Server) claimControlFrame(member domain.MemberID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.controlFrames[member]; held || len(s.controlFrames) >= 2 {
		return false
	}
	s.controlFrames[member] = struct{}{}
	return true
}

func (r *controlFrameReader) begin(buffered bool) {
	r.mu.Lock()
	r.reading = true
	r.mu.Unlock()
	if buffered {
		r.recordProgress()
	}
}

func (r *controlFrameReader) recordProgress() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = time.Now()
	if !r.active {
		r.active = true
		r.started = r.progress
		r.timer = time.AfterFunc(min(r.server.cfg.controlReadIdleTimeout, r.server.cfg.controlFrameTimeout), r.expire)
	}
}

// A single timer checks the latest progress under the mutex rather than
// racing Reset against a callback on every channel read. Expanded frames keep
// the absolute timer through dispatch and response, including a blocked peer.
func (r *controlFrameReader) expire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	deadline := r.started.Add(r.server.cfg.controlFrameTimeout)
	if idle := r.progress.Add(r.server.cfg.controlReadIdleTimeout); r.reading && idle.Before(deadline) {
		deadline = idle
	}
	if remaining := time.Until(deadline); remaining > 0 {
		r.timer.Reset(remaining)
		return
	}
	r.abort()
}

func (r *controlFrameReader) readDone() {
	r.mu.Lock()
	r.reading = false
	if !r.expanded {
		r.active = false
		if r.timer != nil {
			r.timer.Stop()
		}
	}
	r.mu.Unlock()
}

func (r *controlFrameReader) finish() {
	r.mu.Lock()
	r.active = false
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	if r.expanded {
		r.server.mu.Lock()
		delete(r.server.controlFrames, r.member)
		r.server.mu.Unlock()
		r.expanded = false
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
	result, rpcErr := s.dispatch(ctx, member, req.Method, req.Params)
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	resp.Result = result
	return resp
}

// dispatch runs one control-channel method for member: the SSH control
// loop and the in-process client (Local) both call it, so the pending
// gate and the per-call capability checks have one implementation.
func (s *Server) dispatch(ctx context.Context, member domain.MemberID, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	handler, ok := methodHandlers[method]
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
	}
	// Re-validate the caller per request. Pending members may call only
	// server.info; everything else is denied until an admin approves them.
	m, err := s.memberFor(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Pending && method != protocol.MethodServerInfo {
		return nil, &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: "membership pending admin approval; ask an admin to run member.approve " + string(member),
		}
	}
	result, rpcErr := handler(s, ctx, member, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "marshal result: " + err.Error()}
	}
	return raw, nil
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
