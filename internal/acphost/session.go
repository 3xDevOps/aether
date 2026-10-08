// Package acphost hosts one Agent Client Protocol (ACP) session per run. The
// server is the agent process's only ACP client; viewers read its item log.
package acphost

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrClosed is returned once the agent connection has ended.
var ErrClosed = errors.New("acphost: agent connection closed")

// ErrUnsupportedImage means the agent does not accept image prompts; no input
// was sent or queued.
var ErrUnsupportedImage = errors.New("acphost: agent does not support image prompts")

const subscriberBuffer = 1024

// Config describes the session to host.
type Config struct {
	LogPath string
	// Cwd is the agent's working directory inside its container.
	Cwd string
	// MCPServers is sent on session/new, session/resume and session/load.
	MCPServers []acp.McpServer
	// SessionID is the agent session to restore; empty starts a new one.
	SessionID string
	// RequireRestore fails Start instead of starting a new session when
	// SessionID cannot be restored.
	RequireRestore bool
	Logger         *slog.Logger
	// AutoAllow answers every permission request with allow_once, or else
	// the first allow_* option.
	AutoAllow bool

	// OnState reports execution state: working at every prompt start,
	// idle at every turn end with the stop reason, and idle once a
	// restored session opens. failed is the error a turn's prompt failed
	// with while the agent stayed connected.
	OnState func(working bool, reason string, failed error)
	// OnInputs reports the complete set of pending requests whenever it
	// changes and once a restored session opens; an empty set clears them.
	OnInputs func(pending []domain.RunInputRequest)
	// OnActivity reports the ACP tool kind the agent is using, "think" while
	// it thinks, at most once a second.
	OnActivity func(kind, target string)
}

// Receipt says what happened to a prompt.
type Receipt struct {
	Outcome string `json:"outcome"`
}

const (
	// OutcomeSent: the prompt started a turn.
	OutcomeSent = "sent"
	// OutcomeQueued: a turn is running; the prompt starts the next one.
	OutcomeQueued = "queued"
	// OutcomeInjected: the agent added the prompt to the running turn.
	OutcomeInjected = "injected"
)

// State is a snapshot of the session for a viewer that just connected.
type State struct {
	TurnInFlight  bool            `json:"turn_in_flight"`
	Queued        int             `json:"queued"`
	Pending       []Request       `json:"pending"`
	LastActivity  time.Time       `json:"last_activity"`
	Mode          string          `json:"mode,omitempty"`
	ConfigOptions json.RawMessage `json:"config_options,omitempty"`
	Commands      json.RawMessage `json:"commands,omitempty"`
	Auth          json.RawMessage `json:"auth,omitempty"`
	Steering      bool            `json:"steering,omitempty"`
	PromptImages  bool            `json:"prompt_images"`
	AuthMethods   json.RawMessage `json:"auth_methods,omitempty"`
}

// promptAcceptGrace is how long a prompt that starts a turn waits for the
// agent to refuse it before it counts as sent. An agent that is working
// streams an update well within it.
const promptAcceptGrace = 1500 * time.Millisecond

// turnAck resolves when the agent accepts a turn's prompt (its first update
// or request) or answers it.
type turnAck struct {
	done      chan struct{}
	err       error
	delivered func(error)
}

func (s *Session) resolveLocked(a *turnAck, err error) {
	select {
	case <-a.done:
		return
	default:
	}
	if err != nil && s.closingLocked() {
		err = ErrClosed
	}
	a.err = err
	close(a.done)
	if a.delivered == nil {
		return
	}
	if err != nil && !errors.Is(err, ErrClosed) {
		err = fmt.Errorf("acphost: the agent refused the prompt: %w", err)
	}
	s.callback(func() { a.delivered(err) })
}

func (s *Session) closingLocked() bool {
	return s.closed || s.hostClosed || s.conn.closed() || s.conn.inputBroken()
}

type queuedPrompt struct {
	blocks    []acp.ContentBlock
	delivered func(error)
}

// Session ties an agent connection to its item log and viewers.
type Session struct {
	cfg    Config
	conn   *Conn
	log    *Log
	logger *slog.Logger
	notify *serialQueue
	ctx    context.Context
	stop   context.CancelFunc
	done   chan struct{}

	mu sync.Mutex
	// Serialize option RPCs without holding mu, which inbound updates need
	// before the SDK can finish the corresponding response.
	optionMu   sync.Mutex
	options    optionCatalog
	proj       *projector
	turn       int64
	turnActive bool
	starting   *turnAck
	queue      []queuedPrompt
	subs       map[chan Item]struct{}
	closed     bool
	hostClosed bool
	capped     bool
	state      State
	actAt      time.Time
	actNext    *[2]string
	actTimer   *time.Timer
}

