//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// mockModel stands in for a vendor's model API: streaming Anthropic
// Messages for Claude Code and streaming OpenAI Responses for Codex. Every
// conversation plays one scripted turn. A request that offers a shell tool
// gets a call running command, and the request carrying a tool's output
// gets answer as text.
type mockModel struct {
	command string
	answer  string

	mu       sync.Mutex
	requests []modelRequest
}

type modelRequest struct {
	body string
	last string
}

var shellTools = map[string]func(command string) any{
	"Bash":         func(c string) any { return map[string]string{"command": c, "description": "Run the command"} },
	"exec_command": func(c string) any { return map[string]string{"cmd": c} },
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

func (m *mockModel) record(body []byte, last json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, modelRequest{body: string(body), last: string(last)})
}

func (m *mockModel) saw(marker, want string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.ContainsFunc(m.requests, func(r modelRequest) bool {
		return strings.Contains(r.body, marker) && strings.Contains(r.body, want)
	})
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

func (m *mockModel) messages(w http.ResponseWriter, body []byte) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
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
	var last json.RawMessage
	answered := false
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(last, &blocks) == nil {
			for _, b := range blocks {
				answered = answered || b.Type == "tool_result"
			}
		}
	}
	m.record(body, last)
	tools := make([]string, len(req.Tools))
	for i, tool := range req.Tools {
		tools[i] = tool.Name
	}
	start := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": m.answer}
	stop := "end_turn"
	if name, args := m.call(tools, answered); name != "" {
		input, _ := json.Marshal(args)
		start = map[string]any{"type": "tool_use", "id": "toolu_mock", "name": name, "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
		stop = "tool_use"
	}
	writeSSE(w,
		map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "model": req.Model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0},
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
	var last json.RawMessage
	answered := false
	if n := len(req.Input); n > 0 {
		last = req.Input[n-1]
		var item struct {
			Type string `json:"type"`
		}
		answered = json.Unmarshal(last, &item) == nil && item.Type == "function_call_output"
	}
	m.record(body, last)
	tools := make([]string, len(req.Tools))
	for i, tool := range req.Tools {
		tools[i] = tool.Name
	}
	var item map[string]any
	var events []map[string]any
	if name, args := m.call(tools, answered); name != "" {
		arguments, _ := json.Marshal(args)
		item = map[string]any{
			"id": "fc_mock", "type": "function_call", "status": "completed",
			"call_id": "call_mock", "name": name, "arguments": string(arguments),
		}
		events = []map[string]any{{"type": "response.output_item.added", "output_index": 0, "item": item}}
	} else {
		item = map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": m.answer, "annotations": []any{}}},
		}
		// codex-acp forwards an answer only from its text deltas.
		events = []map[string]any{
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
				"id": "msg_mock", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
			}},
			{"type": "response.output_text.delta", "output_index": 0, "item_id": "msg_mock", "content_index": 0, "delta": m.answer},
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
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}},
		},
	)...)
}

func (m *mockModel) call(tools []string, answered bool) (string, any) {
	if answered {
		return "", nil
	}
	for _, name := range tools {
		if args, ok := shellTools[name]; ok {
			return name, args(m.command)
		}
	}
	return "", nil
}

func writeSSE(w http.ResponseWriter, events ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		data, _ := json.Marshal(e)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], data)
	}
}
