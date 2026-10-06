package acphost

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/version"
)

var (
	// ErrAlreadyAnswered is returned when a request was already answered or
	// cancelled. The first answer wins.
	ErrAlreadyAnswered = errors.New("acphost: request already answered")
	// ErrUnknownRequest is returned for a request id this connection never
	// issued.
	ErrUnknownRequest = errors.New("acphost: unknown request")
	// ErrUnknownOption is returned for an option the request does not offer.
	ErrUnknownOption = errors.New("acphost: option not offered by the request")
)

const methodSteering = "_session/steering"

// deliveryMarker is fed to the SDK ahead of the agent's output and never
// sent on the wire. The SDK hands every notification to the handler with one
// context, which it cancels only once the notifications read before the
// connection ended have been handled; the marker's call captures it.
const deliveryMarker = "_aether/delivery"

// AgentInfo is what the agent advertised in initialize that the host acts on.
type AgentInfo struct {
	Name            string
	Version         string
	ProtocolVersion int
	LoadSession     bool
	Resume          bool
	List            bool
	Steering        bool
	AuthMethods     json.RawMessage
}

type initializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume json.RawMessage `json:"resume"`
			List   json.RawMessage `json:"list"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	AuthMethods json.RawMessage `json:"authMethods"`
	Meta        struct {
		Steering struct {
			Supported bool `json:"supported"`
		} `json:"steering"`
	} `json:"_meta"`
}

// sessionResult is the part of a session/new, session/resume or
// session/load response the host keeps. Modes and config options stay raw
// so a variant this SDK version does not know cannot fail the call.
type sessionResult struct {
	SessionID     string          `json:"sessionId"`
	Modes         json.RawMessage `json:"modes"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

func (r sessionResult) currentMode() string {
	var m struct {
		CurrentModeID string `json:"currentModeId"`
	}
	_ = json.Unmarshal(r.Modes, &m)
	return m.CurrentModeID
}

// connEvents receives what the agent sends. Calls for session/update and
// _auth/status_update arrive in wire order; request calls may interleave.
type connEvents interface {
	update(raw json.RawMessage)
	authStatus(raw json.RawMessage)
	requestOpened(r Request)
	requestClosed(r Request)
}

type answer struct {
	optionID string
	content  map[string]any
}

type pendingRequest struct {
	req    Request
	order  uint64
	answer chan answer
}

// Conn is one ACP client connection to one agent process. It never offers
// fs or terminal methods; the agent uses its own tools inside the container.
type Conn struct {
	rpc    *acp.Connection
	w      io.WriteCloser
	logger *slog.Logger
	events connEvents

	info         AgentInfo
	inbound      context.Context
	inboundSet   chan struct{}
	sessionID    atomic.Value
	replaying    atomic.Bool
	lastActivity atomic.Int64

	mu       sync.Mutex
	idle     *sync.Cond
	waiting  int
	opened   uint64
	pending  map[string]*pendingRequest
	resolved map[string]bool
}

func newConn(r io.Reader, w io.WriteCloser, events connEvents, logger *slog.Logger) *Conn {
	c := &Conn{
		w:          w,
		logger:     logger,
		events:     events,
		inboundSet: make(chan struct{}),
		pending:    make(map[string]*pendingRequest),
		resolved:   make(map[string]bool),
	}
	c.idle = sync.NewCond(&c.mu)
	c.sessionID.Store("")
	c.touch()
	marker := strings.NewReader(`{"jsonrpc":"2.0","method":"` + deliveryMarker + `"}` + "\n")
	c.rpc = acp.NewConnection(c.handle, w, io.MultiReader(marker, r))
	c.rpc.SetLogger(logger)
	return c
}

func (c *Conn) touch() { c.lastActivity.Store(time.Now().UnixNano()) }

// LastActivity is when the agent last sent anything.
func (c *Conn) LastActivity() time.Time { return time.Unix(0, c.lastActivity.Load()) }

// Done is closed when the agent's output ends.
func (c *Conn) Done() <-chan struct{} { return c.rpc.Done() }

// delivered returns once the connection has ended and every notification
// read before the end has been handled, or the SDK gave up waiting for them.
func (c *Conn) delivered() {
	<-c.inboundSet
	<-c.inbound.Done()
}

func (c *Conn) closed() bool {
	select {
	case <-c.rpc.Done():
		return true
	default:
		return false
	}
}

// Close closes the agent's stdin; adapters exit on EOF.
func (c *Conn) Close() error { return c.w.Close() }

