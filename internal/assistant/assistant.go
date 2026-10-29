// Package assistant hosts each member's Aether assistant: an Enhanced session
// of the member's own agent, run in a container of its own with a
// server-owned home and wired to a member-scoped tool socket.
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
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// EpochStride separates conversation generations in the epoch viewers see.
	EpochStride  = 1 << 20
	socketName   = "assistant.sock"
	startTimeout = protocol.AssistantStartTimeout
	stopGrace    = 3 * time.Second
	stderrTail   = 4 << 10
	maxKeys      = 128
)

var (
	// ErrNotRunning means the member's assistant has no live session.
	ErrNotRunning = errors.New("assistant: no live session; send a prompt or start it")
	// ErrTurnRunning means a turn is running; the member stops it first.
	ErrTurnRunning = errors.New("assistant: a turn is running; stop it first")
	// ErrClosed means the service is shutting down.
	ErrClosed = errors.New("assistant: the server is shutting down")
	// ErrRemoving means the member is being removed.
	ErrRemoving = errors.New("assistant: the member is being removed")
)

type Config struct {
	// Dir is the host directory holding <member>/items.jsonl and the
	// assistant container's <member>/home, <member>/instructions and
	// <member>/sock.
	Dir   string
	Store store.AssistantStore
	Runs  *scheduler.Scheduler
	// Host runs the assistant's container and adapter.
	Host   Host
	Caller func(domain.MemberID) tools.Caller
	Bus    events.Bus
	// Preferences are the members' private preferences the assistant reads
	// and saves.
	Preferences *preference.Service
}

// Host is the scheduler's side of the assistant: its container and the
// adapter in it.
type Host interface {
	AssistantAdapter(agent string) (scheduler.AssistantLaunch, error)
	StartAssistantExec(ctx context.Context, member domain.MemberID, agent string, spec runtime.ExecSpec, installing func()) (runtime.ManagedExec, error)
	StopAssistantExec(ctx context.Context, identity runtime.ExecIdentity) error
	DiscardAssistantState(ctx context.Context, member domain.MemberID) error
	RemoveAssistant(ctx context.Context, member domain.MemberID) error
	AssistantLoginFound(member domain.MemberID, agent string) (bool, error)
}

type Service struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	members map[domain.MemberID]*member

	watches Watches
}

func New(cfg Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{cfg: cfg, ctx: ctx, cancel: cancel, members: make(map[domain.MemberID]*member)}
}

type member struct {
	id       domain.MemberID
	confirms *confirmations
	actions  heldActions
	// conv is cancelled when a reset discards the conversation, so an
	// action record that outlived it is dropped. Read it under mu.
	conv    context.Context
	endConv context.CancelFunc
	// op serializes starting, stopping and resetting the session.
	op sync.Mutex
	// reset is held while a reset runs, so a viewer that sees the session
	// end reads the new generation.
	reset sync.RWMutex
	// removing refuses new sessions and view contexts while the member is
	// being removed. It is written under both op and mu.
	removing bool
	// settings orders changes to the member's settings with the calls they
	// decide.
	settings sync.Mutex
	// share orders turning view sharing off with the views it drops: a
	// prompt holds it read-locked through its send.
	share sync.RWMutex

	mu       sync.Mutex
	sess     *session
	starting bool
	// installing is set while a start installs the agent.
	installing bool
	err        string
	// changed is closed when the live session starts or ends, or the
	// conversation is reset, so a viewer resubscribes.
	changed chan struct{}
	// attempts holds the prompts by idempotency key: those in flight and
	// the last maxKeys that succeeded.
	attempts map[string]*attempt
	done     []string
	// view is the held view context, viewAt when it arrived, and viewSource
	// whether a prompt or a navigation update carried it.
	view       *protocol.AssistantViewContext
	viewAt     time.Time
	viewSource string

	wmu      sync.Mutex
	watchers map[chan struct{}]struct{}
}

type session struct {
	agent      string
	harnessID  string
	generation int64
	identity   string
	acp        *acphost.Session
	exec       runtime.ManagedExec
	cancel     context.CancelFunc
	ended      chan struct{}

	stopOnce sync.Once
	stopErr  error
}

