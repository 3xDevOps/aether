// Package acpmock is a scripted ACP agent for tests that replays conversations
// recorded from the real adapters (fixtures/) and answers prompts by their text.
package acpmock

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

//go:embed fixtures/*.json
var fixtures embed.FS

const (
	// PromptAskPermission makes the agent ask permission for a tool call
	// and reply with the outcome: "permission: <option id>" or
	// "permission: cancelled".
	PromptAskPermission = "ask permission"
	// PromptWait makes the turn run until session/cancel arrives.
	PromptWait = "wait"
	// PromptAskForm makes the agent ask a form question and reply with the
	// answer: "form: <action> <content as JSON>".
	PromptAskForm = "ask form"
	// PromptRefuse makes the agent refuse the prompt with authRequired.
	PromptRefuse = "refuse"
)

type Fixture struct {
	Initialize json.RawMessage `json:"initialize"`
	SessionNew json.RawMessage `json:"session_new"`
	Prompt     struct {
		Updates []json.RawMessage `json:"updates"`
		Result  json.RawMessage   `json:"result"`
	} `json:"prompt"`
	Load struct {
		Updates []json.RawMessage `json:"updates"`
		Result  json.RawMessage   `json:"result"`
	} `json:"load"`
}

func Load(name string) (Fixture, error) {
	var f Fixture
	b, err := fixtures.ReadFile("fixtures/" + name + ".json")
	if err != nil {
		return f, fmt.Errorf("acpmock: %w", err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("acpmock: fixture %s: %w", name, err)
	}
	return f, nil
}

func (f Fixture) SessionID() string {
	var r struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(f.SessionNew, &r)
	return r.SessionID
}

type Agent struct {
	fix   Fixture
	conn  *acp.Connection
	ready chan struct{}

	mu        sync.Mutex
	session   string
	methods   []string
	cancelled chan struct{}
}

func New(fix Fixture) *Agent {
	return &Agent{fix: fix, ready: make(chan struct{}), cancelled: make(chan struct{}, 1)}
}

func (a *Agent) Serve(r io.Reader, w io.Writer) {
	a.conn = acp.NewConnection(a.handle, w, r)
	a.conn.SetLogger(slog.New(slog.DiscardHandler))
	close(a.ready)
	<-a.conn.Done()
}

func (a *Agent) Methods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.methods...)
}

func (a *Agent) update(ctx context.Context, u json.RawMessage) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	_ = a.conn.SendNotification(ctx, acp.ClientMethodSessionUpdate, map[string]any{"sessionId": session, "update": u})
}

func (a *Agent) text(ctx context.Context, s string) {
	b, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": s},
	})
	a.update(ctx, b)
}

func (a *Agent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	<-a.ready
	a.mu.Lock()
	a.methods = append(a.methods, method)
	a.mu.Unlock()
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case acp.AgentMethodInitialize:
		return a.fix.Initialize, nil
	case acp.AgentMethodSessionNew:
		a.mu.Lock()
		a.session = a.fix.SessionID()
		a.mu.Unlock()
		return a.fix.SessionNew, nil
	case acp.AgentMethodSessionResume, acp.AgentMethodSessionLoad:
		a.mu.Lock()
		a.session = p.SessionID
		a.mu.Unlock()
		if method == acp.AgentMethodSessionLoad {
			for _, u := range a.fix.Load.Updates {
				a.update(ctx, u)
			}
		}
		return a.fix.Load.Result, nil
	case acp.AgentMethodSessionPrompt:
		var text strings.Builder
		for _, b := range p.Prompt {
			text.WriteString(b.Text)
		}
		return a.prompt(ctx, text.String())
	case acp.AgentMethodSessionCancel:
		select {
		case a.cancelled <- struct{}{}:
		default:
		}
		return nil, nil
	case acp.AgentMethodSessionSetMode:
		return map[string]any{}, nil
	case acp.AgentMethodSessionSetConfigOption:
		var o struct {
			ConfigID string `json:"configId"`
			Value    any    `json:"value"`
		}
		_ = json.Unmarshal(params, &o)
		return map[string]any{"configOptions": []any{map[string]any{"id": o.ConfigID, "currentValue": o.Value}}}, nil
	}
	return nil, acp.NewMethodNotFound(method)
}

func (a *Agent) prompt(ctx context.Context, text string) (any, *acp.RequestError) {
	select {
	case <-a.cancelled:
	default:
	}
	switch text {
	case PromptAskPermission:
		a.mu.Lock()
		session := a.session
		a.mu.Unlock()
		res, err := acp.SendRequest[struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}](a.conn, ctx, acp.ClientMethodSessionRequestPermission, map[string]any{
			"sessionId": session,
			"toolCall":  map[string]any{"toolCallId": "mock-tool", "title": "rm -rf build", "kind": "execute", "status": "pending"},
			"options": []any{
				map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
			},
		})
		if err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		answer := res.Outcome.OptionID
		if res.Outcome.Outcome == "cancelled" {
			answer = "cancelled"
		}
		a.text(ctx, "permission: "+answer)
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptRefuse:
		return nil, acp.NewAuthRequired(nil)
	case PromptAskForm:
		res, err := acp.SendRequest[struct {
			Action  string         `json:"action"`
			Content map[string]any `json:"content"`
		}](a.conn, ctx, acp.ClientMethodElicitationCreate, map[string]any{
			"requestId": 1, "mode": "form", "message": "Which database?",
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"db": map[string]any{"type": "string"}}},
		})
		if err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		content, _ := json.Marshal(res.Content)
		a.text(ctx, "form: "+res.Action+" "+string(content))
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptWait:
		select {
		case <-a.cancelled:
		case <-ctx.Done():
		}
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	for _, u := range a.fix.Prompt.Updates {
		a.update(ctx, u)
	}
	return a.fix.Prompt.Result, nil
}
