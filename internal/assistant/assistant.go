// Package assistant hosts each member's Aether assistant: an Enhanced session
// of the member's own agent, run in their environment terminal and wired to a
// member-scoped tool socket.
package assistant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	workDir      = ".aether/assistant"
	socketName   = "assistant.sock"
	startTimeout = 2 * time.Minute
	stopGrace    = 3 * time.Second
	stderrTail   = 4 << 10
	maxKeys      = 128
)

// claudeSettings lets Claude Code call the Aether tools without asking: the
// server holds the confirmed ones for the member's approval, and the shell
// still asks.
const claudeSettings = `{"permissions":{"allow":["mcp__aether"]}}
`

var (
	// ErrNotRunning means the member's assistant has no live session.
	ErrNotRunning = errors.New("assistant: no live session; send a prompt or start it")
	// ErrTurnRunning means a turn is running; the member stops it first.
	ErrTurnRunning = errors.New("assistant: a turn is running; stop it first")
	// ErrClosed means the service is shutting down.
	ErrClosed = errors.New("assistant: the server is shutting down")
)

type Config struct {
	// Dir is the host directory holding <member>/items.jsonl and
	// <member>/sock.
	Dir    string
	Store  store.AssistantStore
	Homes  *memberhome.Manager
	Runs   *scheduler.Scheduler
	Caller func(domain.MemberID) tools.Caller
}

// Service manages one session per member.
type Service struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	members map[domain.MemberID]*member
}

func New(cfg Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{cfg: cfg, ctx: ctx, cancel: cancel, members: make(map[domain.MemberID]*member)}
}

type member struct {
	id       domain.MemberID
	confirms *confirmations
	// op serializes starting, stopping and resetting the session.
	op sync.Mutex

	mu       sync.Mutex
	sess     *session
	starting bool
	err      string
	// changed is closed when the live session starts or ends, or the
	// conversation is reset, so a viewer resubscribes.
	changed chan struct{}
	// attempts holds the prompts by idempotency key: those in flight and
	// the last maxKeys that succeeded.
	attempts map[string]*attempt
	done     []string

	wmu      sync.Mutex
	watchers map[chan struct{}]struct{}
}

type session struct {
	agent  string
	acp    *acphost.Session
	exec   runtime.ManagedExec
	cancel context.CancelFunc
	ended  chan struct{}
}

func (s *Service) member(id domain.MemberID) *member {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[id]
	if m == nil {
		m = &member{id: id, changed: make(chan struct{}), watchers: make(map[chan struct{}]struct{}), attempts: make(map[string]*attempt)}
		m.confirms = newConfirmations(m.notify)
		s.members[id] = m
	}
	return m
}

func (m *member) live() *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveLocked()
}

// liveLocked is the session unless its adapter already exited, which can
// precede its registration.
func (m *member) liveLocked() *session {
	if m.sess != nil {
		select {
		case <-m.sess.acp.Done():
			return nil
		default:
		}
	}
	return m.sess
}

func (m *member) signalLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
	m.notify()
}

// notify tells watchers that the member's status may have changed.
func (m *member) notify() {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	for ch := range m.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// updates receives a value after each status change; unread ones coalesce.
func (m *member) updates() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.watchers[ch] = struct{}{}
	return ch, func() {
		m.wmu.Lock()
		defer m.wmu.Unlock()
		delete(m.watchers, ch)
	}
}

func (s *Service) logPath(id domain.MemberID) string {
	return filepath.Join(s.cfg.Dir, string(id), "items.jsonl")
}

