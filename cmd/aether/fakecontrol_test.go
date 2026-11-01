package main

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeReply is one served method: what it answers and the params of the last
// request it received.
type fakeReply struct {
	mu     sync.Mutex
	result any
	err    *protocol.Error
	got    json.RawMessage
}

func (r *fakeReply) answer(result any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
}

func (r *fakeReply) params() json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got
}

// fakeControlMethods serves every method in replies; any other method is
// answered with a method-not-found error, so a test proves which RPCs ran.
func fakeControlMethods(t *testing.T, replies map[string]*fakeReply) *protocol.Client {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			reply, served := replies[req.Method]
			if !served {
				resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected method " + req.Method}
			} else {
				reply.mu.Lock()
				reply.got = append(json.RawMessage(nil), req.Params...)
				resp.Error = reply.err
				if reply.err == nil {
					resp.Result, _ = json.Marshal(reply.result)
				}
				reply.mu.Unlock()
			}
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	return c
}