func (s *Service) member(id domain.MemberID) *member {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[id]
	if m == nil {
		m = &member{id: id, changed: make(chan struct{}), watchers: make(map[chan struct{}]struct{}), attempts: make(map[string]*attempt)}
		m.confirms = newConfirmations(m.notify)
		m.conv, m.endConv = context.WithCancel(context.Background())
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
		errs = append(errs, s.stop(ctx, m))
		m.op.Unlock()
	}
	s.wg.Wait()
	return errors.Join(errs...)
}

func (s *Service) agents(ctx context.Context, id domain.MemberID) ([]protocol.AssistantAgent, error) {
	raw, perr := s.cfg.Caller(id).Call(ctx, protocol.MethodAgentList, json.RawMessage("{}"))
	if perr != nil {
		return nil, fmt.Errorf("assistant: list agents: %w", perr)
	}
	var listed protocol.AgentListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("assistant: decode the agent list: %w", err)
	}
	return readiness(listed.Agents, func(agent string) (bool, error) { return s.cfg.Host.AssistantLoginFound(id, agent) })
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
	live, starting, installing, failure := m.liveLocked(), m.starting, m.installing, m.err
	m.mu.Unlock()
	settings, err := s.Settings(ctx, id)
	if err != nil {
		return protocol.AssistantStatus{}, err
	}
	status := protocol.AssistantStatus{
		Agents: agents, Error: failure, Confirmations: m.confirms.snapshot(),
		AutoApprove: settings.AutoApprove, SuggestAutoApprove: settings.SuggestAutoApprove, ShareView: settings.ShareView,
		PreferencesEnabled: settings.PreferencesEnabled, Temporary: settings.Temporary,
	}
	switch {
	case live != nil:
		status.Agent = live.agent
		status.State = protocol.AssistantStateIdle
		if live.acp.State().TurnInFlight {
			status.State = protocol.AssistantStateRunning
		}
		return status, nil
	case installing:
		status.State = protocol.AssistantStateInstalling
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
	switch {
	case status.Agent == "":
		status.State = protocol.AssistantStateSetup
	case failure != "" || status.Error != "":
		status.State = protocol.AssistantStateFailed
	default:
		status.State = protocol.AssistantStateIdle
	}
	return status, nil
}