// SessionID is the agent session this connection drives.
func (c *Conn) SessionID() string { return c.sessionID.Load().(string) }

// Info is what the agent advertised in initialize.
func (c *Conn) Info() AgentInfo { return c.info }

func call[T any](c *Conn, ctx context.Context, method string, params any) (T, error) {
	res, err := acp.SendRequest[T](c.rpc, ctx, method, params)
	c.touch()
	if err != nil {
		return res, fmt.Errorf("acphost: %s: %w", method, err)
	}
	return res, nil
}

func (c *Conn) initialize(ctx context.Context) error {
	res, err := call[initializeResult](c, ctx, acp.AgentMethodInitialize, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Auth: acp.AuthCapabilities{Terminal: true},
			Elicitation: &acp.ElicitationCapabilities{
				Form: &acp.ElicitationFormCapabilities{},
				Url:  &acp.ElicitationUrlCapabilities{},
			},
			Meta: map[string]any{"terminal_output": true},
		},
		ClientInfo: &acp.Implementation{Name: "aether", Version: version.Version},
	})
	if err != nil {
		return err
	}
	if res.ProtocolVersion != acp.ProtocolVersionNumber {
		return fmt.Errorf("acphost: agent speaks ACP protocol version %d, the host speaks %d", res.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	caps := res.AgentCapabilities
	c.info = AgentInfo{
		ProtocolVersion: res.ProtocolVersion,
		LoadSession:     caps.LoadSession,
		Resume:          present(caps.SessionCapabilities.Resume),
		List:            present(caps.SessionCapabilities.List),
		Steering:        res.Meta.Steering.Supported,
		AuthMethods:     res.AuthMethods,
	}
	if res.AgentInfo != nil {
		c.info.Name, c.info.Version = res.AgentInfo.Name, res.AgentInfo.Version
	}
	return nil
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func (c *Conn) newSession(ctx context.Context, cwd string, mcp []acp.McpServer) (sessionResult, error) {
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionNew, acp.NewSessionRequest{Cwd: cwd, McpServers: nonNil(mcp)})
	if err != nil {
		return res, err
	}
	if res.SessionID == "" {
		return res, fmt.Errorf("acphost: session/new returned no sessionId")
	}
	c.sessionID.Store(res.SessionID)
	return res, nil
}

func (c *Conn) resumeSession(ctx context.Context, id, cwd string, mcp []acp.McpServer) (sessionResult, error) {
	c.sessionID.Store(id)
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionResume, acp.ResumeSessionRequest{SessionId: acp.SessionId(id), Cwd: cwd, McpServers: nonNil(mcp)})
	if err != nil {
		c.sessionID.Store("")
		return res, err
	}
	res.SessionID = id
	return res, nil
}

// loadSession drops the history the agent replays before it answers. The
// SDK hands every notification received before a response to the handler
// before the request returns, so clearing the flag afterwards is exact.
func (c *Conn) loadSession(ctx context.Context, id, cwd string, mcp []acp.McpServer) (sessionResult, error) {
	c.sessionID.Store(id)
	c.replaying.Store(true)
	defer c.replaying.Store(false)
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionLoad, acp.LoadSessionRequest{SessionId: acp.SessionId(id), Cwd: cwd, McpServers: nonNil(mcp)})
	if err != nil {
		c.sessionID.Store("")
		return res, err
	}
	res.SessionID = id
	return res, nil
}

func nonNil(mcp []acp.McpServer) []acp.McpServer {
	if mcp == nil {
		return []acp.McpServer{}
	}
	return mcp
}

// prompt runs one turn and returns its stop reason. It returns only when
// the turn ends.
func (c *Conn) prompt(ctx context.Context, blocks []acp.ContentBlock) (string, error) {
	res, err := call[struct {
		StopReason string `json:"stopReason"`
	}](c, ctx, acp.AgentMethodSessionPrompt, acp.PromptRequest{SessionId: acp.SessionId(c.SessionID()), Prompt: blocks})
	return res.StopReason, err
}

// steer adds input to the running turn. idleBehavior promptRequired makes
// the agent refuse rather than start a turn the host did not open.
func (c *Conn) steer(ctx context.Context, blocks []acp.ContentBlock) (string, error) {
	res, err := call[struct {
		Outcome string `json:"outcome"`
	}](c, ctx, methodSteering, map[string]any{
		"sessionId": c.SessionID(),
		"prompt":    blocks,
		"_meta":     map[string]any{"steering": map[string]any{"idleBehavior": "promptRequired"}},
	})
	return res.Outcome, err
}