// Close stops every session and waits for the tool sockets to close.
func (s *Service) Close() error {
	s.cancel()
	s.mu.Lock()
	members := make([]*member, 0, len(s.members))
	for _, m := range s.members {
		members = append(members, m)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	for _, m := range members {
		m.op.Lock()
		errs = append(errs, m.stop(ctx))
		m.op.Unlock()
	}
	s.wg.Wait()
	return errors.Join(errs...)
}

// agents reports the assistant's agents as the member's agent.list sees them.
func (s *Service) agents(ctx context.Context, id domain.MemberID) ([]protocol.AssistantAgent, error) {
	raw, perr := s.cfg.Caller(id).Call(ctx, protocol.MethodAgentList, json.RawMessage("{}"))
	if perr != nil {
		return nil, fmt.Errorf("assistant: list agents: %w", perr)
	}
	var listed protocol.AgentListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("assistant: decode the agent list: %w", err)
	}
	return readiness(listed.Agents), nil
}

func (s *Service) stored(ctx context.Context, id domain.MemberID) (*store.AssistantSession, error) {
	row, err := s.cfg.Store.GetAssistantSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return &store.AssistantSession{MemberID: id}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("assistant: load the session: %w", err)
	}
	return row, nil
}

// Status reports the member's assistant.
func (s *Service) Status(ctx context.Context, id domain.MemberID) (protocol.AssistantStatus, error) {
	agents, err := s.agents(ctx, id)
	if err != nil {
		return protocol.AssistantStatus{}, err
	}
	row, err := s.stored(ctx, id)
	if err != nil {
		return protocol.AssistantStatus{}, err
	}
	m := s.member(id)
	m.mu.Lock()
	live, starting, failure := m.liveLocked(), m.starting, m.err
	m.mu.Unlock()
	status := protocol.AssistantStatus{Agents: agents, Error: failure, Confirmations: m.confirms.snapshot()}
	switch {
	case live != nil:
		status.Agent = live.agent
		status.State = protocol.AssistantStateIdle
		if live.acp.State().TurnInFlight {
			status.State = protocol.AssistantStateRunning
		}
		return status, nil
	case starting:
		status.State = protocol.AssistantStateStarting
		return status, nil
	}
	var notReady *AgentNotReadyError
	if status.Agent, err = resolve(agents, "", row.Agent); errors.As(err, &notReady) {
		status.Agent = row.Agent
		if slices.ContainsFunc(agents, func(a protocol.AssistantAgent) bool { return a.Ready }) {
			status.Error = err.Error()
		} else {
			status.Agent = ""
		}
	} else if err != nil {
		return protocol.AssistantStatus{}, err
	}
	stale := false
	if status.Agent != "" && status.Error == "" {
		if stale, err = s.cfg.Runs.AssistantTerminalStale(ctx, id); err != nil {
			return protocol.AssistantStatus{}, fmt.Errorf("assistant: %w", err)
		}
	}
	switch {
	case status.Agent == "":
		status.State = protocol.AssistantStateSetup
	case stale:
		status.State = protocol.AssistantStateNeedsRestart
	case failure != "" || status.Error != "":
		status.State = protocol.AssistantStateFailed
	default:
		status.State = protocol.AssistantStateIdle
	}
	return status, nil
}

// Start starts the member's session if none is live and returns the status,
// which says when no agent is ready or the environment needs a restart.
func (s *Service) Start(ctx context.Context, id domain.MemberID, p protocol.AssistantStartParams) (protocol.AssistantStatus, error) {
	m := s.member(id)
	if p.RestartEnvironment {
		m.op.Lock()
		err := m.stop(ctx)
		m.op.Unlock()
		if err != nil {
			return protocol.AssistantStatus{}, err
		}
		if err = s.cfg.Runs.StopTerminal(ctx, id); err != nil {
			return protocol.AssistantStatus{}, fmt.Errorf("assistant: restart the environment: %w", err)
		}
	}
	_, err := s.ensure(ctx, id, p.Agent)
	var notReady *AgentNotReadyError
	if err != nil && !errors.Is(err, scheduler.ErrAssistantNeedsRestart) && !errors.Is(err, ErrNoAgent) && !errors.As(err, &notReady) {
		return protocol.AssistantStatus{}, err
	}
	return s.Status(ctx, id)
}