func (s *Service) Start(ctx context.Context, id domain.MemberID, p protocol.AssistantStartParams) (protocol.AssistantStatus, error) {
	_, err := s.ensure(ctx, id, p.Agent)
	var notReady *AgentNotReadyError
	if err != nil && !errors.Is(err, ErrNoAgent) && !errors.As(err, &notReady) {
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
	return s.ensureLocked(ctx, m, requested)
}

// ensureLocked is ensure with m.op held.
func (s *Service) ensureLocked(ctx context.Context, m *member, requested string) (*session, error) {
	id := m.id
	if s.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if m.removing {
		return nil, ErrRemoving
	}
	m.mu.Lock()
	cur := m.sess
	m.mu.Unlock()
	if cur != nil {
		select {
		case <-cur.acp.Done():
			select {
			case <-cur.ended:
			case <-ctx.Done():
				return nil, fmt.Errorf("assistant: wait for the earlier session to end: %w", ctx.Err())
			}
		default:
			if requested == "" || requested == cur.agent {
				return cur, nil
			}
		}
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
		if row, err = s.stored(ctx, id); err != nil {
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
	m.starting, m.installing = false, false
	if err != nil {
		m.err = err.Error()
		m.notify()
		return nil, err
	}
	m.sess = live
	m.signalLocked()
	return live, nil
}

func (s *Service) open(ctx context.Context, m *member, agent string, row *store.AssistantSession) (*session, error) {
	launch, err := s.cfg.Host.AssistantAdapter(agent)
	if err != nil {
		return nil, fmt.Errorf("assistant: %w", err)
	}
	dir := scheduler.AssistantInstructionsDir(s.cfg.Dir, m.id)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("assistant: create the instructions directory: %w", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(Instructions), 0o644); err != nil {
			return nil, fmt.Errorf("assistant: write %s: %w", name, err)
		}
	}
	sessCtx, cancel := context.WithCancel(s.ctx)
	ln, err := s.listen(m.id)
	if err != nil {
		cancel()
		return nil, err
	}
	srv := &tools.Server{Caller: s.preferenceCaller(m, actionCaller{Caller: s.cfg.Caller(m.id), s: s, m: m}), Confirm: s.approver(m), Member: string(m.id),
		View: func() protocol.AssistantViewContextResult { return s.View(m.id, time.Now()) }}
	if s.watches != nil {
		srv.Follow = func(ctx context.Context, run string) {
			if _, followErr := s.watches.Follow(ctx, m.id, domain.RunID(run)); followErr != nil {
				body := "Watching it failed: " + followErr.Error() + "\n\nYou will not be notified when it needs you or finishes."
				if noteErr := s.note(ctx, m, acphost.Notice{Severity: "warning", Title: "Run " + run + " is not watched", Description: body}); noteErr != nil {
					slog.Warn("assistant: watch the run", "run", run, "member", m.id, "error", errors.Join(followErr, noteErr))
				}
			}
		}
	}
	s.wg.Go(func() {
		if serveErr := srv.Serve(sessCtx, ln); serveErr != nil {
			slog.Warn("assistant: tool socket stopped", "member", m.id, "error", serveErr)
		}
	})
	live, err := s.openSession(ctx, m, agent, launch, row)
	if err != nil {
		cancel()
		return nil, err
	}
	live.cancel = cancel
	s.wg.Go(func() { s.watch(m, live) })
	return live, nil
}

func (s *Service) openSession(ctx context.Context, m *member, agent string, launch scheduler.AssistantLaunch, row *store.AssistantSession) (*session, error) {
	argv, mode := launch.Argv, launch.Mode
	if err := s.stopStale(ctx, m, row); err != nil {
		return nil, err
	}
	resume := ""
	if row.Agent == agent {
		resume = row.HarnessSessionID
	}
	exec, err := s.cfg.Host.StartAssistantExec(ctx, m.id, agent, runtime.ExecSpec{
		Argv: argv, CreationKey: "assistant-" + rand.Text(),
		Env: append([]string{"AETHER_ASSISTANT=1", "NO_BROWSER=1"}, launch.Env...),
	}, func() {
		m.mu.Lock()
		m.installing = true
		m.mu.Unlock()
		m.notify()
	})
	if err != nil {
		return nil, fmt.Errorf("assistant: start %s: %w", argv[0], err)
	}
	identity, err := json.Marshal(exec.Identity())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("assistant: encode the adapter's identity: %w", err), stopExec(exec))
	}
	record := &store.AssistantSession{MemberID: m.id, Agent: agent, HarnessSessionID: resume, ExecIdentity: string(identity)}
	if err = s.cfg.Store.PutAssistantSession(ctx, record); err != nil {
		return nil, errors.Join(fmt.Errorf("assistant: record the adapter: %w", err), stopExec(exec))
	}
	att := exec.Attachment()
	stderr := &tail{max: stderrTail}
	go func() { _, _ = io.Copy(stderr, att.Stderr()) }()
	acpSess, err := acphost.Start(ctx, att.Stdout(), att.Stdin(), acphost.Config{
		LogPath: s.logPath(m.id),
		Cwd:     scheduler.AssistantCwd,
		MCPServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "aether", Command: coordtransport.BinaryPath, Args: []string{"mcp", "--assistant"}, Env: []acp.EnvVariable{},
		}}},
		SessionID: resume,
		Logger:    slog.Default().With("assistant", m.id),
		OnState: func(st acphost.TurnState) {
			if !st.Working && st.Tasks == 0 {
				m.confirms.cancelAll()
			}
			m.notify()
		},
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("assistant: open the %s session%s: %w", agent, stderr.suffix(), err), stopExec(exec))
	}
	if acpSess.State().Mode != mode {
		if err = acpSess.SetMode(ctx, mode); err != nil {
			_ = acpSess.Close()
			return nil, errors.Join(fmt.Errorf("assistant: set the %q mode: %w", mode, err), stopExec(exec))
		}
	}
	record.HarnessSessionID = acpSess.SessionID()
	if err = s.cfg.Store.PutAssistantSession(ctx, record); err != nil {
		_ = acpSess.Close()
		return nil, errors.Join(fmt.Errorf("assistant: record the session: %w", err), stopExec(exec))
	}
	return &session{agent: agent, harnessID: record.HarnessSessionID, generation: row.Generation, identity: record.ExecIdentity, acp: acpSess, exec: exec, ended: make(chan struct{})}, nil
}

