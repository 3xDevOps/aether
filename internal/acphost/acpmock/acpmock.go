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
	"time"

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
	// PromptDemo plays a paced turn with a thought, a plan, reads, a
	// command with output, an edit with a diff and a markdown answer.
	PromptDemo = "demo"
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
				map[string]any{"optionId": "allow-always", "name": "Always Allow", "kind": "allow_always"},
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
	case PromptDemo:
		return a.demo(ctx)
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

const demoAnswer = "The totals now round to cents before they are returned.\n\n" +
	"- `total` rounds with `Math.round` on the cent value\n" +
	"- the existing tests pass\n\n" +
	"```js\nexport function total(a, b) {\n  return Math.round((a + b) * 100) / 100\n}\n```\n\n" +
	"Run `npm test` again after you change the rounding rule."

func demoPlan(done int) json.RawMessage {
	steps := []string{"Read the billing module", "Run the tests", "Round totals to cents"}
	entries := make([]map[string]any, len(steps))
	for i, step := range steps {
		status := "pending"
		if i < done {
			status = "completed"
		} else if i == done {
			status = "in_progress"
		}
		entries[i] = map[string]any{"content": step, "priority": "medium", "status": status}
	}
	b, _ := json.Marshal(map[string]any{"sessionUpdate": "plan", "entries": entries})
	return b
}

func demoTool(id, kind, title, status string, extra map[string]any) json.RawMessage {
	u := map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "kind": kind, "title": title, "status": status}
	if status != "pending" && status != "in_progress" {
		u["sessionUpdate"] = "tool_call_update"
	}
	for k, v := range extra {
		u[k] = v
	}
	b, _ := json.Marshal(u)
	return b
}

func (a *Agent) demo(ctx context.Context) (any, *acp.RequestError) {
	thought, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "The billing total should round to cents; read the module and its tests first."},
	})
	read := func(id, path string) []json.RawMessage {
		loc := map[string]any{"locations": []any{map[string]any{"path": path}}}
		return []json.RawMessage{demoTool(id, "read", "Read "+path, "in_progress", loc), demoTool(id, "read", "Read "+path, "completed", loc)}
	}
	steps := [][]json.RawMessage{
		{thought},
		{demoPlan(0)},
		read("demo-read-1", "src/billing.js"),
		read("demo-read-2", "src/logging.js"),
		{demoPlan(1), demoTool("demo-test", "execute", "npm test", "in_progress", nil)},
		{demoTool("demo-test", "execute", "npm test", "completed", map[string]any{"_meta": map[string]any{
			"terminal_output": map[string]any{"data": "> project@1.0.0 test\n> node --test\n\nok 1 - total adds two amounts\nok 2 - total keeps cents\n# pass 2\n"},
			"terminal_exit":   map[string]any{"exit_code": 0},
		}})},
		{demoPlan(2), demoTool("demo-edit", "edit", "Edit src/billing.js", "in_progress", map[string]any{"locations": []any{map[string]any{"path": "src/billing.js"}}})},
		{demoTool("demo-edit", "edit", "Edit src/billing.js", "completed", map[string]any{"content": []any{map[string]any{
			"type": "diff", "path": "src/billing.js",
			"oldText": "export function total(a, b) {\n  return a + b\n}\n",
			"newText": "export function total(a, b) {\n  return Math.round((a + b) * 100) / 100\n}\n",
		}}})},
		{demoPlan(3)},
	}
	for _, step := range steps {
		select {
		case <-a.cancelled:
			return map[string]any{"stopReason": "cancelled"}, nil
		case <-ctx.Done():
			return map[string]any{"stopReason": "cancelled"}, nil
		case <-time.After(400 * time.Millisecond):
		}
		for _, u := range step {
			a.update(ctx, u)
		}
	}
	for _, word := range strings.SplitAfter(demoAnswer, " ") {
		a.text(ctx, word)
		time.Sleep(15 * time.Millisecond)
	}
	return map[string]any{"stopReason": "end_turn"}, nil
}