// Start initializes the agent over r and w, restores or creates its session
// and returns once the session is ready. On error it closes w.
func Start(ctx context.Context, r io.Reader, w io.WriteCloser, cfg Config) (*Session, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	log, err := OpenLog(cfg.LogPath)
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	s := &Session{
		cfg:    cfg,
		log:    log,
		logger: cfg.Logger,
		notify: newSerialQueue(),
		done:   make(chan struct{}),
		subs:   make(map[chan Item]struct{}),
	}
	s.ctx, s.stop = context.WithCancel(context.Background())
	s.proj = newProjector(s.projectLocked, s.armFlush, s.activityLocked)
	go s.notify.run()

	last, _, openTurn := log.state()
	s.turn = last.Turn
	if openTurn {
		s.mu.Lock()
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "The agent connection ended before the turn finished."}})
		s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "interrupted"})
		s.mu.Unlock()
	}

	s.conn = newConn(r, w, s, cfg.Logger)
	s.conn.autoAllow = cfg.AutoAllow
	if err := s.open(ctx); err != nil {
		_ = w.Close()
		s.stop()
		s.notify.close(nil)
		return nil, errors.Join(err, log.Close())
	}
	if cfg.SessionID != "" {
		// The connection that held the restored session's turn and
		// requests may have ended without reporting their end.
		s.mu.Lock()
		s.inputsLocked()
		if s.cfg.OnState != nil {
			s.callback(func() { s.cfg.OnState(false, "", nil) })
		}
		s.mu.Unlock()
	}
	go s.watch()
	return s, nil
}

