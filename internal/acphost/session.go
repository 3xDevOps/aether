// Package acphost hosts one Agent Client Protocol (ACP) session per run. The
// server is the agent process's only ACP client; viewers read its item log.
package acphost

import (
	"context"
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
	// OnSpend reports the agent's running cost at each turn's end, and at
	// once for a report with no turn open or a new session.
	OnSpend func(Spend)
	// Cost is the running cost of session SessionID that the run was last
	// charged up to. A restored total below it counts from zero.
	Cost float64
	// TitleLookup, when positive, asks session/list for the session's title
	// that long after a turn starts while the agent has reported none. Claude
	// Code titles a session at its first prompt, but its adapter sends
	// session_info_update only when the turn ends.
	TitleLookup time.Duration
	// Adopt takes over the session an earlier server process opened through
	// the agent's keeper instead of initializing the agent and opening one.
	Adopt *Adoption
}

// TurnState is the session's execution state: working with reason "prompt"
// at every prompt start and "background" when the agent works with no prompt
// open, and idle with the stop reason once a turn ended and no background
// work is left. A restored session reports idle once it opens, with
// "interrupted" when the previous connection left work unfinished.
type TurnState struct {
	Working bool
	Reason  string
	// Tasks counts the background tasks a background state waits for, and
	// Agents those of them that are subagents or workflows.
	Tasks  int
	Agents int
	// Autonomous says a background state's agent runs a turn of its own.
	Autonomous bool
	// Failed is the error a turn's prompt failed with while the agent stayed
	// connected.
	Failed error
	// TurnStartedAt is when the turn the state belongs to started, before
	// its prompt reached the agent: the state's callback can run after the
	// agent already acted on the prompt.
	TurnStartedAt time.Time
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
	// LastProgress is when the agent last did work in this connection, zero
	// before it did: a message, thought, tool call, plan, subagent,
	// compaction, background task progress or request. Aether's own calls,
	// a session/load replay and mode, command and usage updates are not
	// work.
	LastProgress time.Time `json:"last_progress,omitzero"`
	OpenedAt     time.Time `json:"opened_at"`
	// TurnStartedAt is when the latest prompt or autonomous turn started.
	TurnStartedAt time.Time `json:"turn_started_at,omitzero"`
	// ToolsOpen is true while a tool call of the turn is pending or running,
	// Compacting while the agent compacts its context.
	ToolsOpen  bool `json:"tools_open,omitempty"`
	Compacting bool `json:"compacting,omitempty"`
	// Closed is true once the agent connection ended or its input broke.
	Closed bool `json:"-"`
}

// promptAcceptGrace is how long a prompt that starts a turn waits for the
// agent to refuse it before it counts as sent. An agent that is working
// streams an update well within it.
const promptAcceptGrace = 1500 * time.Millisecond

func (s *Session) closingLocked() bool {
	return s.closed || s.hostClosed || s.detached || s.conn.closed() || s.conn.inputBroken()
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
	// detached ends the session without recording its end: the agent keeps
	// the turn, and the next server process adopts it. silent is set once
	// a detached connection ended and what it delivered is recorded, and
	// stops every item and callback after.
	detached, silent bool
	state            State
	actAt            time.Time
	actNext          *[2]string
	actTimer         *time.Timer
	// prompted is the latest prompt's text with whitespace collapsed;
	// message is that prompt as the member wrote it.
	prompted string
	message  UserMessage
	spend    Spend
	spendDue bool

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
	progressAt time.Time
	openedAt   time.Time
	turnAt     time.Time
	compacting bool
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
		spend:      Spend{Total: cfg.Cost},
		openedAt:   time.Now(),
	}
	s.turnEnded = sync.NewCond(&s.mu)
	s.ctx, s.stop = context.WithCancel(context.Background())
	s.proj = newProjector(s.projectLocked, s.armFlush, s.activityLocked)
	go s.notify.run()

	last, _, openTurn := log.state()
	s.turn = last.Turn
	adopt := cfg.Adopt
	if openTurn && adopt == nil {
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
			if adopt == nil {
				s.taskEndedLocked(it.ToolCall.Task.ID, "stopped")
			}
		}
		if adopt != nil {
			s.adoptTasksLocked(LastItem(log, KindBackground))
		} else {
			s.interruptTasksLocked(LastItem(log, KindBackground))
		}
		s.mu.Unlock()
	}

	var adopted chan struct{}
	if adopt != nil {
		// The keeper replays the agent's output at once; it waits for the
		// adopted session's id, the agent's info and the pending request ids.
		adopted = make(chan struct{})
		r = gatedReader{open: adopted, r: r}
	}
	s.conn = newConn(r, w, s, cfg.Logger)
	s.conn.autoAllow = cfg.AutoAllow
	// With subagent sessions, Claude's adapter withholds the Agent and Task
	// tool calls whose results background task tracking reads.
	s.conn.childSessions = !cfg.ClaudeCode
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
	open := s.open
	if adopt != nil {
		open = func(context.Context) error {
			defer close(adopted)
			return s.adopt(openTurn)
		}
	}
	if err := open(ctx); err != nil {
		_ = w.Close()
		s.stop()
		s.notify.close(nil)
		return nil, errors.Join(err, log.Close())
	}
	if cfg.SessionID != "" && adopt == nil {
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
	if s.cfg.SessionID == "" || restoreErr != nil {
		s.spend, s.spendDue = Spend{FromZero: true}, true
		s.reportSpendLocked()
	}
	if restoreErr != nil {
		s.emitLocked(Item{Kind: KindReset})
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{
			Severity:    "warning",
			Title:       "Started a new agent session",
			Description: fmt.Sprintf("Session %s could not be restored: %v", s.cfg.SessionID, restoreErr),
		}})
	}
	s.sessionOptionsLocked(res)
	return nil
}

