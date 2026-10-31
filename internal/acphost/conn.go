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

	"github.com/3xDevOps/Aether/internal/acpkeeper"
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

// answeredMarker is fed to the SDK right behind the agent's answer to a
// session/prompt. The SDK hands a response to its caller once the
// notifications read before it are handled, but handles later ones
// meanwhile; the marker's call holds them until the turn's end is recorded.
const answeredMarker = "_aether/prompt-answered"

var answeredLine = []byte(`{"jsonrpc":"2.0","method":"` + answeredMarker + `"}` + "\n")

// maxChildren bounds the subagent sessions one connection tracks.
const maxChildren = 1024

// childAnnounceWait bounds how long a request naming an unknown session
// waits for the update that announces it as a subagent session.
var childAnnounceWait = 5 * time.Second

// AgentInfo is what the agent advertised in initialize that the host acts on.
type AgentInfo struct {
	Name            string
	Version         string
	ProtocolVersion int
	LoadSession     bool
	Resume          bool
	List            bool
	Steering        bool
	PromptImages    bool
	PromptEmbedded  bool
	AuthMethods     json.RawMessage
}

type initializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image           bool `json:"image"`
			EmbeddedContext bool `json:"embeddedContext"`
		} `json:"promptCapabilities"`
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
// session/load response the host keeps. Models, modes and config options stay
// raw so a variant this SDK version does not know cannot fail the call.
type sessionResult struct {
	SessionID     string          `json:"sessionId"`
	Modes         json.RawMessage `json:"modes"`
	Models        json.RawMessage `json:"models"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

// connEvents receives what the agent sends. Calls for session/update,
// _auth/status_update, _claude/sdkMessage and promptAnswered arrive in wire
// order; request calls may interleave. update's parent is empty for the
// agent's session and the launch id for a subagent session's.
// replayedSubagent gets the subagent updates session/load replays.
type connEvents interface {
	update(parent string, raw json.RawMessage)
	replayedSubagent(c subagentChild)
	authStatus(raw json.RawMessage)
	sdkMessage(raw json.RawMessage)
	promptAnswered()
	// flushText writes the streamed text the session still buffers.
	flushText()
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
	w      *agentInput
	logger *slog.Logger
	events connEvents

	info         AgentInfo
	inbound      context.Context
	inboundSet   chan struct{}
	sessionID    atomic.Value
	replaying    atomic.Bool
	lastActivity atomic.Int64
	// answers counts session/prompt answers read from the agent, in wire
	// order with requests.
	answers   atomic.Int64
	autoAllow bool
	// childSessions declares the ACP subagents client capability.
	childSessions bool
	// sessionMeta is the _meta of session/new, session/resume and
	// session/load.
	sessionMeta map[string]any

	mu       sync.Mutex
	idle     *sync.Cond
	waiting  int
	opened   uint64
	pending  map[string]*pendingRequest
	resolved map[string]bool
	// children holds the subagent sessions announced on this connection;
	// announced is closed and replaced at each new one.
	children  map[string]childLink
	announced chan struct{}
	// cancelled is closed and replaced each time pending requests are
	// cancelled.
	cancelled chan struct{}
	// reuse holds the ids an adopted session's log gives requests still
	// pending, for the agent's requests the keeper sends again.
	reuse []Request
}

// childLink is a subagent session's launch id and the launch id of the
// session that announced it, empty for the agent's own.
type childLink struct{ launch, parent string }

func newConn(r io.Reader, w io.WriteCloser, events connEvents, logger *slog.Logger) *Conn {
	outR, outW := io.Pipe()
	c := &Conn{
		w:          &agentInput{WriteCloser: w, output: outW},
		logger:     logger,
		events:     events,
		inboundSet: make(chan struct{}),
		pending:    make(map[string]*pendingRequest),
		resolved:   make(map[string]bool),
		children:   make(map[string]childLink),
		announced:  make(chan struct{}),
		cancelled:  make(chan struct{}),
	}
	c.idle = sync.NewCond(&c.mu)
	go c.relay(outW, r)
	c.sessionID.Store("")
	c.touch()
	marker := strings.NewReader(`{"jsonrpc":"2.0","method":"` + deliveryMarker + `"}` + "\n")
	open := make(chan struct{})
	c.rpc = acp.NewConnection(c.handle, c.w, gatedReader{open, io.MultiReader(marker, outR)})
	c.rpc.SetLogger(logger)
	close(open)
	return c
}

func (c *Conn) touch() { c.lastActivity.Store(time.Now().UnixNano()) }

// LastActivity is when the agent last sent anything.
func (c *Conn) LastActivity() time.Time { return time.Unix(0, c.lastActivity.Load()) }

// Done is closed when the agent's output ends or a write to its input fails.
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

func (c *Conn) inputBroken() bool { return c.w.broken.Load() }

// Close closes the agent's stdin; adapters exit on EOF.
func (c *Conn) Close() error { return c.w.Close() }

func (c *Conn) SessionID() string { return c.sessionID.Load().(string) }

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
	meta := map[string]any{"terminal_output": true}
	clientCaps := map[string]any{
		"auth":        acp.AuthCapabilities{Terminal: true},
		"elicitation": acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}, Url: &acp.ElicitationUrlCapabilities{}},
		"_meta":       meta,
	}
	if c.childSessions {
		clientCaps["subagents"] = map[string]any{}
		// Cursor's ACP SDK drops the field above and reads this one.
		meta["subagents"] = map[string]any{}
	}
	res, err := call[initializeResult](c, ctx, acp.AgentMethodInitialize, map[string]any{
		"protocolVersion":    acp.ProtocolVersionNumber,
		"clientCapabilities": clientCaps,
		"clientInfo":         acp.Implementation{Name: "aether", Version: version.Version},
	})
	if err != nil {
		return err
	}
	return c.setInfo(res)
}

// setInfo takes what the agent advertised in its initialize result.
func (c *Conn) setInfo(res initializeResult) error {
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
		PromptImages:    caps.PromptCapabilities.Image,
		PromptEmbedded:  caps.PromptCapabilities.EmbeddedContext,
		AuthMethods:     res.AuthMethods,
	}
	if res.AgentInfo != nil {
		c.info.Name, c.info.Version = res.AgentInfo.Name, res.AgentInfo.Version
	}
	return nil
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func (c *Conn) newSession(ctx context.Context, cwd string, mcp []acp.McpServer) (sessionResult, error) {
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionNew, acp.NewSessionRequest{Cwd: cwd, McpServers: nonNil(mcp), Meta: c.sessionMeta})
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
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionResume, acp.ResumeSessionRequest{SessionId: acp.SessionId(id), Cwd: cwd, McpServers: nonNil(mcp), Meta: c.sessionMeta})
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
	res, err := call[sessionResult](c, ctx, acp.AgentMethodSessionLoad, acp.LoadSessionRequest{SessionId: acp.SessionId(id), Cwd: cwd, McpServers: nonNil(mcp), Meta: c.sessionMeta})
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

// errNothingToAdopt is the keeper's answer to an adopted turn whose prompt
// never reached it.
var errNothingToAdopt = errors.New("acphost: the keeper has no prompt in flight")

// prompt runs one turn and returns its stop reason and the tokens the agent
// reports for it, cache reads and writes as input and thought tokens as
// output. A malformed usage counts no tokens rather than failing the turn. It
// returns only when the turn ends. With no blocks it takes over the turn
// whose prompt an earlier server process sent through the agent's keeper.
func (c *Conn) prompt(ctx context.Context, blocks []acp.ContentBlock) (string, Spend, error) {
	method, params := acp.AgentMethodSessionPrompt, any(acp.PromptRequest{SessionId: acp.SessionId(c.SessionID()), Prompt: blocks})
	if blocks == nil {
		method, params = acpkeeper.AdoptMethod, map[string]any{}
	}
	res, err := call[struct {
		StopReason string          `json:"stopReason"`
		Usage      json.RawMessage `json:"usage"`
	}](c, ctx, method, params)
	var re *acp.RequestError
	if blocks == nil && errors.As(err, &re) && re.Message == acpkeeper.ErrNothingToAdopt.Error() {
		err = errNothingToAdopt
	}
	var u acp.Usage
	if len(res.Usage) == 0 || json.Unmarshal(res.Usage, &u) != nil {
		return res.StopReason, Spend{}, err
	}
	return res.StopReason, Spend{
		InputTokens:  int64(u.InputTokens + deref(u.CachedReadTokens) + deref(u.CachedWriteTokens)),
		OutputTokens: int64(u.OutputTokens + deref(u.ThoughtTokens)),
	}, err
}

// SessionLost reports whether err, as returned for a failed session/prompt,
// says the agent no longer has the session. Claude's adapter drops a session
// whose Claude Code process died but keeps running, and answers every later
// prompt to it so.
func SessionLost(err error) bool {
	var re *acp.RequestError
	if !errors.As(err, &re) || re.Data == nil {
		return false
	}
	raw, err := json.Marshal(re.Data)
	if err != nil {
		return false
	}
	var data struct {
		Details string `json:"details"`
	}
	return json.Unmarshal(raw, &data) == nil && data.Details == "Session not found"
}

// steer adds input to the running turn. idleBehavior promptRequired makes
// the agent refuse rather than start a turn the host did not open.
func (c *Conn) steer(ctx context.Context, blocks []acp.ContentBlock, tag string) (string, error) {
	c.w.mu.Lock()
	c.w.steerTag = tag
	c.w.mu.Unlock()
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
	// A turn's prompt is written from its own goroutine; a cancel that
	// reached the agent first would be ignored and the turn would run on.
	if unsent := c.w.promptUnsent(); unsent != nil {
		select {
		case <-unsent:
		case <-c.rpc.Done():
		case <-ctx.Done():
			return fmt.Errorf("acphost: session/cancel: %w", ctx.Err())
		}
	}
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

// The legacy model API is no longer generated by this SDK version.
func (c *Conn) setModel(ctx context.Context, modelID string) error {
	_, err := call[json.RawMessage](c, ctx, "session/set_model", struct {
		SessionID string `json:"sessionId"`
		ModelID   string `json:"modelId"`
	}{c.SessionID(), modelID})
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
	switch method {
	case deliveryMarker:
		c.inbound = ctx
		close(c.inboundSet)
		return nil, nil
	case acpkeeper.HandledMethod:
		var n struct {
			Seq uint64 `json:"seq"`
		}
		if kept, ok := c.w.WriteCloser.(keeperWriter); ok && json.Unmarshal(params, &n) == nil {
			// The keeper drops what is acked: it must be in the log.
			c.events.flushText()
			kept.Handled(n.Seq)
		}
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
		parent, ok := c.parentOf(n.SessionID)
		if !ok {
			c.logger.Debug("acphost: update for another session", "session", n.SessionID)
			return nil, nil
		}
		child, isChild := parseSubagent(n.Update)
		if isChild && !c.adopt(n.SessionID, parent, child) {
			return nil, nil
		}
		if c.replaying.Load() && replayedKind(n.Update) {
			if isChild {
				c.events.replayedSubagent(child)
			}
			return nil, nil
		}
		c.events.update(parent, n.Update)
		return nil, nil
	case "_claude/sdkMessage":
		var n struct {
			SessionID string          `json:"sessionId"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(params, &n); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if n.SessionID == c.SessionID() {
			c.events.sdkMessage(n.Message)
		}
		return nil, nil
	case answeredMarker:
		c.events.promptAnswered()
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
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan",
		"subagent_spawned", "subagent_state_update", "subagent_update":
		return true
	}
	return false
}