func (s *Session) open(ctx context.Context) error {
	if err := s.conn.initialize(ctx); err != nil {
		return err
	}
	info := s.conn.Info()
	var res sessionResult
	var restoreErr error
	if id := s.cfg.SessionID; id != "" {
		restoreErr = errors.New("acphost: the agent supports neither session/resume nor session/load")
		if info.Resume {
			res, restoreErr = s.conn.resumeSession(ctx, id, s.cfg.Cwd, s.cfg.MCPServers)
		}
		if restoreErr != nil && info.LoadSession {
			res, restoreErr = s.conn.loadSession(ctx, id, s.cfg.Cwd, s.cfg.MCPServers)
		}
		if restoreErr != nil && s.cfg.RequireRestore {
			return fmt.Errorf("restore session %s: %w", id, restoreErr)
		}
	}
	if s.cfg.SessionID == "" || restoreErr != nil {
		var err error
		if res, err = s.conn.newSession(ctx, s.cfg.Cwd, s.cfg.MCPServers); err != nil {
			return errors.Join(restoreErr, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if restoreErr != nil {
		s.emitLocked(Item{Kind: KindReset})
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{
			Severity:    "warning",
			Title:       "Started a new agent session",
			Description: fmt.Sprintf("Session %s could not be restored: %v", s.cfg.SessionID, restoreErr),
		}})
	}
	s.options.model = legacyOptions(res.Models, "availableModels", "modelId", "currentModelId")
	s.options.mode = legacyOptions(res.Modes, "availableModes", "id", "currentModeId")
	// Resume/load may have already delivered config updates. If the response
	// omits configOptions, retain those rather than resetting their selections
	// to the legacy current IDs.
	options := res.ConfigOptions
	if !present(options) {
		options = s.options.native
	}
	if s.options.mode.current == "" {
		s.options.mode.current = s.state.Mode
	}
	s.replaceOptionsLocked(options)
	return nil
}

// SessionID is the agent session id to store for a later restore.
func (s *Session) SessionID() string { return s.conn.SessionID() }

func (s *Session) Info() AgentInfo { return s.conn.Info() }

func (s *Session) Log() *Log { return s.log }

// Done is closed once the agent connection ended, everything the agent sent
// and the interruption (if any) are recorded, and every callback has run.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close closes the agent's stdin. The adapter exits on EOF; Done follows
// once its output ends.
func (s *Session) Close() error {
	s.mu.Lock()
	s.hostClosed = true
	s.mu.Unlock()
	return s.conn.Close()
}

// Prompt sends input to the agent. With no turn running it starts one and
// returns once the agent accepted it; a refusal is an error. While a turn
// runs, steer asks the agent to add the input to that turn if it advertises
// steering; otherwise the input waits for the turn to end. delivered, if set,
// is called once for a queued prompt: with nil when the agent accepts it, or
// with why it was never delivered.
func (s *Session) Prompt(ctx context.Context, blocks []acp.ContentBlock, steer bool, delivered func(error)) (Receipt, error) {
	if len(blocks) == 0 {
		return Receipt{}, errors.New("acphost: empty prompt")
	}
	if err := s.validatePromptImages(blocks); err != nil {
		return Receipt{}, err
	}
	s.mu.Lock()
	if s.closingLocked() {
		s.mu.Unlock()
		return Receipt{}, ErrClosed
	}
	if !s.turnActive {
		ack := s.startTurnLocked(blocks, nil)
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	if !steer || !s.conn.Info().Steering {
		s.queue = append(s.queue, queuedPrompt{blocks, delivered})
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeQueued}, nil
	}
	s.mu.Unlock()

	outcome, err := s.conn.steer(ctx, blocks)
	if err != nil {
		return Receipt{}, err
	}
	var cancelErr error
	if outcome == "startedNewTurn" {
		// The agent ignored idleBehavior promptRequired and ran the input as
		// a turn of its own. ACP reports a turn's end only in the
		// session/prompt response, so the host cannot see that turn end,
		// and its next session/prompt would reach a busy agent. Stop it.
		cancelErr = s.conn.cancel(ctx)
	}
	s.mu.Lock()
	switch outcome {
	case OutcomeInjected:
		s.userMessageLocked(blocks)
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeInjected}, nil
	case "startedNewTurn":
		// The agent has the input; re-sending it would deliver it twice.
		s.userMessageLocked(blocks)
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{
			Severity:    "warning",
			Title:       "Steered message stopped",
			Description: "The agent started a turn of its own for this message instead of adding it to the running turn, so Aether cancelled that turn. The agent has the message; send a new prompt to continue.",
		}})
		s.mu.Unlock()
		return Receipt{}, errors.Join(fmt.Errorf("acphost: %s: the agent answered startedNewTurn despite idleBehavior promptRequired; its turn was cancelled", methodSteering), cancelErr)
	case "promptRequired":
		// The turn ended before the steer reached the agent.
	default:
		s.mu.Unlock()
		return Receipt{}, fmt.Errorf("acphost: %s: unknown outcome %q", methodSteering, outcome)
	}
	if s.closingLocked() {
		s.mu.Unlock()
		return Receipt{}, ErrClosed
	}
	if !s.turnActive {
		ack := s.startTurnLocked(blocks, nil)
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	s.queue = append(s.queue, queuedPrompt{blocks, delivered})
	s.mu.Unlock()
	return Receipt{Outcome: OutcomeQueued}, nil
}

// awaitAccept reports a turn's prompt sent once the agent accepted it, or
// once promptAcceptGrace passed without a refusal.
func awaitAccept(ctx context.Context, ack *turnAck) (Receipt, error) {
	grace := time.NewTimer(promptAcceptGrace)
	defer grace.Stop()
	select {
	case <-ack.done:
		if ack.err != nil {
			return Receipt{}, fmt.Errorf("acphost: the agent refused the prompt: %w", ack.err)
		}
	case <-grace.C:
	case <-ctx.Done():
		return Receipt{}, ctx.Err()
	}
	return Receipt{Outcome: OutcomeSent}, nil
}

// PromptIdle starts a turn only when no turn is running and no prompt is
// queued, and reports whether it did once the agent accepted the prompt.
func (s *Session) PromptIdle(ctx context.Context, blocks []acp.ContentBlock) (bool, error) {
	if err := s.validatePromptImages(blocks); err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.closingLocked() {
		s.mu.Unlock()
		return false, ErrClosed
	}
	if s.turnActive || len(s.queue) > 0 {
		s.mu.Unlock()
		return false, nil
	}
	ack := s.startTurnLocked(blocks, nil)
	s.mu.Unlock()
	if _, err := awaitAccept(ctx, ack); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Session) validatePromptImages(blocks []acp.ContentBlock) error {
	if !s.conn.Info().PromptImages {
		for _, block := range blocks {
			if block.Image != nil {
				return ErrUnsupportedImage
			}
		}
	}
	return nil
}