// ensure returns the member's live session, starting one first when needed.
// A requested agent other than the stored one begins a new conversation.
func (s *Service) ensure(ctx context.Context, id domain.MemberID, requested string) (*session, error) {
	if s.ctx.Err() != nil {
		return nil, ErrClosed
	}
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if s.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if live := m.live(); live != nil && (requested == "" || requested == live.agent) {
		return live, nil
	}
	agents, err := s.agents(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err := s.stored(ctx, id)
	if err != nil {
		return nil, err
	}
	agent, err := resolve(agents, requested, row.Agent)
	if err != nil {
		return nil, err
	}
	if agent == "" {
		return nil, ErrNoAgent
	}
	if requested != "" && row.Agent != "" && row.Agent != agent {
		if err = s.resetLocked(ctx, m); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	m.starting, m.err = true, ""
	m.mu.Unlock()
	m.notify()
	openCtx, cancel := context.WithTimeout(s.ctx, startTimeout)
	defer cancel()
	live, err := s.open(openCtx, m, agent, row)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starting = false
	if err != nil {
		if !errors.Is(err, scheduler.ErrAssistantNeedsRestart) {
			m.err = err.Error()
		}
		m.notify()
		return nil, err
	}
	m.sess = live
	m.signalLocked()
	return live, nil
}

// open starts the agent's adapter in the member's terminal and opens the
// session over it, resuming row's when it belongs to agent.
func (s *Service) open(ctx context.Context, m *member, agent string, row *store.AssistantSession) (*session, error) {
	argv, askMode, err := s.cfg.Runs.AssistantAdapter(ctx, m.id, agent)
	if err != nil {
		return nil, fmt.Errorf("assistant: %w", err)
	}
	files := map[string]string{"AGENTS.md": Instructions, "CLAUDE.md": Instructions, ".claude/settings.json": claudeSettings}
	for name, content := range files {
		if err = s.cfg.Homes.HomeUpload(m.id, workDir+"/"+name, []byte(content), true); err != nil {
			return nil, fmt.Errorf("assistant: write %s: %w", name, err)
		}
	}
	sessCtx, cancel := context.WithCancel(s.ctx)
	ln, err := s.listen(m.id)
	if err != nil {
		cancel()
		return nil, err
	}
	srv := &tools.Server{Caller: s.cfg.Caller(m.id), Confirm: m.confirms.confirm, Member: string(m.id)}
	s.wg.Go(func() {
		if serveErr := srv.Serve(sessCtx, ln); serveErr != nil {
			slog.Warn("assistant: tool socket stopped", "member", m.id, "error", serveErr)
		}
	})
	live, err := s.openSession(ctx, m, agent, askMode, argv, row)
	if err != nil {
		cancel()
		return nil, err
	}
	live.cancel = cancel
	s.wg.Go(func() { m.watch(live) })
	return live, nil
}

func (s *Service) openSession(ctx context.Context, m *member, agent, askMode string, argv []string, row *store.AssistantSession) (*session, error) {
	exec, cwd, err := s.cfg.Runs.StartAssistantExec(ctx, m.id, runtime.ExecSpec{
		Argv: argv, WorkingDir: workDir, CreationKey: "assistant-" + rand.Text(),
		Env: []string{"AETHER_ASSISTANT=1", "NO_BROWSER=1"},
	})
	if err != nil {
		return nil, fmt.Errorf("assistant: start %s: %w", argv[0], err)
	}
	att := exec.Attachment()
	stderr := &tail{max: stderrTail}
	go func() { _, _ = io.Copy(stderr, att.Stderr()) }()
	resume := ""
	if row.Agent == agent {
		resume = row.HarnessSessionID
	}
	acpSess, err := acphost.Start(ctx, att.Stdout(), att.Stdin(), acphost.Config{
		LogPath: s.logPath(m.id),
		Cwd:     cwd,
		MCPServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "aether", Command: coordtransport.BinaryPath, Args: []string{"mcp", "--assistant"}, Env: []acp.EnvVariable{},
		}}},
		SessionID: resume,
		Logger:    slog.Default().With("assistant", m.id),
		OnState:   func(bool, string, error) { m.notify() },
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("assistant: open the %s session%s: %w", agent, stderr.suffix(), err), stopExec(exec))
	}
	if acpSess.State().Mode != askMode {
		if err = acpSess.SetMode(ctx, askMode); err != nil {
			_ = acpSess.Close()
			return nil, errors.Join(fmt.Errorf("assistant: set the %q mode, without which the assistant does not run: %w", askMode, err), stopExec(exec))
		}
	}
	if err = s.cfg.Store.PutAssistantSession(ctx, &store.AssistantSession{MemberID: m.id, Agent: agent, HarnessSessionID: acpSess.SessionID()}); err != nil {
		_ = acpSess.Close()
		return nil, errors.Join(fmt.Errorf("assistant: record the session: %w", err), stopExec(exec))
	}
	return &session{agent: agent, acp: acpSess, exec: exec, ended: make(chan struct{})}, nil
}

