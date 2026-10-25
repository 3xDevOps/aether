// Package codexmock is a fake Codex app-server for tests: it answers the
// JSON-RPC lines codexapp's client sends through the bridge, and lets a test
// play the terminal's side by starting threads and changing their status.
package codexmock

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type thread struct {
	id        string
	ephemeral bool
	parent    string
	updatedAt int64
	status    map[string]any
	turns     []turn
}

type turn struct {
	id     string
	status string
}

type Request struct {
	Method string
	Params json.RawMessage
}

type Server struct {
	mu       sync.Mutex
	out      io.Writer
	threads  map[string]*thread
	order    []string
	requests []Request
	// failSteer makes turn/steer fail as it does when the turn ends first.
	failSteer bool
}

func New() *Server {
	return &Server{threads: make(map[string]*thread)}
}

// FailSteers makes every later turn/steer fail as one does when its turn
// ends first.
func (s *Server) FailSteers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failSteer = true
}

// Serve answers the requests read from in on out, until in ends.
func (s *Server) Serve(in io.Reader, out io.Writer) {
	s.mu.Lock()
	s.out = out
	s.mu.Unlock()
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 64<<10), 10<<20)
	for lines.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(lines.Bytes(), &msg) != nil || msg.Method == "" {
			continue
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: msg.Method, Params: msg.Params})
		result, failure := s.answer(msg.Method, msg.Params)
		if msg.ID != nil {
			reply := map[string]any{"id": msg.ID}
			if failure != "" {
				reply["error"] = map[string]any{"code": -32600, "message": failure}
			} else {
				reply["result"] = result
			}
			s.send(reply)
		}
		s.mu.Unlock()
	}
}

// answer runs under s.mu.
func (s *Server) answer(method string, raw json.RawMessage) (any, string) {
	var p struct {
		ThreadID       string `json:"threadId"`
		ExpectedTurnID string `json:"expectedTurnId"`
	}
	_ = json.Unmarshal(raw, &p)
	t := s.threads[p.ThreadID]
	switch method {
	case "initialize":
		return map[string]any{"userAgent": "codexmock"}, ""
	case "thread/loaded/list":
		return map[string]any{"data": s.order}, ""
	case "thread/read":
		if t == nil {
			return nil, "thread not loaded: " + p.ThreadID
		}
		return map[string]any{"thread": t.json()}, ""
	case "thread/turns/list":
		if t == nil || len(t.turns) == 0 {
			return nil, fmt.Sprintf("thread %s is not materialized yet", p.ThreadID)
		}
		last := t.turns[len(t.turns)-1]
		return map[string]any{"data": []any{map[string]any{"id": last.id, "status": last.status}}}, ""
	case "turn/start":
		if t == nil {
			return nil, "thread not found: " + p.ThreadID
		}
		if n := len(t.turns); n > 0 && t.turns[n-1].status == "inProgress" {
			return map[string]any{"turn": map[string]any{"id": t.turns[n-1].id, "status": "inProgress"}}, ""
		}
		id := fmt.Sprintf("turn-%d", len(t.turns)+1)
		t.turns = append(t.turns, turn{id: id, status: "inProgress"})
		s.setStatus(t, "active")
		return map[string]any{"turn": map[string]any{"id": id, "status": "inProgress"}}, ""
	case "turn/steer":
		n := len(t.turns)
		if s.failSteer || n == 0 || t.turns[n-1].status != "inProgress" {
			return nil, "no active turn to steer"
		}
		if p.ExpectedTurnID != t.turns[n-1].id {
			return nil, "expected active turn id " + p.ExpectedTurnID
		}
		return map[string]any{"turnId": p.ExpectedTurnID}, ""
	}
	return nil, "unsupported method " + method
}

func (t *thread) json() map[string]any {
	m := map[string]any{"id": t.id, "ephemeral": t.ephemeral, "parentThreadId": nil, "updatedAt": t.updatedAt, "status": t.status}
	if t.parent != "" {
		m["parentThreadId"] = t.parent
	}
	return m
}

// send runs under s.mu, so lines never interleave.
func (s *Server) send(msg any) {
	line, _ := json.Marshal(msg)
	_, _ = s.out.Write(append(line, '\n'))
}

func (s *Server) setStatus(t *thread, typ string, flags ...string) {
	t.status = map[string]any{"type": typ}
	if typ == "active" {
		t.status["activeFlags"] = append([]string{}, flags...)
	}
	s.send(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": t.id, "status": t.status}})
}

// LoadThread adds a thread that was loaded before the client connected, or
// that a terminal resumed: the app-server announces neither.
func (s *Server) LoadThread(id string, ephemeral bool, parent string, updatedAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[id] = &thread{id: id, ephemeral: ephemeral, parent: parent, updatedAt: updatedAt, status: map[string]any{"type": "idle"}}
	s.order = append(s.order, id)
}

// StartThread adds a thread and announces it, as a terminal's new thread is.
func (s *Server) StartThread(id string, ephemeral bool, parent string) {
	s.LoadThread(id, ephemeral, parent, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": s.threads[id].json()}})
}

func (s *Server) SetStatus(id, typ string, flags ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setStatus(s.threads[id], typ, flags...)
}

func (s *Server) EndTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.threads[id]
	if n := len(t.turns); n > 0 {
		t.turns[n-1].status = "completed"
	}
	s.setStatus(t, "idle")
}

func (s *Server) Requests(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []json.RawMessage
	for _, r := range s.requests {
		if r.Method == method {
			out = append(out, r.Params)
		}
	}
	return out
}
