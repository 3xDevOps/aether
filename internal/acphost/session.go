// Package acphost hosts one Agent Client Protocol (ACP) session per run. The
// server is the agent process's only ACP client: it owns the process's stdin
// and stdout, projects everything the agent sends into a per-run item log,
// and fans that log out to any number of viewers.
//
// Invariants:
//   - One client per agent process. Viewers never speak ACP; they read items
//     and act through the Session.
//   - Prompts are serialized. A second session/prompt is never sent while one
//     is in flight: mid-turn input is steered where the agent supports it and
//     queued until the turn ends otherwise.
//   - Answers are forwarded verbatim. A permission answer is the option id the
//     agent offered; the first answer to a request wins.
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
var ErrClosed = errors.New("acphost: session closed")

const subscriberBuffer = 1024

// Config describes the session to host.
type Config struct {
	// LogPath is the run's item log file.
	LogPath string
	// Cwd is the agent's working directory inside its container.
	Cwd string
	// MCPServers is sent on session/new, session/resume and session/load.
	MCPServers []acp.McpServer
	// SessionID is the agent session to restore; empty starts a new one.
	SessionID string
	Logger    *slog.Logger

	// OnState reports execution state: working at every prompt start,
	// idle at every turn end with the stop reason.
	OnState func(working bool, reason string)
	// OnInputs reports the complete set of pending requests whenever it
	// changes; an empty set clears them.
	OnInputs func(pending []domain.RunInputRequest)
	// OnActivity reports what the agent is doing, at most once a second.
	OnActivity func(verb, target string)
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

	mu         sync.Mutex
	proj       *projector
	turn       int64
	turnActive bool
	queue      [][]acp.ContentBlock
	subs       map[chan Item]struct{}
	closed     bool
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
	s.proj = newProjector(s.emitLocked, s.armFlush, s.activityLocked)
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
	if err := s.open(ctx); err != nil {
		_ = w.Close()
		s.stop()
		s.notify.close(nil)
		return nil, errors.Join(err, log.Close())
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
	if mode := res.currentMode(); mode != "" {
		s.emitLocked(Item{Kind: KindModeChange, Mode: mode})
	}
	if present(res.ConfigOptions) {
		s.emitLocked(Item{Kind: KindConfigOptions, ConfigOptions: res.ConfigOptions})
	}
	return nil
}

// SessionID is the agent session id to store for a later restore.
func (s *Session) SessionID() string { return s.conn.SessionID() }

// Info is what the agent advertised in initialize.
func (s *Session) Info() AgentInfo { return s.conn.Info() }

// Log is the run's item log, for history reads.
func (s *Session) Log() *Log { return s.log }

// Done is closed once the agent connection ended, everything the agent sent
// and the interruption (if any) are recorded, and every callback has run.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close closes the agent's stdin. The adapter exits on EOF; Done follows
// once its output ends.
func (s *Session) Close() error { return s.conn.Close() }

// Prompt sends input to the agent. With no turn running it starts one. While
// a turn runs, steer asks the agent to add the input to that turn if it
// advertises steering; otherwise the input waits for the turn to end.
func (s *Session) Prompt(ctx context.Context, blocks []acp.ContentBlock, steer bool) (Receipt, error) {
	if len(blocks) == 0 {
		return Receipt{}, errors.New("acphost: empty prompt")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Receipt{}, ErrClosed
	}
	if !s.turnActive {
		s.startTurnLocked(blocks)
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeSent}, nil
	}
	if !steer || !s.conn.Info().Steering {
		s.queue = append(s.queue, blocks)
		s.mu.Unlock()
		return Receipt{Outcome: OutcomeQueued}, nil
	}
	s.mu.Unlock()

	outcome, err := s.conn.steer(ctx, blocks)
	if err != nil {
		return Receipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch outcome {
	case OutcomeInjected:
		s.userMessageLocked(blocks)
		return Receipt{Outcome: OutcomeInjected}, nil
	case "startedNewTurn":
		// The agent ignored idleBehavior and ran the input as a turn of its
		// own; it has the input, so re-sending it would deliver it twice.
		s.userMessageLocked(blocks)
		return Receipt{Outcome: OutcomeInjected}, nil
	case "promptRequired":
		// The turn ended before the steer reached the agent.
	default:
		return Receipt{}, fmt.Errorf("acphost: %s: unknown outcome %q", methodSteering, outcome)
	}
	if s.closed {
		return Receipt{}, ErrClosed
	}
	if !s.turnActive {
		s.startTurnLocked(blocks)
		return Receipt{Outcome: OutcomeSent}, nil
	}
	s.queue = append(s.queue, blocks)
	return Receipt{Outcome: OutcomeQueued}, nil
}

func (s *Session) startTurnLocked(blocks []acp.ContentBlock) {
	s.turn++
	s.turnActive = true
	s.emitLocked(Item{Kind: KindTurnStart})
	s.userMessageLocked(blocks)
	s.callback(func() {
		if s.cfg.OnState != nil {
			s.cfg.OnState(true, "prompt")
		}
	})
	go s.runTurn(blocks)
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

func (s *Session) runTurn(blocks []acp.ContentBlock) {
	stop, err := s.conn.prompt(s.ctx, blocks)
	if err != nil && s.conn.closed() {
		// Record what the agent sent before it went away ahead of the
		// interruption.
		s.conn.delivered()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.proj.endTurn()
	reason := stop
	if err != nil {
		if s.conn.closed() {
			reason = "interrupted"
			s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: err.Error()}})
		} else {
			reason = "error"
			s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Prompt failed", Description: err.Error()}})
		}
	}
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: reason})
	s.turnActive = false
	if len(s.queue) > 0 && !s.conn.closed() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next)
		return
	}
	s.callback(func() {
		if s.cfg.OnState != nil {
			s.cfg.OnState(false, reason)
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

// SetOption sets a config option to a value id (string) or a boolean.
func (s *Session) SetOption(ctx context.Context, configID string, value any) error {
	opts, err := s.conn.setOption(ctx, configID, value)
	if err != nil {
		return err
	}
	if present(opts) {
		s.mu.Lock()
		s.emitLocked(Item{Kind: KindConfigOptions, ConfigOptions: opts})
		s.mu.Unlock()
	}
	return nil
}

// SetMode switches the agent's mode for agents that expose modes but no
// mode config option.
func (s *Session) SetMode(ctx context.Context, modeID string) error {
	if err := s.conn.setMode(ctx, modeID); err != nil {
		return err
	}
	s.mu.Lock()
	s.emitLocked(Item{Kind: KindModeChange, Mode: modeID})
	s.mu.Unlock()
	return nil
}

// ListSessions returns one page of the agent's sessions.
func (s *Session) ListSessions(ctx context.Context, cwd, cursor string) ([]SessionSummary, string, error) {
	return s.conn.listSessions(ctx, cwd, cursor)
}

// State returns a snapshot of the session.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.TurnInFlight = s.turnActive
	st.Queued = len(s.queue)
	st.Pending = s.conn.pendingRequests()
	st.LastActivity = s.conn.LastActivity()
	return st
}

// Subscribe returns the items after afterSeq and a channel of every item
// appended from then on, with no gap between the two. A subscriber that
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

// emitLocked appends an item to the log and hands it to subscribers.
func (s *Session) emitLocked(it Item) {
	if s.closed {
		return
	}
	switch it.Kind {
	case KindModeChange:
		s.state.Mode = it.Mode
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

func (s *Session) activityLocked(verb, target string) {
	if s.cfg.OnActivity == nil {
		return
	}
	now := time.Now()
	if wait := s.actAt.Add(time.Second).Sub(now); wait > 0 {
		s.actNext = &[2]string{verb, target}
		if s.actTimer == nil {
			s.actTimer = time.AfterFunc(wait, s.flushActivity)
		}
		return
	}
	s.actAt = now
	s.callback(func() { s.cfg.OnActivity(verb, target) })
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
// requests, cleared inputs and an idle state.
func (s *Session) watch() {
	<-s.conn.Done()
	s.conn.delivered()
	s.stop()
	s.conn.drain()
	s.mu.Lock()
	if s.turnActive {
		s.proj.endTurn()
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "error", Title: "Turn interrupted", Description: "The agent connection closed before the turn finished."}})
		s.emitLocked(Item{Kind: KindTurnEnd, StopReason: "interrupted"})
		s.turnActive = false
		s.callback(func() {
			if s.cfg.OnState != nil {
				s.cfg.OnState(false, "interrupted")
			}
		})
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