func stopExec(exec runtime.ManagedExec) error {
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace+10*time.Second)
	defer cancel()
	defer func() { _ = exec.Detach() }()
	if _, err := exec.Stop(ctx, stopGrace); err != nil {
		if state, statusErr := exec.Status(ctx); statusErr == nil && !state.Running {
			return nil
		}
		return fmt.Errorf("assistant: stop the adapter: %w", err)
	}
	return nil
}

// listen opens the member's tool socket, replacing a stale one.
func (s *Service) listen(id domain.MemberID) (*net.UnixListener, error) {
	dir := scheduler.AssistantSocketDir(s.cfg.Dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("assistant: create the socket directory: %w", err)
	}
	path := filepath.Join(dir, socketName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("assistant: remove the stale socket: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("assistant: listen on the tool socket: %w", err)
	}
	// A closed listener must not unlink the path: the next session's socket
	// may already be there.
	ln.SetUnlinkOnClose(false)
	if err = os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("assistant: open the tool socket to the member: %w", err)
	}
	return ln, nil
}

// watch cleans up after the adapter exits, whether stopped or not.
func (m *member) watch(live *session) {
	<-live.acp.Done()
	live.cancel()
	m.confirms.cancelAll()
	_ = live.exec.Detach()
	m.mu.Lock()
	if m.sess == live {
		m.sess = nil
		m.signalLocked()
	}
	m.mu.Unlock()
	close(live.ended)
}

// stop ends the live session, if any. The caller holds m.op.
func (m *member) stop(ctx context.Context) error {
	live := m.live()
	if live == nil {
		return nil
	}
	_ = live.acp.Close()
	if err := stopExec(live.exec); err != nil {
		return err
	}
	select {
	case <-live.ended:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("assistant: wait for the session to end: %w", ctx.Err())
	}
}

// AgentNotReadyError means the agent the conversation belongs to cannot run.
// The assistant never switches to another agent unasked, because that ends
// the conversation.
type AgentNotReadyError struct{ Agent protocol.AssistantAgent }

func (e *AgentNotReadyError) Error() string {
	name := e.Agent.DisplayName
	if name == "" {
		name = e.Agent.Name
	}
	return fmt.Sprintf("%s is not ready: %s; sign in again or start with another agent", name, e.Agent.Reason)
}

// resolve is choose, except that an unrequested stored agent that is not
// ready is an error instead of a switch to another agent.
func resolve(agents []protocol.AssistantAgent, requested, stored string) (string, error) {
	if requested == "" && stored != "" {
		if i := slices.IndexFunc(agents, func(a protocol.AssistantAgent) bool { return a.Name == stored }); i >= 0 && !agents[i].Ready {
			return stored, &AgentNotReadyError{Agent: agents[i]}
		}
	}
	return choose(agents, requested, stored)
}