// cancel asks the agent to stop the turn and answers every pending request
// cancelled, as the protocol requires.
func (c *Conn) cancel(ctx context.Context) error {
	err := c.rpc.SendNotification(ctx, acp.AgentMethodSessionCancel, acp.CancelNotification{SessionId: acp.SessionId(c.SessionID())})
	c.cancelPending()
	if err != nil {
		return fmt.Errorf("acphost: session/cancel: %w", err)
	}
	return nil
}

func (c *Conn) setMode(ctx context.Context, modeID string) error {
	_, err := call[json.RawMessage](c, ctx, acp.AgentMethodSessionSetMode, acp.SetSessionModeRequest{SessionId: acp.SessionId(c.SessionID()), ModeId: acp.SessionModeId(modeID)})
	return err
}

// setOption sets a select option to a value id, or a boolean option to a
// bool, and returns the agent's complete option list.
func (c *Conn) setOption(ctx context.Context, configID string, value any) (json.RawMessage, error) {
	var req acp.SetSessionConfigOptionRequest
	switch v := value.(type) {
	case bool:
		req.Boolean = &acp.SetSessionConfigOptionBoolean{SessionId: acp.SessionId(c.SessionID()), ConfigId: acp.SessionConfigId(configID), Type: "boolean", Value: v}
	case string:
		req.ValueId = &acp.SetSessionConfigOptionValueId{SessionId: acp.SessionId(c.SessionID()), ConfigId: acp.SessionConfigId(configID), Value: acp.SessionConfigValueId(v)}
	default:
		return nil, fmt.Errorf("acphost: config option value must be a string or a bool, got %T", value)
	}
	res, err := call[struct {
		ConfigOptions json.RawMessage `json:"configOptions"`
	}](c, ctx, acp.AgentMethodSessionSetConfigOption, req)
	return res.ConfigOptions, err
}

// SessionSummary is one entry of the agent's session list.
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// listSessions returns one page of the agent's sessions. The list is global
// to the agent's user, not scoped to this connection.
func (c *Conn) listSessions(ctx context.Context, cwd, cursor string) ([]SessionSummary, string, error) {
	req := acp.ListSessionsRequest{}
	if cwd != "" {
		req.Cwd = &cwd
	}
	if cursor != "" {
		req.Cursor = &cursor
	}
	res, err := call[struct {
		Sessions   []SessionSummary `json:"sessions"`
		NextCursor string           `json:"nextCursor"`
	}](c, ctx, acp.AgentMethodSessionList, req)
	return res.Sessions, res.NextCursor, err
}

func (c *Conn) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	if method == deliveryMarker {
		c.inbound = ctx
		close(c.inboundSet)
		return nil, nil
	}
	c.touch()
	switch method {
	case acp.ClientMethodSessionUpdate:
		var n struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(params, &n); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		// Before session/new answers, the session id is not known yet and
		// the agent may already announce commands for it.
		if sid := c.SessionID(); sid != "" && n.SessionID != sid {
			c.logger.Debug("acphost: update for another session", "session", n.SessionID)
			return nil, nil
		}
		if c.replaying.Load() && replayedKind(n.Update) {
			return nil, nil
		}
		c.events.update(n.Update)
		return nil, nil
	case "_auth/status_update":
		var n struct {
			AuthStatus json.RawMessage `json:"authStatus"`
		}
		if err := json.Unmarshal(params, &n); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		c.events.authStatus(n.AuthStatus)
		return nil, nil
	case acp.ClientMethodSessionRequestPermission:
		return c.permission(ctx, params)
	case acp.ClientMethodElicitationCreate:
		return c.elicitation(ctx, params)
	case acp.ClientMethodElicitationComplete:
		return nil, nil
	}
	if strings.HasPrefix(method, "_") {
		c.logger.Debug("acphost: ignoring extension method", "method", method)
	}
	return nil, acp.NewMethodNotFound(method)
}

// replayedKind reports whether an update is conversation history, which
// session/load replays and the item log already holds.
func replayedKind(raw json.RawMessage) bool {
	var u struct {
		Kind string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &u)
	switch u.Kind {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan":
		return true
	}
	return false
}