// stopStale stops the adapter row records, which an earlier session or server
// process started and may not have stopped.
func (s *Service) stopStale(ctx context.Context, m *member, row *store.AssistantSession) error {
	if row.ExecIdentity == "" {
		return nil
	}
	var identity runtime.ExecIdentity
	if err := json.Unmarshal([]byte(row.ExecIdentity), &identity); err != nil {
		return fmt.Errorf("assistant: decode the recorded adapter: %w", err)
	}
	if err := s.cfg.Host.StopAssistantExec(ctx, identity); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	return s.cfg.Store.ClearAssistantExec(ctx, m.id, row.Generation, row.ExecIdentity)
}

// endExec stops the session's adapter once and forgets its recorded
// identity. A failure keeps the identity, so the next start stops it.
func (s *Service) endExec(live *session, member domain.MemberID) error {
	live.stopOnce.Do(func() {
		if live.stopErr = stopExec(live.exec); live.stopErr != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), stopGrace+10*time.Second)
		defer cancel()
		if err := s.cfg.Store.ClearAssistantExec(ctx, member, live.generation, live.identity); err != nil {
			live.stopErr = fmt.Errorf("assistant: forget the stopped adapter: %w", err)
		}
	})
	return live.stopErr
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

// watch ends the session's adapter and its bookkeeping once the ACP
// connection is gone: a dropped attachment does not mean the adapter exited.
func (s *Service) watch(m *member, live *session) {
	<-live.acp.Done()
	live.cancel()
	m.confirms.cancelAll()
	if err := s.endExec(live, m.id); err != nil {
		slog.Warn("assistant: the adapter may still run; the next start stops it", "member", m.id, "error", err)
	}
	m.mu.Lock()
	if m.sess == live {
		m.sess = nil
		m.signalLocked()
	}
	m.mu.Unlock()
	close(live.ended)
}

