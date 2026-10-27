//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// mockModel stands in for a vendor's model API: streaming Anthropic
// Messages for Claude Code and streaming OpenAI Responses for Codex. It plays
// the script named by the last scriptTag in a request, one step per request,
// then answers as text. A conversation holding a report reminder it has not
// answered gets the report the reminder asks for, and a compaction request
// gets a summary tagged with the script's compacted name.
type mockModel struct {
	answer string

	mu       sync.Mutex
	scripts  map[string][]step
	reminded map[string]string
	requests []modelRequest
	// calls numbers the tool calls: a conversation that calls twice needs
	// distinct ids.
	calls int
}

// A step is one scripted model reply: a shell command, or a subagent
// started with a prompt. Usage is the input tokens the reply claims, to
// push the agent into compacting.
type step struct {
	command  string
	subagent string
	usage    int
}

type modelRequest struct {
	body string
	// last is what the request carries since the model's last output.
	last string
	// agent is set on a request that offers a shell tool: the agent's own
	// turn, not a side request such as Claude Code's title.
	agent bool
}

// scriptTag names a turn's script; subagentTag names a subagent's, so the
// parent's request that starts the subagent still plays the parent's.
const (
	scriptTag   = "aether-script:"
	subagentTag = "aether-subagent:"
)

func tagged(script string) string { return scriptTag + script + ";" }

func subagentTagged(script string) string { return subagentTag + script + ";" }

// compacted is the script a compaction summary of script's conversation
// plays.
func compacted(script string) string { return script + "/compacted" }

// Claude Code asks for a compaction summary in a prompt; Codex appends a
// trigger and takes the summary as a compaction item.
const (
	claudeCompactPrompt = "Your task is to create a detailed summary of the conversation so far"
	codexCompactTrigger = `{"type":"compaction_trigger"}`
)

var shellTools = map[string]func(command string) any{
	"Bash":         func(c string) any { return map[string]string{"command": c, "description": "Run the command"} },
	"exec_command": func(c string) any { return map[string]string{"cmd": c} },
}

// subagentTools start a subagent with a prompt: Claude Code's tools, and
// Codex's tool in its multi-agent namespace.
var subagentTools = map[string]func(prompt, mark string) reply{
	"Task":  claudeSubagent("Task"),
	"Agent": claudeSubagent("Agent"),
	"multi_agent_v1": func(prompt, _ string) reply {
		return reply{tool: "spawn_agent", namespace: "multi_agent_v1", args: map[string]string{"message": prompt}}
	},
}

func claudeSubagent(tool string) func(prompt, mark string) reply {
	return func(prompt, mark string) reply {
		return reply{tool: tool, args: map[string]string{"description": "Run the subagent" + mark, "prompt": prompt, "subagent_type": "general-purpose"}}
	}
}

func (m *mockModel) script(name string, steps ...step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scripts == nil {
		m.scripts = map[string][]step{}
	}
	m.scripts[name] = steps
}

// remind makes script's turn answer a report reminder by reporting
// outcome, with its flags, instead of answered.
func (m *mockModel) remind(script, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reminded == nil {
		m.reminded = map[string]string{}
	}
	m.reminded[script] = outcome
}

func (m *mockModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	// Codex opens Responses over a WebSocket first and retries a refused
	// handshake for seconds; 426 sends it straight to HTTP.
	case r.Header.Get("Upgrade") != "":
		http.Error(w, "this mock speaks HTTP only", http.StatusUpgradeRequired)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/messages"):
		m.messages(w, body)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/responses"):
		m.responses(w, body)
	default:
		http.NotFound(w, r)
	}
}

// record keeps a request without its team memory, which lists other runs'
// tasks and so their tags, and returns the body it kept.
func (m *mockModel) record(body, last []byte, tools []string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	agent := slices.ContainsFunc(tools, func(name string) bool { return shellTools[name] != nil })
	kept := teamMemory.ReplaceAllString(string(body), "")
	m.requests = append(m.requests, modelRequest{body: kept, last: string(last), agent: agent})
	return kept
}

func (m *mockModel) saw(marker, want string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.ContainsFunc(m.requests, func(r modelRequest) bool {
		return strings.Contains(r.body, marker) && strings.Contains(r.body, want)
	})
}