func (c *Conn) permission(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var p struct {
		SessionID string                 `json:"sessionId"`
		ToolCall  json.RawMessage        `json:"toolCall"`
		Options   []acp.PermissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if sid := c.SessionID(); p.SessionID != sid {
		return nil, acp.NewInvalidParams(map[string]any{"error": fmt.Sprintf("unknown session %q", p.SessionID)})
	}
	var tc struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
	}
	_ = json.Unmarshal(p.ToolCall, &tc)
	if u := toolCallUpdateFrom(p.ToolCall); u != nil {
		c.events.update(u)
	}
	req := Request{Kind: RequestPermission, Title: tc.Title, ToolCallID: tc.ToolCallID}
	if req.Title == "" {
		req.Title = "Permission requested"
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, Option{ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	a, ok := c.wait(ctx, req)
	if !ok {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": a.optionID}}, nil
}

// toolCallUpdateFrom turns a permission request's tool call into a
// tool_call_update so the tool call shows what is being asked about.
func toolCallUpdateFrom(toolCall json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(toolCall, &m) != nil || m == nil {
		return nil
	}
	m["sessionUpdate"] = "tool_call_update"
	b, _ := json.Marshal(m)
	return b
}

var elicitationOptions = []Option{{ID: "accept", Name: "Accept"}, {ID: "decline", Name: "Decline"}}

func (c *Conn) elicitation(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var p struct {
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		URL             string          `json:"url"`
		ToolCallID      string          `json:"toolCallId"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	req := Request{Title: p.Message, ToolCallID: p.ToolCallID, Options: elicitationOptions}
	switch p.Mode {
	case "form":
		req.Kind, req.Schema = RequestQuestion, p.RequestedSchema
	case "url":
		req.Kind, req.URL = RequestLink, p.URL
	default:
		return map[string]any{"action": "decline"}, nil
	}
	a, ok := c.wait(ctx, req)
	if !ok {
		return map[string]any{"action": "cancel"}, nil
	}
	res := map[string]any{"action": a.optionID}
	if a.optionID == "accept" && a.content != nil {
		res["content"] = a.content
	}
	return res, nil
}

// wait registers a pending request and blocks until it is answered, the
// turn is cancelled, or the agent withdraws it. ok is false when cancelled.
func (c *Conn) wait(ctx context.Context, req Request) (answer, bool) {
	req.ID = rand.Text()
	req.Status = RequestPending
	p := &pendingRequest{req: req, answer: make(chan answer, 1)}
	c.mu.Lock()
	c.opened++
	c.waiting++
	p.order = c.opened
	c.pending[req.ID] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.waiting--
		c.idle.Broadcast()
		c.mu.Unlock()
	}()
	c.events.requestOpened(req)

	var a answer
	var ok bool
	select {
	case a, ok = <-p.answer:
	case <-ctx.Done():
		c.mu.Lock()
		if c.pending[req.ID] == p {
			delete(c.pending, req.ID)
			c.resolved[req.ID] = true
		} else {
			// Answered concurrently; the answer is already buffered.
			a, ok = <-p.answer
		}
		c.mu.Unlock()
	}
	closed := req
	if ok {
		closed.Status, closed.Answer = RequestAnswered, a.optionID
	} else {
		closed.Status = RequestCancelled
	}
	c.events.requestClosed(closed)
	return a, ok
}

// answer resolves a pending request. The option id is forwarded verbatim.
func (c *Conn) answer(requestID, optionID string, content map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pending[requestID]
	if p == nil {
		if c.resolved[requestID] {
			return ErrAlreadyAnswered
		}
		return ErrUnknownRequest
	}
	if !slices.ContainsFunc(p.req.Options, func(o Option) bool { return o.ID == optionID }) {
		return fmt.Errorf("%w: %q", ErrUnknownOption, optionID)
	}
	if content != nil && (p.req.Kind != RequestQuestion || optionID != "accept") {
		return fmt.Errorf("acphost: only an accepted question carries content")
	}
	delete(c.pending, requestID)
	c.resolved[requestID] = true
	p.answer <- answer{optionID: optionID, content: content}
	return nil
}

func (c *Conn) cancelPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, p := range c.pending {
		delete(c.pending, id)
		c.resolved[id] = true
		close(p.answer)
	}
}

// drain cancels every pending request and returns once each one has been
// reported closed.
func (c *Conn) drain() {
	c.cancelPending()
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.waiting > 0 {
		c.idle.Wait()
	}
}

func (c *Conn) pendingRequests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	ps := slices.SortedFunc(maps.Values(c.pending), func(a, b *pendingRequest) int { return cmp.Compare(a.order, b.order) })
	out := make([]Request, len(ps))
	for i, p := range ps {
		out[i] = p.req
	}
	return out
}