func (s *Session) startTurnLocked(blocks []acp.ContentBlock, delivered func(error)) *turnAck {
	ack := &turnAck{done: make(chan struct{}), delivered: delivered}
	s.starting = ack
	s.turn++
	s.turnActive = true
	s.emitLocked(Item{Kind: KindTurnStart})
	s.userMessageLocked(blocks)
	s.callback(func() {
		if s.cfg.OnState != nil {
			s.cfg.OnState(true, "prompt", nil)
		}
	})
	go s.runTurn(blocks, ack, s.conn.w.sendingPrompt())
	return ack
}

// acceptedLocked resolves the starting turn's prompt as accepted.
func (s *Session) acceptedLocked() {
	if s.starting != nil {
		s.resolveLocked(s.starting, nil)
		s.starting = nil
	}
}

func (s *Session) userMessageLocked(blocks []acp.ContentBlock) {
	s.proj.closeText()
	var text strings.Builder
	var attachments []Content
	for _, b := range blocks {
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		} else {
			attachments = append(attachments, contentRef(b))
		}
	}
	body, cut := cutTail(text.String(), maxTextSegment)
	s.emitLocked(Item{Kind: KindMessage, Truncated: cut, Message: &Message{
		Role: "user", MessageID: rand.Text(), Text: body, Attachments: attachments, Complete: true,
	}})
}

func (s *Session) runTurn(blocks []acp.ContentBlock, ack *turnAck, sent func()) {
	stop, err := s.conn.prompt(s.ctx, blocks)
	sent()
	if err != nil && s.conn.closed() {
		// Record what the agent sent before it went away ahead of the
		// interruption.
		s.conn.delivered()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveLocked(ack, err)
	if s.starting == ack {
		s.starting = nil
	}
	if s.closed {
		return
	}
	s.proj.endTurn()
	reason := stop
	var failed error
	switch {
	case err != nil && (s.conn.closed() || s.conn.inputBroken()):
		reason = "interrupted"
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: err.Error()}})
	case s.hostClosed && (err != nil || stop == "cancelled"):
		// The agent answered its input closing; a restore must resume the
		// turn, not take it as cancelled or failed.
		reason = "interrupted"
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "Aether closed the agent connection before the turn finished."}})
	case err != nil:
		reason, failed = "error", err
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Prompt failed", Description: err.Error()}})
	}
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: reason})
	s.turnActive = false
	if len(s.queue) > 0 && !s.closingLocked() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next.blocks, next.delivered)
		return
	}
	s.callback(func() {
		if s.cfg.OnState != nil {
			s.cfg.OnState(false, reason, failed)
		}
	})
}

// Cancel stops the running turn and answers every pending request
// cancelled. Queued prompts still run afterwards.
func (s *Session) Cancel(ctx context.Context) error { return s.conn.cancel(ctx) }

// Answer resolves a pending request with one of its option ids. Content is
// the form answer for an accepted question and nil otherwise.
func (s *Session) Answer(requestID, optionID string, content map[string]any) error {
	return s.conn.answer(requestID, optionID, content)
}

