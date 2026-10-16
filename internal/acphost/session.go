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
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrClosed is returned once the agent connection has ended.
var ErrClosed = errors.New("acphost: agent connection closed")

// ErrPromptUnsent is ErrClosed for a prompt refused before any of it reached
// the agent, so sending it on a new session cannot repeat it.
var ErrPromptUnsent = fmt.Errorf("%w; the prompt was not sent", ErrClosed)

// ErrUnsupportedImage means the agent does not accept image prompts; no input
// was sent or queued.
var ErrUnsupportedImage = errors.New("acphost: agent does not support image prompts")

const subscriberBuffer = 1024

// titleLookupPages bounds how far one title lookup follows session/list: every
// run of a member keeps its sessions under the same working directory.
const titleLookupPages = 10

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
	// SystemPrompt is sent as _meta.systemPrompt.append when the session is
	// created or restored, for an agent that reads it.
	SystemPrompt string
	// ClaudeCode marks Claude's adapter: the session asks it for Claude
	// Code's background task frames, and takes agent output that arrives
	// with no prompt open for a turn the agent runs on its own.
	ClaudeCode bool
	// AgentCommand is the ACP server's executable. A terminal auth method's
	// args belong to it.
	AgentCommand string

	OnState func(TurnState)
	// OnInputs reports the complete set of pending requests whenever it
	// changes and once a restored session opens; an empty set clears them.
	OnInputs func(pending []domain.RunInputRequest)
	// OnActivity reports the ACP tool kind the agent is using, "think" while
	// it thinks, at most once a second.
	OnActivity func(kind, target string)
	OnTitle    func(title string)
	// TitleLookup, when positive, asks session/list for the session's title
	// that long after a turn starts while the agent has reported none. Claude
	// Code titles a session at its first prompt, but its adapter sends
	// session_info_update only when the turn ends.
	TitleLookup time.Duration
}

