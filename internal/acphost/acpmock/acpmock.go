// Package acpmock is a scripted ACP agent for tests that replays conversations
// recorded from the real adapters (fixtures/) and answers prompts by their text.
package acpmock

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/coordhooks"
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
	// PromptChatter runs the turn until session/cancel arrives, sending
	// only usage and command updates, every ChatterInterval: no progress.
	PromptChatter = "chatter"
	// PromptThink runs the turn until session/cancel arrives, streaming a
	// thought chunk every ChatterInterval.
	PromptThink = "think"
	// PromptGoQuiet sends one thought chunk, then waits, silent, until
	// session/cancel arrives.
	PromptGoQuiet = "go quiet"
	// PromptLongTool starts a tool call and keeps it running, silent,
	// until session/cancel arrives.
	PromptLongTool = "run a long tool"
	// ChatterInterval paces PromptChatter and PromptThink.
	ChatterInterval = 20 * time.Millisecond
	// PromptAskForm makes the agent ask a form question and reply with the
	// answer: "form: <action> <content as JSON>".
	PromptAskForm = "ask form"
	// PromptRefuse makes the agent refuse the prompt with authRequired.
	PromptRefuse = "refuse"
	// PromptUsageLimit makes the agent fail the prompt as Claude's adapter
	// does when the account's usage limit is reached
	// (acphost/testdata/claude_usage_limit.jsonl).
	PromptUsageLimit = "hit the usage limit"
	// PromptLoseSession makes the agent fail the prompt as Claude's adapter
	// does for a session it dropped when its Claude Code process died.
	PromptLoseSession = "lose the session"
	// PromptBackground starts workflow BackgroundTaskID in the background as
	// Claude's adapter reports one and ends the turn. BackgroundDelay later
	// the workflow ends and the agent runs the follow-up turn Claude Code
	// runs for it, with no prompt open. Task frames go only to a session
	// whose meta asked for them in claudeCode.emitRawSDKMessages.
	PromptBackground = "work in background"
	// PromptBackgroundPair is PromptBackground with a second workflow,
	// BackgroundTaskID+"-2", that ends and is followed up twice as late.
	PromptBackgroundPair = "work in background twice"
	// PromptDevServer starts shell DevServerTaskID in the background, which
	// never ends, and ends the turn.
	PromptDevServer = "start a dev server"
	// PromptHold starts workflow HeldTaskID in the background and ends the
	// turn; the workflow runs until PromptRelease.
	PromptHold = "hold in background"
	// PromptRelease ends the turn, and BackgroundDelay later ends HeldTaskID
	// and runs its follow-up turn.
	PromptRelease = "release background"
	// PromptDemo plays a paced turn with a thought, a plan, reads, a
	// command with output, an edit with a diff and a markdown answer.
	PromptDemo = "demo"
	// PromptDemoEdit plays PromptDemo and also writes its edit to
	// src/billing.js under the session's cwd.
	PromptDemoEdit = "demo edit"
	// PromptSubagents plays a turn in which the main agent launches
	// subagents the way Claude's adapter reports them: nested, concurrent,
	// and with later updates that omit the parent link.
	PromptSubagents = "subagents"
	// PromptWorkflow launches a dynamic workflow and a background Agent
	// the way Claude's adapter reports them: calls that complete once their
	// work runs in the background, and the Agent's work after that.
	PromptWorkflow = "workflow"
	// ReminderReply is the reply to Aether's report reminder, which the
	// mock cannot act on.
	ReminderReply = "nothing to report"
	// PromptRun followed by a shell command on the same line runs it in
	// the agent's container, as an agent's tool call would, replies with
	// its output and ends the turn. Lines after it, such as the run's
	// instructions, are ignored.
	PromptRun = "run: "
)

// BackgroundDelay is how long after PromptBackground's turn ends its
// follow-up turn starts.
var BackgroundDelay = 2 * time.Second

const (
	// BackgroundTaskID is the id of the workflow PromptBackground starts.
	BackgroundTaskID = "mock-background-task"
	// DevServerTaskID is the id of the shell PromptDevServer starts.
	DevServerTaskID = "mock-dev-server"
	// HeldTaskID is the id of the workflow PromptHold starts.
	HeldTaskID = "mock-held-task"
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
	// List holds successive session/list results; the last one repeats.
	List []json.RawMessage `json:"list"`
	// RefusePrompts makes the agent refuse every prompt with authRequired.
	RefusePrompts bool `json:"-"`
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

	mu      sync.Mutex
	session string
	cwd     string
	// frames says the session asked for Claude Code's task frames; live
	// maps each live background task to its type.
	frames  bool
	live    map[string]string
	methods []string
	prompts []json.RawMessage
	listed  int
	options []map[string]any
	// turn ends at a session/cancel that follows the latest session/prompt
	// on the wire.
	turn       context.Context
	cancelTurn context.CancelFunc
}