// parentOf returns the launch id of a session's updates: empty for the
// agent's session, and for any session before session/new answers, when the
// agent may already announce commands. ok is false for an unknown session.
func (c *Conn) parentOf(sid string) (string, bool) {
	if root := c.SessionID(); root == "" || sid == root {
		return "", true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.children[sid]
	return l.launch, ok
}

// adopt registers the subagent session an update from session from
// announces, and reports whether to record the update: a new child, or one
// announced from the same session under the same launch id. A known child
// keeps its parent and no session becomes its own ancestor.
func (c *Conn) adopt(from, parent string, child subagentChild) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.children[child.ID]; ok {
		return l == childLink{child.Launch, parent}
	}
	if !child.Announces || child.ID == from || child.ID == c.SessionID() {
		return false
	}
	for _, l := range c.children {
		if l.launch == child.Launch {
			return false
		}
	}
	if len(c.children) >= maxChildren {
		c.logger.Warn("acphost: too many subagent sessions; dropping the announcement", "session", child.ID, "limit", maxChildren)
		return false
	}
	c.children[child.ID] = childLink{child.Launch, parent}
	close(c.announced)
	c.announced = make(chan struct{})
	return true
}

var errRequestCancelled = errors.New("acphost: the turn was cancelled")

// nextCancel returns a channel closed when pending requests are next
// cancelled, which a request that arrives now must count as.
func (c *Conn) nextCancel() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