// SetOption sets an advertised option, using the legacy API only for selectors
// synthesized from the agent's models or modes.
func (s *Session) SetOption(ctx context.Context, configID string, value any) error {
	s.optionMu.Lock()
	defer s.optionMu.Unlock()
	s.mu.Lock()
	category, err := s.options.selection(configID, value)
	nativeRevision := s.options.nativeRevision
	modeRevision := s.options.mode.revision
	var revision uint64
	if category != "" {
		revision = s.options.legacy(category).revision
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if category == "" {
		opts, optionErr := s.conn.setOption(ctx, configID, value)
		if optionErr != nil {
			return optionErr
		}
		if present(opts) {
			s.mu.Lock()
			// Full catalog notifications own the entire snapshot. A mode-only
			// notification owns just mode, not the other accepted response values.
			if s.options.nativeRevision == nativeRevision {
				mode, modeChanged := s.options.mode.current, s.options.mode.revision != modeRevision
				s.options.replace(opts)
				if modeChanged {
					s.options.setCurrent("mode", mode)
				}
				s.publishOptionsLocked()
			}
			s.mu.Unlock()
		}
		return nil
	}
	selected := value.(string) // selection checked the legacy value and type.
	if category == "model" {
		err = s.conn.setModel(ctx, selected)
	} else {
		err = s.conn.setMode(ctx, selected)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Notifications can supply the authoritative selection during the RPC.
	// Only fill in the requested value when the agent did not send one.
	if s.options.legacy(category).revision == revision {
		if category == "mode" {
			s.emitLocked(Item{Kind: KindModeChange, Mode: selected})
		} else {
			s.options.setCurrent(category, selected)
			s.emitOptionsLocked()
		}
	}
	return nil
}

// SetMode switches the agent's mode for agents that expose modes but no
// mode config option.
func (s *Session) SetMode(ctx context.Context, modeID string) error {
	s.optionMu.Lock()
	defer s.optionMu.Unlock()
	s.mu.Lock()
	revision := s.options.mode.revision
	s.mu.Unlock()
	if err := s.conn.setMode(ctx, modeID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.options.mode.revision == revision {
		s.emitLocked(Item{Kind: KindModeChange, Mode: modeID})
	}
	return nil
}

func (s *Session) ListSessions(ctx context.Context, cwd, cursor string) ([]SessionSummary, string, error) {
	return s.conn.listSessions(ctx, cwd, cursor)
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.TurnInFlight = s.turnActive
	st.Queued = len(s.queue)
	st.Pending = s.conn.pendingRequests()
	st.LastActivity = s.conn.LastActivity()
	info := s.conn.Info()
	st.Steering, st.AuthMethods = info.Steering, info.AuthMethods
	st.PromptImages = info.PromptImages
	return st
}

// ReplayWindow is the most items a subscribe replays; older items are paged
// with run.acp.history.
const ReplayWindow = 200

// ReplayStart is the cursor a subscribe replays from: afterSeq, or the start
// of the newest ReplayWindow items when afterSeq is older than that.
func ReplayStart(afterSeq, last int64) int64 { return max(afterSeq, last-ReplayWindow) }

// Subscribe returns the items after ReplayStart(afterSeq) and a channel of
// every item appended from then on, with no gap between the two. A subscriber that
// falls more than subscriberBuffer items behind has its channel closed and
// resubscribes from its last seq. The channel is closed when the session
// ends or cancel is called.
func (s *Session) Subscribe(afterSeq int64) ([]Item, <-chan Item, func(), error) {
	// The replay is read outside the session lock: holding it would stall
	// session/update handling, and the SDK closes the agent connection when
	// its notification queue overflows.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, nil, ErrClosed
	}
	last := s.log.LastSeq()
	ch := make(chan Item, subscriberBuffer)
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
	}
	afterSeq = ReplayStart(afterSeq, last)
	if afterSeq >= last {
		return nil, ch, cancel, nil
	}
	// Sequence numbers are contiguous from 1, so this stops at last, where
	// the channel takes over.
	replay, err := s.log.ReadAfter(afterSeq, int(last-max(afterSeq, 0)))
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return replay, ch, cancel, nil
}

func (s *Session) emitLocked(it Item) {
	if s.closed {
		return
	}
	switch it.Kind {
	case KindModeChange:
		s.state.Mode = it.Mode
		s.options.setCurrent("mode", it.Mode)
		s.emitOptionsLocked()
	case KindConfigOptions:
		s.state.ConfigOptions = it.ConfigOptions
	case KindCommands:
		s.state.Commands = it.Commands
	case KindAuthStatus:
		s.state.Auth = it.Auth
	}
	it.Turn = s.turn
	if _, size, _ := s.log.state(); size >= MaxRunBytes && !it.essential() {
		if !s.capped {
			s.capped = true
			s.appendLocked(Item{Kind: KindNotice, Turn: s.turn, Notice: &Notice{
				Severity: "warning",
				Title:    "Session log is full",
				Description: fmt.Sprintf("The item log reached %d MiB; only requests and turn boundaries are recorded from here.",
					MaxRunBytes>>20),
			}})
		}
		return
	}
	s.appendLocked(it)
}

func (s *Session) appendLocked(it Item) {
	if err := s.log.Append(&it); err != nil {
		s.logger.Error("acphost: item not recorded", "kind", it.Kind, "err", err)
		return
	}
	for ch := range s.subs {
		select {
		case ch <- it:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
}

func (s *Session) armFlush() {
	time.AfterFunc(textFlushInterval, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.proj.flushText()
	})
}

func (s *Session) activityLocked(kind, target string) {
	if s.cfg.OnActivity == nil {
		return
	}
	now := time.Now()
	if wait := s.actAt.Add(time.Second).Sub(now); wait > 0 {
		s.actNext = &[2]string{kind, target}
		if s.actTimer == nil {
			s.actTimer = time.AfterFunc(wait, s.flushActivity)
		}
		return
	}
	s.actAt = now
	s.callback(func() { s.cfg.OnActivity(kind, target) })
}

func (s *Session) flushActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actTimer = nil
	if next := s.actNext; next != nil && !s.closed {
		s.actNext = nil
		s.actAt = time.Now()
		s.callback(func() { s.cfg.OnActivity(next[0], next[1]) })
	}
}

// callback runs f after every earlier callback, outside the session lock.
func (s *Session) callback(f func()) { s.notify.push(f) }

func (s *Session) inputsLocked() {
	if s.closed || s.cfg.OnInputs == nil {
		return
	}
	pending := s.conn.pendingRequests()
	inputs := make([]domain.RunInputRequest, len(pending))
	for i, r := range pending {
		in := domain.RunInputRequest{ID: r.ID, SessionID: s.conn.SessionID(), Kind: "question"}
		if r.Kind == RequestPermission {
			in.Kind = "permission"
		}
		inputs[i] = in
	}
	s.callback(func() { s.cfg.OnInputs(inputs) })
}

func (s *Session) update(raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acceptedLocked()
	s.proj.update(raw)
}

func (s *Session) authStatus(raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(Item{Kind: KindAuthStatus, Auth: raw})
}

func (s *Session) requestOpened(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acceptedLocked()
	s.proj.flushText()
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
}

func (s *Session) requestClosed(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
}

// watch records the end of the connection: an interrupted turn, cancelled
// requests, undelivered queued prompts, cleared inputs and an idle state.
func (s *Session) watch() {
	<-s.conn.Done()
	s.conn.delivered()
	s.stop()
	s.conn.drain()
	s.mu.Lock()
	if s.starting != nil {
		s.resolveLocked(s.starting, ErrClosed)
		s.starting = nil
	}
	if s.turnActive {
		s.proj.endTurn()
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "The agent connection closed before the turn finished."}})
		s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "interrupted"})
		s.turnActive = false
		s.callback(func() {
			if s.cfg.OnState != nil {
				s.cfg.OnState(false, "interrupted", nil)
			}
		})
	}
	for _, queued := range s.queue {
		if queued.delivered != nil {
			s.callback(func() { queued.delivered(ErrClosed) })
		}
		var text strings.Builder
		for _, b := range queued.blocks {
			if b.Text != nil {
				text.WriteString(b.Text.Text)
			}
		}
		body, _ := cutTail(text.String(), maxTextSegment)
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Message not delivered: agent connection closed", Description: body}})
	}
	if s.cfg.OnInputs != nil {
		s.callback(func() { s.cfg.OnInputs(nil) })
	}
	s.closed = true
	s.queue = nil
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	if s.actTimer != nil {
		s.actTimer.Stop()
	}
	if err := s.log.Close(); err != nil {
		s.logger.Error("acphost: close item log", "err", err)
	}
	s.mu.Unlock()
	s.notify.close(s.done)
}

// serialQueue runs callbacks one at a time in push order on its own
// goroutine, so callbacks may call back into the Session.
type serialQueue struct {
	mu     sync.Mutex
	fns    []func()
	closed bool
	done   chan struct{}
	wake   chan struct{}
}

func newSerialQueue() *serialQueue { return &serialQueue{wake: make(chan struct{}, 1)} }

func (q *serialQueue) push(f func()) {
	q.mu.Lock()
	q.fns = append(q.fns, f)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// close stops the queue after the callbacks already pushed and then closes
// done, if given.
func (q *serialQueue) close(done chan struct{}) {
	q.mu.Lock()
	q.closed, q.done = true, done
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *serialQueue) run() {
	for {
		q.mu.Lock()
		fns, closed, done := q.fns, q.closed, q.done
		q.fns = nil
		q.mu.Unlock()
		for _, f := range fns {
			f()
		}
		if len(fns) > 0 {
			continue
		}
		if closed {
			if done != nil {
				close(done)
			}
			return
		}
		<-q.wake
	}
}
