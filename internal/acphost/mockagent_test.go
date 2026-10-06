package acphost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"

	"github.com/3xDevOps/Aether/internal/domain"
)

type fixture = acpmock.Fixture

var discard = slog.New(slog.DiscardHandler)

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	f, err := acpmock.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type promptCall struct {
	ctx    context.Context
	params struct {
		SessionID string          `json:"sessionId"`
		Prompt    json.RawMessage `json:"prompt"`
	}
}

// mockAgent speaks the agent side of ACP over pipes. By default it replays
// its fixture; tests override single methods.
type mockAgent struct {
	t      *testing.T
	fix    fixture
	conn   *acp.Connection
	stdout io.WriteCloser

	// Overrides, set before the session starts.
	onPrompt func(m *mockAgent, call promptCall) (any, *acp.RequestError)
	onSteer  func(m *mockAgent, params json.RawMessage) (any, *acp.RequestError)
	resume   func() *acp.RequestError
	load     func() *acp.RequestError
	newErr   *acp.RequestError

	mu          sync.Mutex
	methods     []string
	inflight    int
	maxInflight int
	cancelled   chan struct{}
}

func newMockAgent(t *testing.T, fix fixture) *mockAgent {
	return &mockAgent{t: t, fix: fix, cancelled: make(chan struct{}, 8)}
}

func (m *mockAgent) pipes() (io.Reader, io.WriteCloser) {
	hostIn, agentOut := io.Pipe()
	agentIn, hostOut := io.Pipe()
	m.stdout = agentOut
	m.conn = acp.NewConnection(m.handle, agentOut, agentIn)
	m.conn.SetLogger(discard)
	m.t.Cleanup(func() { _ = agentOut.Close(); _ = hostOut.Close() })
	return hostIn, hostOut
}

func (m *mockAgent) called(method string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.methods, method)
}

func (m *mockAgent) update(u any) {
	b, err := json.Marshal(u)
	if err != nil {
		m.t.Error(err)
		return
	}
	if err := m.conn.SendNotification(context.Background(), acp.ClientMethodSessionUpdate, map[string]any{
		"sessionId": m.sessionID(), "update": json.RawMessage(b),
	}); err != nil {
		m.t.Error(err)
	}
}

func (m *mockAgent) sessionID() string {
	return m.fix.SessionID()
}

func (m *mockAgent) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return acp.SendRequest[json.RawMessage](m.conn, ctx, method, params)
}

func (m *mockAgent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	m.mu.Lock()
	m.methods = append(m.methods, method)
	m.mu.Unlock()
	switch method {
	case acp.AgentMethodInitialize:
		return m.fix.Initialize, nil
	case acp.AgentMethodSessionNew:
		if m.newErr != nil {
			return nil, m.newErr
		}
		return m.fix.SessionNew, nil
	case acp.AgentMethodSessionResume:
		if m.resume != nil {
			if err := m.resume(); err != nil {
				return nil, err
			}
		}
		return m.fix.Load.Result, nil
	case acp.AgentMethodSessionLoad:
		if m.load != nil {
			if err := m.load(); err != nil {
				return nil, err
			}
		}
		for _, u := range m.fix.Load.Updates {
			m.update(u)
		}
		return m.fix.Load.Result, nil
	case acp.AgentMethodSessionPrompt:
		var call promptCall
		call.ctx = ctx
		_ = json.Unmarshal(params, &call.params)
		m.mu.Lock()
		m.inflight++
		m.maxInflight = max(m.maxInflight, m.inflight)
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.inflight--
			m.mu.Unlock()
		}()
		if m.onPrompt != nil {
			return m.onPrompt(m, call)
		}
		for _, u := range m.fix.Prompt.Updates {
			m.update(u)
		}
		return m.fix.Prompt.Result, nil
	case acp.AgentMethodSessionCancel:
		m.cancelled <- struct{}{}
		return nil, nil
	case methodSteering:
		if m.onSteer != nil {
			return m.onSteer(m, params)
		}
	case acp.AgentMethodSessionSetConfigOption:
		return map[string]any{"configOptions": []any{map[string]any{"id": "mode", "currentValue": "plan"}}}, nil
	case acp.AgentMethodSessionSetMode:
		return map[string]any{}, nil
	case acp.AgentMethodSessionList:
		return map[string]any{"sessions": []any{map[string]any{"sessionId": m.sessionID(), "cwd": "/workspace", "title": "Earlier"}}}, nil
	}
	return nil, acp.NewMethodNotFound(method)
}

type recorder struct {
	mu       sync.Mutex
	states   []string
	inputs   [][]domain.RunInputRequest
	activity [][2]string
	idle     chan string
	inputsCh chan []domain.RunInputRequest
}

func newRecorder() *recorder {
	return &recorder{idle: make(chan string, 16), inputsCh: make(chan []domain.RunInputRequest, 16)}
}

func (r *recorder) config(c Config) Config {
	c.OnState = func(working bool, reason string, _ error) {
		r.mu.Lock()
		if working {
			r.states = append(r.states, "working:"+reason)
		} else {
			r.states = append(r.states, "idle:"+reason)
		}
		r.mu.Unlock()
		if !working {
			r.idle <- reason
		}
	}
	c.OnInputs = func(p []domain.RunInputRequest) {
		r.mu.Lock()
		r.inputs = append(r.inputs, p)
		r.mu.Unlock()
		r.inputsCh <- p
	}
	c.OnActivity = func(kind, target string) {
		r.mu.Lock()
		r.activity = append(r.activity, [2]string{kind, target})
		r.mu.Unlock()
	}
	return c
}

func (r *recorder) waitIdle(t *testing.T) string {
	t.Helper()
	select {
	case reason := <-r.idle:
		return reason
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the turn to end")
		return ""
	}
}

func (r *recorder) waitInputs(t *testing.T, n int) []domain.RunInputRequest {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case p := <-r.inputsCh:
			if len(p) == n {
				return p
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d pending inputs", n)
			return nil
		}
	}
}

// startMock starts a Session against the mock and stops both at cleanup.
func startMock(t *testing.T, m *mockAgent, cfg Config) (*Session, *recorder) {
	t.Helper()
	rec := newRecorder()
	if cfg.LogPath == "" {
		cfg.LogPath = filepath.Join(t.TempDir(), "run.items.jsonl")
	}
	if cfg.Cwd == "" {
		cfg.Cwd = "/workspace"
	}
	cfg.Logger = discard
	r, w := m.pipes()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Start(ctx, r, w, rec.config(cfg))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = m.stdout.Close()
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Error("session did not finish")
		}
	})
	return s, rec
}

func textPrompt(s string) []acp.ContentBlock { return []acp.ContentBlock{acp.TextBlock(s)} }

func items(t *testing.T, s *Session) []Item {
	t.Helper()
	its, err := s.Log().ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return its
}

func ofKind(its []Item, k Kind) []Item {
	var out []Item
	for _, it := range its {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

func messageText(its []Item, role string) string {
	var s string
	for _, it := range its {
		if it.Kind == KindMessage && it.Message.Role == role {
			s += it.Message.Text
		}
	}
	return s
}