// resetLocked discards the conversation. The caller holds m.op.
func (s *Service) resetLocked(ctx context.Context, m *member) error {
	if err := m.stop(ctx); err != nil {
		return err
	}
	if err := s.cfg.Store.DeleteAssistantSession(ctx, m.id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if err := os.Remove(s.logPath(m.id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("assistant: remove the item log: %w", err)
	}
	m.mu.Lock()
	m.err = ""
	m.signalLocked()
	m.mu.Unlock()
	return nil
}

// Remove stops the member's session and deletes everything the assistant
// keeps for them. Call it when the member is removed.
func (s *Service) Remove(ctx context.Context, id domain.MemberID) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if err := m.stop(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.members, id)
	s.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.cfg.Dir, string(id))); err != nil {
		return fmt.Errorf("assistant: remove the member's files: %w", err)
	}
	return nil
}

// Reset stops the session and discards the conversation.
func (s *Service) Reset(ctx context.Context, id domain.MemberID) (protocol.AssistantStatus, error) {
	m := s.member(id)
	m.op.Lock()
	err := s.resetLocked(ctx, m)
	m.op.Unlock()
	if err != nil {
		return protocol.AssistantStatus{}, err
	}
	return s.Status(ctx, id)
}

// attempt is one prompt under an idempotency key; err is final once done is
// closed.
type attempt struct {
	done chan struct{}
	err  error
}

// Prompt sends text to the assistant, starting it first when needed, unless a
// turn is running. A key that is in flight or was accepted is not sent again:
// a duplicate waits for the first attempt and returns its result.
func (s *Service) Prompt(ctx context.Context, id domain.MemberID, text, key string) error {
	m := s.member(id)
	a, first := m.begin(key)
	if !first {
		select {
		case <-a.done:
			return a.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := s.send(ctx, id, text)
	m.finish(key, a, err)
	return err
}

func (s *Service) send(ctx context.Context, id domain.MemberID, text string) error {
	live, err := s.ensure(ctx, id, "")
	if err != nil {
		return err
	}
	started, err := live.acp.PromptIdle(ctx, []acp.ContentBlock{acp.TextBlock(text)})
	if err == nil && !started {
		err = ErrTurnRunning
	}
	return err
}

func (m *member) begin(key string) (*attempt, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.attempts[key]; a != nil {
		return a, false
	}
	a := &attempt{done: make(chan struct{})}
	m.attempts[key] = a
	return a, true
}

// finish records a's result. A failed key is forgotten, so the caller can
// retry it.
func (m *member) finish(key string, a *attempt, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.err = err
	close(a.done)
	if err != nil {
		delete(m.attempts, key)
		return
	}
	if m.done = append(m.done, key); len(m.done) > maxKeys {
		delete(m.attempts, m.done[0])
		m.done = m.done[1:]
	}
}

func (s *Service) liveSession(id domain.MemberID) (*acphost.Session, error) {
	live := s.member(id).live()
	if live == nil {
		return nil, ErrNotRunning
	}
	return live.acp, nil
}

// Cancel stops the turn in flight.
func (s *Service) Cancel(ctx context.Context, id domain.MemberID) error {
	sess, err := s.liveSession(id)
	if err != nil {
		return err
	}
	return sess.Cancel(ctx)
}

// Answer answers one of the harness's pending requests.
func (s *Service) Answer(id domain.MemberID, requestID, optionID string, values map[string]any) error {
	sess, err := s.liveSession(id)
	if err != nil {
		return err
	}
	return sess.Answer(requestID, optionID, values)
}

// Confirm answers one of the member's pending confirmations.
func (s *Service) Confirm(id domain.MemberID, confirmationID string, approve bool) error {
	return s.member(id).confirms.decide(confirmationID, approve)
}

type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

// suffix is the adapter's stderr, for an error message.
func (t *tail) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := strings.TrimSpace(string(t.b)); s != "" {
		return "; stderr: " + s
	}
	return ""
}