func New(fix Fixture) *Agent {
	return &Agent{fix: fix, ready: make(chan struct{})}
}

// gatedReader holds back reads until open is closed: the SDK's receive loop
// starts inside NewConnection and reads the logger SetLogger writes.
type gatedReader struct {
	open <-chan struct{}
	r    io.Reader
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.open
	return g.r.Read(p)
}

func (a *Agent) Serve(r io.Reader, w io.Writer) {
	in, out := io.Pipe()
	go a.route(r, out)
	open := make(chan struct{})
	a.conn = acp.NewConnection(a.handle, w, gatedReader{open, in})
	a.conn.SetLogger(slog.New(slog.DiscardHandler))
	close(open)
	close(a.ready)
	<-a.conn.Done()
	_ = in.Close()
}

// route ties each session/cancel to the prompt before it in wire order: the
// SDK runs every request handler on its own goroutine, so a cancel sent right
// after a prompt can be handled before that prompt's handler starts.
func (a *Agent) route(r io.Reader, out *io.PipeWriter) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var m struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(line, &m)
			a.mu.Lock()
			switch m.Method {
			case acp.AgentMethodSessionPrompt:
				a.turn, a.cancelTurn = context.WithCancel(context.Background())
			case acp.AgentMethodSessionCancel:
				if a.cancelTurn != nil {
					a.cancelTurn()
				}
			}
			a.mu.Unlock()
			if _, werr := out.Write(line); werr != nil {
				return
			}
		}
		if err != nil {
			_ = out.CloseWithError(err)
			return
		}
	}
}

func (a *Agent) Methods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.methods...)
}