// bodies are the agent requests carrying marker.
func (m *mockModel) bodies(marker string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.requests {
		if r.agent && strings.Contains(r.body, marker) {
			out = append(out, r.body)
		}
	}
	return out
}

func (m *mockModel) tails(marker string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.requests {
		if strings.Contains(r.body, marker) {
			fmt.Fprintf(&b, "\n  %.400s", r.last)
		}
	}
	return b.String()
}

// reply is what the mock answers a request with: a tool call when tool is
// set, a compaction summary when compaction is, text otherwise.
type reply struct {
	tool       string
	namespace  string
	args       any
	text       string
	compaction bool
	usage      int
}

func (m *mockModel) messages(w http.ResponseWriter, body []byte) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Claude Code sends a held stop's feedback as a trailing system
	// message, after the user message holding the tool output.
	var after []json.RawMessage
	for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role != "assistant"; i-- {
		after = append([]json.RawMessage{req.Messages[i].Content}, after...)
	}
	last, _ := json.Marshal(after)
	var segments []segment
	for _, msg := range req.Messages {
		var blocks []json.RawMessage
		if json.Unmarshal(msg.Content, &blocks) != nil {
			blocks = []json.RawMessage{msg.Content}
		}
		for _, b := range blocks {
			segments = append(segments, segment{user: msg.Role == "user", text: string(b)})
		}
	}
	tools := make([]string, len(req.Tools))
	for i, tool := range req.Tools {
		tools[i] = tool.Name
	}
	rep := m.reply(tools, m.record(body, last, tools), string(last), claudeCompactPrompt, segments)
	start := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": rep.text}
	stop := "end_turn"
	if rep.tool != "" {
		input, _ := json.Marshal(rep.args)
		start = map[string]any{"type": "tool_use", "id": m.callID("toolu_mock"), "name": rep.tool, "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
		stop = "tool_use"
	}
	writeSSE(w,
		map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "model": req.Model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": rep.usage, "output_tokens": 0},
		}},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": start},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": delta},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": 5}},
		map[string]any{"type": "message_stop"},
	)
}