// sessionOptionsLocked takes the agent's models, modes and options from its
// session/new, session/resume or session/load result.
func (s *Session) sessionOptionsLocked(res sessionResult) {
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
	return s.Record(Notice{Severity: severity, Title: title, Description: description})
}

// Record writes n as a notice item of the current turn.
func (s *Session) Record(n Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.silent {
		return ErrClosed
	}
	it := Item{Kind: KindNotice, Notice: &n, Turn: s.turn}
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
	st.LastProgress, st.OpenedAt, st.TurnStartedAt = s.progressAt, s.openedAt, s.turnAt
	st.ToolsOpen, st.Compacting = s.proj.toolsOpen(), s.compacting
	st.Closed = s.closingLocked()
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
	if s.closed || s.silent {
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
		// Claude Code reports a zero total for a crashed turn and then
		// continues from the total its transcript saved. A lower total counts
		// from zero after a reset the session did not see, so the total before
		// it is reported first.
		if u := it.Usage; u.Cost != nil && *u.Cost > 0 && u.Currency == "USD" {
			if *u.Cost < s.spend.Total {
				s.reportSpendLocked()
				s.spend.FromZero = true
			}
			s.spend.Total, s.spendDue = *u.Cost, true
			if !s.turnActive && !s.autonomous {
				s.reportSpendLocked()
			}
		}
	case KindTurnEnd:
		s.reportSpendLocked()
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

func (s *Session) armFlush() { time.AfterFunc(textFlushInterval, s.flushText) }

func (s *Session) flushText() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proj.flushText()
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
func (s *Session) callback(f func()) {
	if !s.silent {
		s.notify.push(f)
	}
}

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

func (s *Session) update(parent string, raw json.RawMessage) {
	var head updateHead
	_ = json.Unmarshal(raw, &head)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A background subagent streams while the main agent has not yet taken
	// the prompt.
	main := parent == "" && head.Meta.ClaudeCode.ParentToolUseID == ""
	if main {
		s.acceptedLocked()
	}
	if main && head.opensTurn() {
		s.openAutonomousLocked()
	}
	if !s.conn.replaying.Load() {
		if head.progress() {
			s.progressAt = time.Now()
		}
		if head.Kind == "compaction_update" {
			s.compacting = head.Status == "in_progress"
		}
	}
	s.proj.update(parent, raw)
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
	s.lastUpdate, s.progressAt = time.Now(), time.Now()
	s.proj.flushText()
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
	s.settleLocked()
}

func (s *Session) requestClosed(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached && r.Status == RequestCancelled {
		// The connection ended under it; the agent still waits for the
		// answer, which the next server process asks for.
		return
	}
	s.emitLocked(Item{Kind: KindRequest, Request: &r})
	s.inputsLocked()
	// The agent resumes only after this returns; its quiet wait starts now.
	s.lastUpdate, s.progressAt = time.Now(), time.Now()
	s.settleLocked()
}

// watch records the end of the connection: an interrupted turn, cancelled
// requests, undelivered queued prompts, cleared inputs and an idle state.
func (s *Session) watch() {
	<-s.conn.Done()
	s.conn.delivered()
	s.mu.Lock()
	s.silent = s.detached
	s.mu.Unlock()
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
	s.turnActive, s.autonomous, s.compacting = false, false, false
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