// Prompts are the content blocks of each session/prompt, in order.
func (a *Agent) Prompts() []json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]json.RawMessage(nil), a.prompts...)
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
		Cwd       string `json:"cwd"`
		Prompt    []struct {
			Text string `json:"text"`
		} `json:"prompt"`
		Meta struct {
			ClaudeCode struct {
				EmitRawSDKMessages json.RawMessage `json:"emitRawSDKMessages"`
			} `json:"claudeCode"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(params, &p)
	frames := len(p.Meta.ClaudeCode.EmitRawSDKMessages) > 0 && string(p.Meta.ClaudeCode.EmitRawSDKMessages) != "false"
	switch method {
	case acp.AgentMethodInitialize:
		return a.fix.Initialize, nil
	case acp.AgentMethodSessionNew:
		a.mu.Lock()
		a.session, a.cwd, a.frames = a.fix.SessionID(), p.Cwd, frames
		a.mu.Unlock()
		return a.fix.SessionNew, nil
	case acp.AgentMethodSessionResume, acp.AgentMethodSessionLoad:
		a.mu.Lock()
		a.session, a.cwd, a.frames = p.SessionID, p.Cwd, frames
		a.mu.Unlock()
		if method == acp.AgentMethodSessionLoad {
			for _, u := range a.fix.Load.Updates {
				a.update(ctx, u)
			}
		}
		return a.fix.Load.Result, nil
	case acp.AgentMethodSessionPrompt:
		var raw struct {
			Prompt json.RawMessage `json:"prompt"`
		}
		_ = json.Unmarshal(params, &raw)
		a.mu.Lock()
		a.prompts = append(a.prompts, raw.Prompt)
		a.mu.Unlock()
		var text strings.Builder
		for _, b := range p.Prompt {
			text.WriteString(b.Text)
		}
		return a.prompt(ctx, text.String())
	case "_session/steering":
		a.mu.Lock()
		running := a.turn != nil && a.turn.Err() == nil
		a.mu.Unlock()
		if !running {
			return map[string]any{"outcome": "promptRequired"}, nil
		}
		return map[string]any{"outcome": "injected"}, nil
	case acp.AgentMethodSessionCancel:
		return nil, nil
	case acp.AgentMethodSessionList:
		if len(a.fix.List) == 0 {
			break
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		result := a.fix.List[min(a.listed, len(a.fix.List)-1)]
		a.listed++
		return result, nil
	case acp.AgentMethodSessionSetMode:
		return map[string]any{}, nil
	case acp.AgentMethodSessionSetConfigOption:
		var o struct {
			ConfigID string `json:"configId"`
			Value    any    `json:"value"`
		}
		_ = json.Unmarshal(params, &o)
		return map[string]any{"configOptions": a.setOption(o.ConfigID, o.Value)}, nil
	}
	return nil, acp.NewMethodNotFound(method)
}

// setOption returns the session's full option catalog with the change
// applied, as a real agent does, starting from the fixture's session/new.
func (a *Agent) setOption(id string, value any) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.options == nil {
		var r struct {
			ConfigOptions []map[string]any `json:"configOptions"`
		}
		_ = json.Unmarshal(a.fix.SessionNew, &r)
		a.options = r.ConfigOptions
	}
	for _, option := range a.options {
		if option["id"] == id {
			option["currentValue"] = value
			return a.options
		}
	}
	a.options = append(a.options, map[string]any{"id": id, "currentValue": value})
	return a.options
}

func (a *Agent) prompt(ctx context.Context, text string) (any, *acp.RequestError) {
	a.mu.Lock()
	cancelled := a.turn.Done()
	cancelTurn := a.cancelTurn
	a.mu.Unlock()
	defer cancelTurn()
	if a.fix.RefusePrompts {
		return nil, acp.NewAuthRequired(nil)
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
	case PromptUsageLimit:
		return nil, &acp.RequestError{Code: -32603, Message: "Internal error: You've hit your weekly limit · resets Oct 14, 9am (UTC)",
			Data: map[string]any{"errorKind": "rate_limit"}}
	case PromptLoseSession:
		return nil, acp.NewInternalError(map[string]any{"details": "Session not found"})
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
	case PromptDemo, PromptDemoEdit:
		return a.demo(ctx, cancelled, text == PromptDemoEdit), nil
	case PromptSubagents:
		a.subagents(ctx)
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptBackground, PromptBackgroundPair:
		a.launch(ctx, BackgroundTaskID, "local_workflow")
		time.AfterFunc(BackgroundDelay, func() { a.finish(BackgroundTaskID) })
		if text == PromptBackgroundPair {
			a.launch(ctx, BackgroundTaskID+"-2", "local_workflow")
			time.AfterFunc(2*BackgroundDelay, func() { a.finish(BackgroundTaskID + "-2") })
		}
		a.text(ctx, "started")
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptDevServer:
		a.launch(ctx, DevServerTaskID, "local_bash")
		a.text(ctx, "started")
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptHold:
		a.launch(ctx, HeldTaskID, "local_workflow")
		a.text(ctx, "started")
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptRelease:
		time.AfterFunc(BackgroundDelay, func() { a.finish(HeldTaskID) })
		a.text(ctx, "releasing")
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptWorkflow:
		a.workflow(ctx)
		return map[string]any{"stopReason": "end_turn"}, nil
	case PromptWait:
		select {
		case <-cancelled:
		case <-ctx.Done():
		}
		return map[string]any{"stopReason": "cancelled"}, nil
	case PromptChatter, PromptThink:
		usage, _ := json.Marshal(map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 100})
		commands, _ := json.Marshal(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{}})
		thought, _ := json.Marshal(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "."}})
		tick := time.NewTicker(ChatterInterval)
		defer tick.Stop()
		for {
			select {
			case <-cancelled:
				return map[string]any{"stopReason": "cancelled"}, nil
			case <-ctx.Done():
				return map[string]any{"stopReason": "cancelled"}, nil
			case <-tick.C:
			}
			if text == PromptThink {
				a.update(ctx, thought)
				continue
			}
			a.update(ctx, usage)
			a.update(ctx, commands)
		}
	case PromptLongTool, PromptGoQuiet:
		if text == PromptGoQuiet {
			thought, _ := json.Marshal(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hm"}})
			a.update(ctx, thought)
		} else {
			a.update(ctx, demoTool("long-tool", "execute", "make build", "in_progress", nil))
		}
		select {
		case <-cancelled:
		case <-ctx.Done():
		}
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	if command, ok := strings.CutPrefix(text, PromptRun); ok {
		command, _, _ = strings.Cut(command, "\n")
		out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
		if err != nil {
			out = append(out, []byte("\n"+err.Error())...)
		}
		a.text(ctx, string(out))
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	if strings.HasPrefix(text, coordhooks.ReportReminderLead) {
		// The mock cannot run aether-internal.
		a.text(ctx, ReminderReply)
		return map[string]any{"stopReason": "end_turn"}, nil
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

const demoNewBilling = "export function total(a, b) {\n  return Math.round((a + b) * 100) / 100\n}\n"

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

func (a *Agent) demo(ctx context.Context, cancelled <-chan struct{}, write bool) any {
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
			"newText": demoNewBilling,
		}}})},
		{demoPlan(3)},
	}
	editStep := len(steps) - 2
	for i, step := range steps {
		select {
		case <-cancelled:
			return map[string]any{"stopReason": "cancelled"}
		case <-ctx.Done():
			return map[string]any{"stopReason": "cancelled"}
		case <-time.After(400 * time.Millisecond):
		}
		for _, u := range step {
			a.update(ctx, u)
		}
		if write && i == editStep {
			a.mu.Lock()
			cwd := a.cwd
			a.mu.Unlock()
			err := errors.New("the session has no cwd")
			if cwd != "" {
				err = os.MkdirAll(filepath.Join(cwd, "src"), 0o755)
			}
			if err == nil {
				err = os.WriteFile(filepath.Join(cwd, "src", "billing.js"), []byte(demoNewBilling), 0o644)
			}
			if err != nil {
				a.text(ctx, "write failed: "+err.Error())
			}
		}
	}
	for _, word := range strings.SplitAfter(demoAnswer, " ") {
		a.text(ctx, word)
		time.Sleep(15 * time.Millisecond)
	}
	return map[string]any{"stopReason": "end_turn"}
}

func claudeUpdate(u map[string]any, parent, toolName string) json.RawMessage {
	cc := map[string]any{}
	if parent != "" {
		cc["parentToolUseId"] = parent
	}
	if toolName != "" {
		cc["toolName"] = toolName
	}
	if len(cc) > 0 {
		u["_meta"] = map[string]any{"claudeCode": cc}
	}
	b, _ := json.Marshal(u)
	return b
}

func (a *Agent) subagents(ctx context.Context) {
	prose := func(parent, s string) json.RawMessage {
		return claudeUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": s}}, parent, "")
	}
	call := func(id, kind, title, parent, toolName string, rawInput any) json.RawMessage {
		u := map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "kind": kind, "title": title, "status": "in_progress"}
		if rawInput != nil {
			u["rawInput"] = rawInput
		}
		return claudeUpdate(u, parent, toolName)
	}
	done := func(id, parent, toolName string) json.RawMessage {
		return claudeUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"}, parent, toolName)
	}
	launch := func(description, subagentType, prompt string) map[string]any {
		in := map[string]any{"description": description, "prompt": prompt}
		if subagentType != "" {
			in["subagent_type"] = subagentType
		}
		return in
	}
	plan := claudeUpdate(map[string]any{"sessionUpdate": "plan", "entries": []any{
		map[string]any{"content": "List the functions", "priority": "medium", "status": "completed"},
		map[string]any{"content": "Find what calls them", "priority": "medium", "status": "in_progress"},
	}}, "task-1", "")
	for _, u := range []json.RawMessage{
		prose("", "Splitting the work across subagents."),
		call("task-1", "think", "Map the billing module", "", "Agent",
			launch("Map the billing module", "Explore", "List every function in src/billing.js and what calls it.")),
		prose("task-1", "Reading the billing module."),
		plan,
		call("task-1-read", "read", "Read src/billing.js", "task-1", "Read", nil),
		call("task-1-1", "think", "Find the callers", "task-1", "Task",
			launch("Find the callers", "general-purpose", "Search for callers of total().")),
		call("task-1-1-grep", "search", "grep total(", "task-1-1", "Grep", nil),
		prose("task-1-1", "Two callers found."),
		done("task-1-1-grep", "", "Grep"),
		done("task-1-1", "task-1", ""),
		done("task-1-read", "", ""),
		done("task-1", "", "Agent"),
		call("task-2", "think", "Run the tests", "", "Agent", launch("Run the tests", "general-purpose", "Run npm test and report the result.")),
		call("task-3", "think", "Round totals to cents", "", "Agent", launch("Round totals to cents", "", "Make total() round to cents.")),
		call("task-2-bash", "execute", "npm test", "task-2", "Bash", nil),
		call("task-3-edit", "edit", "Edit src/billing.js", "task-3", "Edit", nil),
		prose("task-2", "Running the tests."),
		prose("task-3", "Rounding the total."),
		prose("task-2", "Both tests pass."),
		done("task-2-bash", "", ""),
		claudeUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "task-3-edit", "status": "completed", "content": []any{map[string]any{
			"type": "diff", "path": "src/billing.js", "oldText": "  return a + b\n", "newText": "  return Math.round((a + b) * 100) / 100\n",
		}}}, "", "Edit"),
		done("task-2", "", "Agent"),
		done("task-3", "", "Agent"),
		prose("", "All three subagents finished."),
	} {
		a.update(ctx, u)
	}
}

// launch starts background task id of type typ the way Claude's adapter
// reports it: the tool call, the live set, and the tool result.
func (a *Agent) launch(ctx context.Context, id, typ string) {
	call, tool, response := id+"-call", "Workflow", map[string]any{"status": "async_launched", "taskId": id}
	if typ == "local_bash" {
		tool, response = "Bash", map[string]any{"stdout": "", "stderr": "", "interrupted": false, "backgroundTaskId": id}
	}
	a.update(ctx, demoTool(call, "execute", tool+" "+id, "in_progress", nil))
	a.mu.Lock()
	if a.live == nil {
		a.live = map[string]string{}
	}
	a.live[id] = typ
	a.mu.Unlock()
	a.liveSet(ctx)
	a.update(ctx, demoTool(call, "execute", tool+" "+id, "completed", map[string]any{"_meta": map[string]any{
		"claudeCode": map[string]any{"toolName": tool, "toolResponse": response},
	}}))
}

// finish ends background task id and runs the follow-up turn for it.
func (a *Agent) finish(id string) {
	ctx := context.Background()
	a.mu.Lock()
	delete(a.live, id)
	a.mu.Unlock()
	a.liveSet(ctx)
	a.sdk(ctx, map[string]any{"type": "system", "subtype": "task_updated", "task_id": id, "patch": map[string]any{"status": "completed"}})
	a.sdk(ctx, map[string]any{"type": "system", "subtype": "task_notification", "task_id": id, "status": "completed", "summary": "done"})
	a.text(ctx, "background done: "+id)
	usage, _ := json.Marshal(map[string]any{
		"sessionUpdate": "usage_update", "used": 1000, "size": 200000,
		"cost":  map[string]any{"amount": 0.01, "currency": "USD"},
		"_meta": map[string]any{"_claude/origin": map[string]any{"kind": "task-notification"}},
	})
	a.update(ctx, usage)
}

func (a *Agent) liveSet(ctx context.Context) {
	a.mu.Lock()
	tasks := []map[string]any{}
	for id, typ := range a.live {
		tasks = append(tasks, map[string]any{"task_id": id, "task_type": typ, "description": id})
	}
	a.mu.Unlock()
	a.sdk(ctx, map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": tasks})
}

// sdk forwards a raw Claude Code SDK message as Claude's adapter does, to a
// session that asked for them.
func (a *Agent) sdk(ctx context.Context, message map[string]any) {
	a.mu.Lock()
	session, frames := a.session, a.frames
	a.mu.Unlock()
	if frames {
		_ = a.conn.SendNotification(ctx, "_claude/sdkMessage", map[string]any{"sessionId": session, "message": message})
	}
}

func (a *Agent) workflow(ctx context.Context) {
	input := map[string]any{"name": "audit-billing", "description": "Audit the billing module", "script": "agent('List every function in src/billing.js')"}
	a.update(ctx, claudeUpdate(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "workflow-1", "kind": "other", "title": "Workflow", "status": "in_progress", "rawInput": input,
	}, "", "Workflow"))
	done, _ := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "workflow-1", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Workflow audit-billing is running in the background."}}},
		"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "Workflow", "toolResponse": map[string]any{
			"status": "async_launched", "taskId": "mock-workflow-task", "taskType": "local_workflow", "workflowName": "audit-billing",
		}}},
	})
	a.update(ctx, done)
	in := map[string]any{"description": "Watch the build", "subagent_type": "general-purpose", "prompt": "Watch the build log.", "run_in_background": true}
	a.update(ctx, claudeUpdate(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "background-1", "kind": "think", "title": "Watch the build", "status": "in_progress", "rawInput": in,
	}, "", "Agent"))
	a.update(ctx, claudeUpdate(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "background-1", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Async agent launched successfully."}}},
		"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "Agent", "toolResponse": map[string]any{
			"status": "async_launched", "agentId": "mock-background-agent",
		}}},
	}, "", ""))
	time.Sleep(20 * time.Millisecond)
	a.update(ctx, claudeUpdate(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "background-1-read", "kind": "read", "title": "Read build.log", "status": "in_progress",
	}, "background-1", "Read"))
	a.text(ctx, "The workflow is running.")
}