func (s *Service) stop(ctx context.Context, m *member) error {
	m.mu.Lock()
	live := m.sess
	m.mu.Unlock()
	if live == nil {
		return nil
	}
	_ = live.acp.Close()
	if err := s.endExec(live, m.id); err != nil {
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

// resetLocked discards the conversation and starts the next generation. The
// caller holds m.op.
func (s *Service) resetLocked(ctx context.Context, m *member) error {
	m.reset.Lock()
	defer m.reset.Unlock()
	if err := s.stop(ctx, m); err != nil {
		return err
	}
	row, err := s.stored(ctx, m.id)
	if err != nil {
		return err
	}
	if err = s.stopStale(ctx, m, row); err != nil {
		return err
	}
	if err = s.cfg.Host.DiscardAssistantState(ctx, m.id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if _, err := s.cfg.Store.ResetAssistantSession(ctx, m.id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if err := os.Remove(s.logPath(m.id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("assistant: remove the item log: %w", err)
	}
	m.mu.Lock()
	m.endConv()
	m.conv, m.endConv = context.WithCancel(context.Background())
	m.err = ""
	m.attempts, m.done = make(map[string]*attempt), nil
	m.signalLocked()
	m.mu.Unlock()
	return nil
}

// Fence stops the member's session and refuses new ones until Unfence or
// Remove, so a member being removed cannot recreate it.
func (s *Service) Fence(ctx context.Context, id domain.MemberID) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	m.fence(true)
	if err := s.stop(ctx, m); err != nil {
		m.fence(false)
		return err
	}
	return nil
}

func (m *member) fence(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removing = on
	m.forgetViewLocked()
}

// Unfence lets a member whose removal failed use the assistant again.
func (s *Service) Unfence(id domain.MemberID) {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	m.fence(false)
}

func (s *Service) Remove(ctx context.Context, id domain.MemberID) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if err := s.stop(ctx, m); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.members, id)
	s.mu.Unlock()
	if err := s.cfg.Host.RemoveAssistant(ctx, id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	return nil
}

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

type attempt struct {
	done chan struct{}
	err  error
}

// Prompt sends text to the assistant, starting it first when needed, unless a
// turn is running. A key that is in flight or was accepted, even before a
// restart, is not sent again: a duplicate waits for the first attempt and
// returns its result.
func (s *Service) Prompt(ctx context.Context, id domain.MemberID, text, key string, view *protocol.AssistantViewContext) error {
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
	m.share.RLock()
	dispatched, err := s.send(ctx, m, text, key, view)
	m.share.RUnlock()
	if err != nil && !dispatched {
		m.finish(key, a, err)
		return err
	}
	m.finish(key, a, nil)
	return err
}

// send dispatches text under m.op, so a reset cannot fall between the key
// check, the dispatch and saving the key under the generation it ran in.
// dispatched is true once the agent may have the prompt; its key then stays
// reserved even if the caller left or saving it failed.
func (s *Service) send(ctx context.Context, m *member, text, key string, view *protocol.AssistantViewContext) (dispatched bool, err error) {
	m.op.Lock()
	defer m.op.Unlock()
	row, err := s.stored(ctx, m.id)
	if err != nil {
		return false, err
	}
	seen, err := s.cfg.Store.HasAssistantPromptKey(ctx, m.id, row.Generation, key)
	if err != nil {
		return false, fmt.Errorf("assistant: %w", err)
	}
	if seen {
		return true, nil
	}
	if view, err = s.sharedView(ctx, m, view); err != nil {
		return false, err
	}
	live, err := s.ensureLocked(ctx, m, "")
	if err != nil {
		return false, err
	}
	notes, restore := s.preferenceNotes(ctx, m.id)
	defer func() {
		if !dispatched {
			restore()
		}
	}()
	started, err := promptLive(ctx, live, text, view, notes)
	if errors.Is(err, acphost.ErrPromptUnsent) {
		// The adapter died after ensure saw it alive and before the prompt
		// left: once its session has ended, start a new one and send there.
		select {
		case <-live.ended:
		case <-ctx.Done():
			return false, fmt.Errorf("assistant: wait for the ended session: %w", ctx.Err())
		}
		if live, err = s.ensureLocked(ctx, m, ""); err != nil {
			return false, err
		}
		started, err = promptLive(ctx, live, text, view, notes)
	}
	switch {
	case err == nil && !started:
		return false, ErrTurnRunning
	case err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded):
		return false, err
	}
	// Only a prompt dispatched now holds its view: one refused, or a retry
	// of one sent earlier, must not replace a newer view. A caller that left
	// while waiting for the agent's acceptance may have started the turn, so
	// its view and key are kept all the same.
	m.holdView(view, viewFromPrompt, time.Now())
	if saveErr := s.cfg.Store.AddAssistantPromptKey(context.WithoutCancel(ctx), m.id, row.Generation, key); saveErr != nil {
		return true, errors.Join(err, fmt.Errorf("assistant: %w", saveErr))
	}
	return true, err
}

func promptLive(ctx context.Context, live *session, text string, view *protocol.AssistantViewContext, notes []acp.ContentBlock) (bool, error) {
	blocks, err := promptBlocks(text, view, live.acp.Info().PromptEmbedded)
	if err != nil {
		return false, err
	}
	return live.acp.PromptIdle(ctx, append(blocks, notes...), nil)
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
// retry it. A reset in between has already dropped a.
func (m *member) finish(key string, a *attempt, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.err = err
	close(a.done)
	if m.attempts[key] != a {
		return
	}
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

func (s *Service) Cancel(ctx context.Context, id domain.MemberID) error {
	sess, err := s.liveSession(id)
	if err != nil {
		return err
	}
	err = sess.Cancel(ctx)
	s.member(id).confirms.cancelAll()
	return err
}

func (s *Service) Answer(id domain.MemberID, requestID, optionID string, values map[string]any) error {
	sess, err := s.liveSession(id)
	if err != nil {
		return err
	}
	return sess.Answer(requestID, optionID, values)
}

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

func (t *tail) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := strings.TrimSpace(string(t.b)); s != "" {
		return "; stderr: " + s
	}
	return ""
}