func (m *mockModel) responses(w http.ResponseWriter, body []byte) {
	var req struct {
		Model string            `json:"model"`
		Input []json.RawMessage `json:"input"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var after []json.RawMessage
	segments := make([]segment, len(req.Input))
	answered := false
	for i := len(req.Input) - 1; i >= 0; i-- {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(req.Input[i], &item)
		answered = answered || item.Type == "function_call" || item.Role == "assistant"
		if !answered {
			after = append([]json.RawMessage{req.Input[i]}, after...)
		}
		segments[i] = segment{user: item.Role == "user", text: string(req.Input[i])}
	}
	last, _ := json.Marshal(after)
	tools := make([]string, len(req.Tools))
	for i, tool := range req.Tools {
		tools[i] = tool.Name
	}
	rep := m.reply(tools, m.record(body, last, tools), string(last), codexCompactTrigger, segments)
	var item map[string]any
	var events []map[string]any
	switch {
	case rep.compaction:
		item = map[string]any{"id": "cmp_mock", "type": "compaction", "encrypted_content": rep.text}
		events = []map[string]any{{"type": "response.output_item.added", "output_index": 0, "item": item}}
	case rep.tool != "":
		arguments, _ := json.Marshal(rep.args)
		item = map[string]any{
			"id": m.callID("fc_mock"), "type": "function_call", "status": "completed",
			"call_id": m.callID("call_mock"), "name": rep.tool, "arguments": string(arguments),
		}
		if rep.namespace != "" {
			item["namespace"] = rep.namespace
		}
		events = []map[string]any{{"type": "response.output_item.added", "output_index": 0, "item": item}}
	default:
		item = map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": rep.text, "annotations": []any{}}},
		}
		// codex-acp forwards an answer only from its text deltas.
		events = []map[string]any{
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
				"id": "msg_mock", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
			}},
			{"type": "response.output_text.delta", "output_index": 0, "item_id": "msg_mock", "content_index": 0, "delta": rep.text},
		}
	}
	writeSSE(w, slices.Concat(
		[]map[string]any{{"type": "response.created", "response": map[string]any{
			"id": "resp_mock", "object": "response", "model": req.Model, "status": "in_progress", "output": []any{},
		}}},
		events,
		[]map[string]any{
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{
				"id": "resp_mock", "object": "response", "model": req.Model, "status": "completed", "output": []any{item},
				"usage": map[string]int{"input_tokens": rep.usage, "output_tokens": 5, "total_tokens": rep.usage + 5},
			}},
		},
	)...)
}

func (m *mockModel) callID(prefix string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return fmt.Sprintf("%s_%d", prefix, m.calls)
}

// A segment is one block of a Messages request's content or one item of a
// Responses request's input, in conversation order.
type segment struct {
	user bool
	text string
}

// reply plays the script the conversation's last tag names: the first step
// whose mark the conversation does not hold yet, then the answer. A
// request without a tag, such as Claude Code's title request, gets the
// answer. A user reminder after the tag that no reply marked with the
// script answers gets the report. Claude Code merges adjacent messages,
// moving tool outputs ahead of text, and repeats a held stop's reminder in
// a trailing system message, so the mock reads marks and user text, not
// positions of outputs.
func (m *mockModel) reply(tools []string, body, last, compactPrompt string, segments []segment) reply {
	answer := reply{text: m.answer, usage: 10}
	at, name := -1, ""
	for _, tag := range []string{scriptTag, subagentTag} {
		for i := len(segments) - 1; i >= 0 && at < 0; i-- {
			text := teamMemory.ReplaceAllString(segments[i].text, "")
			if j := strings.LastIndex(text, tag); j >= 0 {
				at = i
				name, _, _ = strings.Cut(text[j+len(tag):], ";")
			}
		}
	}
	if at < 0 {
		return answer
	}
	if strings.Contains(last, compactPrompt) {
		return reply{text: "Summary: " + tagged(compacted(name)) + " the command ran.", compaction: true, usage: 10}
	}
	reminders := 0
	for _, seg := range segments[at:] {
		if seg.user && strings.Contains(seg.text, coordhooks.ReportReminderLead) {
			reminders++
		}
	}
	m.mu.Lock()
	steps := m.scripts[name]
	outcome, ok := m.reminded[name]
	m.mu.Unlock()
	if reminders > strings.Count(body, remindedMark(name)) {
		if !ok {
			outcome = protocol.CoordOutcomeAnswered
		}
		return shellCall(tools, coordtransport.CLIPath+" report --outcome "+outcome+" --summary '"+remindedPrefix+name+"'"+remindedMark(name))
	}
	done := 0
	for done < len(steps) && strings.Contains(body, stepMark(name, done)) {
		done++
	}
	if done == len(steps) {
		return answer
	}
	s := steps[done]
	rep := shellCall(tools, s.command+stepMark(name, done))
	if s.subagent != "" {
		rep = reply{text: "the request offers no subagent tool"}
		for _, tool := range tools {
			if start, ok := subagentTools[tool]; ok {
				rep = start(s.subagent+stepMark(name, done), stepMark(name, done))
			}
		}
	}
	rep.usage = max(s.usage, 10)
	return rep
}

var teamMemory = regexp.MustCompile(`(?s)<team-memory.*?</team-memory>`)

// stepMark ends the call that plays step i of script, as a shell comment.
func stepMark(script string, i int) string { return fmt.Sprintf(" # aether-step:%s#%d;", script, i) }

// remindedMark ends the command that answers a report reminder in
// script's turn, as a shell comment.
func remindedMark(script string) string { return " # aether-reminded:" + script + ";" }

// remindedPrefix starts the summary of every report that answers a
// reminder; the script's name follows.
const remindedPrefix = "reported when reminded in "

// shellCall runs command with the request's shell tool, or answers with
// text when it offers none.
func shellCall(tools []string, command string) reply {
	for _, name := range tools {
		if args, ok := shellTools[name]; ok {
			return reply{tool: name, args: args(command), usage: 10}
		}
	}
	return reply{text: "no shell tool", usage: 10}
}

func writeSSE(w http.ResponseWriter, events ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		data, _ := json.Marshal(e)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], data)
	}
}