// awaitSession returns the launch id of the session a request names, empty
// for the agent's own. The SDK runs each request on its own goroutine while
// notifications queue in order, so a subagent's request can arrive before
// the update announcing its session has been handled. It returns
// errRequestCancelled once cancelled is closed.
func (c *Conn) awaitSession(ctx context.Context, sid string, cancelled <-chan struct{}) (string, error) {
	if sid == c.SessionID() {
		return "", nil
	}
	deadline := time.NewTimer(childAnnounceWait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		l, ok := c.children[sid]
		announced := c.announced
		c.mu.Unlock()
		if ok {
			return l.launch, nil
		}
		select {
		case <-announced:
		case <-cancelled:
			return "", errRequestCancelled
		case <-deadline.C:
			return "", fmt.Errorf("unknown agent conversation %q", sid)
		case <-ctx.Done():
			return "", fmt.Errorf("unknown agent conversation %q", sid)
		}
	}
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
	cancelled := c.nextCancel()
	parent, err := c.awaitSession(ctx, p.SessionID, cancelled)
	switch {
	case errors.Is(err, errRequestCancelled):
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	case err != nil:
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	var tc struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
	}
	_ = json.Unmarshal(p.ToolCall, &tc)
	if u := toolCallUpdateFrom(p.ToolCall); u != nil {
		c.events.update(parent, u)
	}
	req := Request{Kind: RequestPermission, Title: tc.Title, ToolCallID: tc.ToolCallID}
	if req.Title == "" {
		req.Title = "Permission requested"
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, Option{ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	// allow_always can persist a rule into settings the run commits.
	i := slices.IndexFunc(req.Options, func(o Option) bool { return o.Kind == "allow_once" })
	if i < 0 {
		i = slices.IndexFunc(req.Options, func(o Option) bool { return strings.HasPrefix(o.Kind, "allow_") })
	}
	if c.autoAllow && i >= 0 {
		req.ID, req.Status, req.Answer = rand.Text(), RequestAnswered, req.Options[i].ID
		c.events.requestClosed(req)
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": req.Answer}}, nil
	}
	a, ok := c.wait(ctx, req, cancelled)
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
		SessionID       *string         `json:"sessionId"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		URL             string          `json:"url"`
		ToolCallID      string          `json:"toolCallId"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	cancelled := c.nextCancel()
	// A request-scoped elicitation carries no sessionId; a session-scoped
	// one must name this connection's session or a subagent session.
	if p.SessionID != nil {
		_, err := c.awaitSession(ctx, *p.SessionID, cancelled)
		switch {
		case errors.Is(err, errRequestCancelled):
			return map[string]any{"action": "cancel"}, nil
		case err != nil:
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
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
	a, ok := c.wait(ctx, req, cancelled)
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
// turn is cancelled, or the agent withdraws it. ok is false when cancelled,
// also when cancelled closed before the request was registered.
func (c *Conn) wait(ctx context.Context, req Request, cancelled <-chan struct{}) (answer, bool) {
	req.ID = rand.Text()
	req.Status = RequestPending
	c.mu.Lock()
	if i := slices.IndexFunc(c.reuse, func(r Request) bool {
		return r.Kind == req.Kind && r.ToolCallID == req.ToolCallID && r.Title == req.Title
	}); i >= 0 {
		req.ID = c.reuse[i].ID
		c.reuse = slices.Delete(c.reuse, i, i+1)
	}
	p := &pendingRequest{req: req, answer: make(chan answer, 1)}
	select {
	case <-cancelled:
		c.mu.Unlock()
		return answer{}, false
	default:
	}
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

// reuseRequests gives the agent's requests the keeper sends again the ids
// the item log shows them pending under.
func (c *Conn) reuseRequests(pending []Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reuse = pending
}

func (c *Conn) cancelPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, p := range c.pending {
		delete(c.pending, id)
		c.resolved[id] = true
		close(p.answer)
	}
	close(c.cancelled)
	c.cancelled = make(chan struct{})
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