// TurnState is the session's execution state: working with reason "prompt"
// at every prompt start and "background" when the agent works with no prompt
// open, and idle with the stop reason once a turn ended and no background
// work is left. A restored session reports idle once it opens, with
// "interrupted" when the previous connection left work unfinished.
type TurnState struct {
	Working bool
	Reason  string
	// Tasks counts the background tasks a background state waits for.
	Tasks int
	// Autonomous says a background state's agent runs a turn of its own.
	Autonomous bool
	// Failed is the error a turn's prompt failed with while the agent stayed
	// connected.
	Failed error
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
	// TurnInFlight is true while a prompt or an autonomous turn runs.
	TurnInFlight bool `json:"turn_in_flight"`
	// Background is true while the agent works with no prompt open: an
	// autonomous turn runs or tasks the run waits for are live.
	Background bool `json:"background,omitempty"`
	// Tasks are the live background tasks, as the latest background item
	// records them.
	Tasks         []Task          `json:"tasks,omitempty"`
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
	AgentCommand  string          `json:"agent_command,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
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
	state      State
	actAt      time.Time
	actNext    *[2]string
	actTimer   *time.Timer
	// prompted is the latest prompt's text with whitespace collapsed.
	prompted string

	// autonomous is a turn the agent runs with no prompt from the host;
	// resulted says the agent reported its result, origin what started it.
	autonomous  bool
	resulted    bool
	origin      string
	tasks       map[string]*liveTask
	loggedTasks []Task
	loggedOwed  bool
	// taskCalls holds the last snapshot of each tool call whose task runs;
	// endedTasks the status of tasks that ended before their tool call
	// arrived, oldest first in endedOrder.
	taskCalls  map[string]ToolCall
	endedTasks map[string]string
	endedOrder []string
	lastUpdate time.Time
	// quietAt is when the armed check of the autonomous turn's quiet fires.
	quietAt time.Time
	// owedUntil bounds the wait for the turn reporting back on a task the
	// run waited for; endedInTurn says such a task ended during a turn.
	owedUntil   time.Time
	endedInTurn bool
	// reported is the latest state OnState got; idleStop is the stop reason
	// idle reports once background work drains. holdIdle keeps a failed
	// turn's idle until the agent works again.
	reported TurnState
	idleStop string
	holdIdle bool
	// answers counts session/prompt answers the answer barrier passed,
	// ended the turns whose end is recorded; turnEnded signals ended
	// changing.
	answers, ended int64
	turnEnded      *sync.Cond
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
		cfg:        cfg,
		log:        log,
		logger:     cfg.Logger,
		notify:     newSerialQueue(),
		done:       make(chan struct{}),
		subs:       make(map[chan Item]struct{}),
		tasks:      make(map[string]*liveTask),
		taskCalls:  make(map[string]ToolCall),
		endedTasks: make(map[string]string),
	}
	s.turnEnded = sync.NewCond(&s.mu)
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
	if cfg.ClaudeCode {
		running, err := RunningTaskCalls(log)
		if err != nil {
			_ = w.Close()
			s.stop()
			s.notify.close(nil)
			return nil, errors.Join(fmt.Errorf("acphost: scan running tasks: %w", err), log.Close())
		}
		s.mu.Lock()
		for _, it := range running {
			s.taskCalls[it.ToolCall.Task.ID] = *it.ToolCall
			s.taskEndedLocked(it.ToolCall.Task.ID, "stopped")
		}
		s.interruptTasksLocked(LastItem(log, KindBackground))
		s.mu.Unlock()
	}

	s.conn = newConn(r, w, s, cfg.Logger)
	s.conn.autoAllow = cfg.AutoAllow
	if cfg.SystemPrompt != "" || cfg.ClaudeCode {
		s.conn.sessionMeta = map[string]any{}
	}
	if cfg.SystemPrompt != "" {
		s.conn.sessionMeta["systemPrompt"] = map[string]any{"append": cfg.SystemPrompt}
	}
	if cfg.ClaudeCode {
		// Claude's adapter forwards a subagent's text and thinking only when
		// asked; they arrive stamped with the subagent's parent link.
		s.conn.sessionMeta["claudeCode"] = map[string]any{"emitRawSDKMessages": claudeTaskFrames, "options": map[string]any{"forwardSubagentText": true}}
	}
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
		reason := ""
		if Interrupted(log) {
			reason = "interrupted"
		}
		s.stateLocked(TurnState{Reason: reason})
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
// is called once for a prompt that started, awaited or joined a turn, in the
// order the agent took its prompts: with nil when the agent accepted it, or
// with why it never did. queued says the prompt waited for a turn to end.
func (s *Session) Prompt(ctx context.Context, blocks []acp.ContentBlock, steer bool, delivered func(queued bool, err error)) (Receipt, error) {
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
	if !s.turnActive && !s.autonomous {
		ack := s.startTurnLocked(blocks, report(delivered, false))
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	if !steer || !s.conn.Info().Steering {
		s.queue = append(s.queue, queuedPrompt{blocks, report(delivered, true)})
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeQueued}, nil
	}
	starting := s.starting
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
		// The agent adds input only to a turn whose prompt it accepted. A
		// later turn may have started while the steer was answered.
		if starting != nil && s.starting == starting {
			s.acceptedLocked()
		}
		s.userMessageLocked(blocks)
		if delivered != nil {
			s.callback(func() { delivered(false, nil) })
		}
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
	if !s.turnActive && !s.autonomous {
		ack := s.startTurnLocked(blocks, report(delivered, false))
		s.mu.Unlock()
		return awaitAccept(ctx, ack)
	}
	s.queue = append(s.queue, queuedPrompt{blocks, report(delivered, true)})
	s.mu.Unlock()
	return Receipt{Outcome: OutcomeQueued}, nil
}

func report(delivered func(queued bool, err error), queued bool) func(error) {
	if delivered == nil {
		return nil
	}
	return func(err error) { delivered(queued, err) }
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

// PromptIdle starts a turn only when no prompt or autonomous turn is running
// or owed and no prompt is queued, and reports whether it did once the agent
// accepted the prompt. delivered, if set, is called once for a prompt that
// started a turn, as for Prompt.
func (s *Session) PromptIdle(ctx context.Context, blocks []acp.ContentBlock, delivered func(error)) (bool, error) {
	if err := s.validatePromptImages(blocks); err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.closingLocked() {
		s.mu.Unlock()
		return false, ErrPromptUnsent
	}
	if s.turnActive || s.autonomous || s.owingLocked() || len(s.queue) > 0 {
		s.mu.Unlock()
		return false, nil
	}
	ack := s.startTurnLocked(blocks, delivered)
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
	s.turnActive, s.holdIdle, s.owedUntil = true, false, time.Time{}
	s.emitLocked(Item{Kind: KindTurnStart})
	s.relogTasksLocked()
	s.userMessageLocked(blocks)
	s.stateLocked(TurnState{Working: true, Reason: "prompt"})
	if s.cfg.TitleLookup > 0 && s.proj.title == "" && s.conn.Info().List {
		time.AfterFunc(s.cfg.TitleLookup, s.lookupTitle)
	}
	go s.runTurn(blocks, ack, s.conn.w.sendingPrompt())
	return ack
}

// lookupTitle adopts the title session/list reports for this session. An
// agent lists a session it has not titled under its latest prompt, which is
// not a title.
func (s *Session) lookupTitle() {
	s.mu.Lock()
	prompted := s.prompted
	skip := s.closed || s.proj.title != "" || prompted == ""
	s.mu.Unlock()
	if skip {
		return
	}
	var cursor string
	for range titleLookupPages {
		sessions, next, err := s.conn.listSessions(s.ctx, s.cfg.Cwd, cursor)
		if err != nil {
			if s.ctx.Err() == nil {
				s.logger.Warn("acphost: look up the session title", "err", err)
			}
			return
		}
		i := slices.IndexFunc(sessions, func(listed SessionSummary) bool { return listed.SessionID == s.conn.SessionID() })
		if i >= 0 {
			title := strings.Join(strings.Fields(sessions[i].Title), " ")
			s.mu.Lock()
			if s.proj.title == "" && !strings.HasPrefix(prompted, strings.TrimSpace(strings.TrimSuffix(title, "…"))) {
				s.proj.title = title
				s.emitLocked(Item{Kind: KindSessionInfo, Title: title})
			}
			s.mu.Unlock()
			return
		}
		if next == "" {
			return
		}
		cursor = next
	}
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
		if forAgentOnly(b) {
			continue
		}
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		} else {
			attachments = append(attachments, contentRef(b))
		}
	}
	s.prompted = strings.Join(strings.Fields(text.String()), " ")
	body, cut := cutTail(text.String(), maxTextSegment)
	s.emitLocked(Item{Kind: KindMessage, Truncated: cut, Message: &Message{
		Role: "user", MessageID: rand.Text(), Text: body, Attachments: attachments, Complete: true,
	}})
}

// forAgentOnly reports a block annotated for the agent's audience alone,
// which the transcript does not record.
func forAgentOnly(b acp.ContentBlock) bool {
	var a *acp.Annotations
	switch {
	case b.Text != nil:
		a = b.Text.Annotations
	case b.Resource != nil:
		a = b.Resource.Annotations
	}
	return a != nil && len(a.Audience) > 0 && !slices.Contains(a.Audience, acp.RoleUser)
}

func (s *Session) runTurn(blocks []acp.ContentBlock, ack *turnAck, sent func()) {
	stop, err := s.conn.prompt(s.ctx, blocks)
	sent()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended++
	s.turnEnded.Broadcast()
	if err != nil && s.conn.closed() {
		// Record what the agent sent before it went away ahead of the
		// interruption. Its answer barrier, released above, is among it.
		s.mu.Unlock()
		s.conn.delivered()
		s.mu.Lock()
	}
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
	if reason == "cancelled" {
		s.endedInTurn = false
		s.dismissLocked()
	}
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: reason})
	s.turnActive = false
	if len(s.queue) > 0 && !s.closingLocked() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next.blocks, next.delivered)
		return
	}
	s.followUpLocked()
	s.idleStop = reason
	switch {
	case failed != nil:
		// Tasks still report back; the failure shows until the agent works
		// again.
		s.holdIdle, s.idleStop = true, ""
		s.stateLocked(TurnState{Reason: reason, Failed: failed})
	case reason == "interrupted":
		s.stateLocked(TurnState{Reason: reason})
	default:
		s.settleLocked()
	}
}

// promptAnswered returns once the turn whose session/prompt the agent just
// answered has ended, so the updates the agent sends after its answer are
// not taken for that turn.
func (s *Session) promptAnswered() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers++
	s.awaitTurnEndsLocked(s.answers)
}

// awaitTurnEndsLocked waits until the ends of the first n turns are recorded.
func (s *Session) awaitTurnEndsLocked(n int64) {
	for s.ended < n && !s.closed {
		s.turnEnded.Wait()
	}
}

// Cancel stops the running turn and answers every pending request
// cancelled. Queued prompts still run afterwards. With no prompt open it ends
// the autonomous turn and stops waiting for background tasks' follow-up.
func (s *Session) Cancel(ctx context.Context) error {
	err := s.conn.cancel(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelBackgroundLocked()
	return err
}

// Answer resolves a pending request with one of its option ids. Content is
// the form answer for an accepted question and nil otherwise.
func (s *Session) Answer(requestID, optionID string, content map[string]any) error {
	return s.conn.answer(requestID, optionID, content)
}

// Notice records a notice item. It returns ErrClosed once the session is
// closed, and the log's error when the item was not recorded.
func (s *Session) Notice(severity, title, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	it := Item{Kind: KindNotice, Notice: &Notice{Severity: severity, Title: title, Description: description}, Turn: s.turn}
	if err := s.log.Append(&it); err != nil {
		return fmt.Errorf("acphost: record notice: %w", err)
	}
	s.fanoutLocked(it)
	return nil
}

func (s *Session) ListSessions(ctx context.Context, cwd, cursor string) ([]SessionSummary, string, error) {
	return s.conn.listSessions(ctx, cwd, cursor)
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.TurnInFlight = s.turnActive || s.autonomous
	st.Background = s.backgroundLocked()
	st.Tasks = s.loggedTasks
	st.Queued = len(s.queue)
	st.Pending = s.conn.pendingRequests()
	st.LastActivity = s.conn.LastActivity()
	info := s.conn.Info()
	st.Steering, st.AuthMethods = info.Steering, info.AuthMethods
	st.PromptImages = info.PromptImages
	st.AgentCommand = s.cfg.AgentCommand
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
func (s *Session) Subscribe(afterSeq int64) (SubscriptionReplay, <-chan Item, func(), error) {
	// Capture the replay before releasing mu, so appends cannot leave a gap
	// between the history boundary and registration for live delivery.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return SubscriptionReplay{}, nil, nil, ErrClosed
	}
	replay, read := s.log.beginReplay(afterSeq)
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
	var err error
	replay.Items, err = read.items()
	if err != nil {
		cancel()
		return SubscriptionReplay{}, nil, nil, err
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
	case KindUsage:
		s.state.Usage = it.Usage
	case KindSessionInfo:
		if s.cfg.OnTitle != nil {
			s.callback(func() { s.cfg.OnTitle(it.Title) })
		}
	case KindToolCall:
		s.trackTaskCallLocked(&it)
	}
	it.Turn = s.turn
	s.appendLocked(it)
}

func (s *Session) appendLocked(it Item) {
	if err := s.log.Append(&it); err != nil {
		s.logger.Error("acphost: item not recorded", "kind", it.Kind, "err", err)
		return
	}
	s.fanoutLocked(it)
}

func (s *Session) fanoutLocked(it Item) {
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
	var head updateHead
	_ = json.Unmarshal(raw, &head)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A background subagent streams while the main agent has not yet taken
	// the prompt.
	if head.Meta.ClaudeCode.ParentToolUseID == "" {
		s.acceptedLocked()
	}
	if head.opensTurn() {
		s.openAutonomousLocked()
	}
	s.proj.update(raw)
	s.trackLocked(head)
	s.settleLocked()
}

func (s *Session) authStatus(raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(Item{Kind: KindAuthStatus, Auth: raw})
}

func (s *Session) requestOpened(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Requests are not ordered with the answer barrier's notification.
	s.awaitTurnEndsLocked(s.conn.answers.Load())
	s.acceptedLocked()
	s.openAutonomousLocked()
	s.lastUpdate = time.Now()
	s.proj.flushText()
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
	s.settleLocked()
}

func (s *Session) requestClosed(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
	// The agent resumes only after this returns; its quiet wait starts now.
	s.lastUpdate = time.Now()
	s.settleLocked()
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
	for id := range s.taskCalls {
		s.taskEndedLocked(id, "stopped")
	}
	if held := s.heldTasksLocked(); len(held) > 0 {
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Background work interrupted", Description: "The agent connection closed while these background tasks ran: " + strings.Join(held, ", ")}})
	}
	s.interruptTasksLocked(Item{Tasks: s.loggedTasks, FollowUp: s.loggedOwed})
	if s.turnActive || s.autonomous {
		s.proj.endTurn()
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "The agent connection closed before the turn finished."}})
		s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "interrupted", Autonomous: s.autonomous})
	}
	if s.turnActive || s.backgroundLocked() {
		s.stateLocked(TurnState{Reason: "interrupted"})
	}
	s.turnActive, s.autonomous = false, false
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
	s.turnEnded.Broadcast()
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
