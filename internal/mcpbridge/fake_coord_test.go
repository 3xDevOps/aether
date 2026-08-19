package mcpbridge

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeCoord is a coordination socket that answers with the golden
// wire-v1 bytes. The bridge inside a container is built against exactly
// those bytes and outlives the server that wrote them, so a bridge that
// only ever sees the goldens is the honest test of that promise.
type fakeCoord struct {
	t    *testing.T
	path string

	mu       sync.Mutex
	ln       net.Listener
	requests []protocol.Request
	handler  func(req protocol.Request) protocol.Response
}

func newFakeCoord(t *testing.T, handler func(protocol.Request) protocol.Response) *fakeCoord {
	t.Helper()
	f := &fakeCoord{t: t, path: filepath.Join(t.TempDir(), "coord.sock"), handler: handler}
	f.listen()
	t.Cleanup(f.stop)
	return f
}

// listen binds the socket, replacing any listener already on it - the same
// thing the server does when it recovers a run's coordination directory.
func (f *fakeCoord) listen() {
	f.t.Helper()
	f.stop()
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		f.t.Fatalf("unlink socket: %v", err)
	}
	ln, err := net.Listen("unix", f.path)
	if err != nil {
		f.t.Fatalf("listen: %v", err)
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	go f.accept(ln)
}

func (f *fakeCoord) stop() {
	f.mu.Lock()
	ln := f.ln
	f.ln = nil
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (f *fakeCoord) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go f.serve(conn)
	}
}

func (f *fakeCoord) serve(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // test connection
	r := bufio.NewReader(conn)
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		var req protocol.Request
		if derr := json.Unmarshal(line, &req); derr != nil {
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		handler := f.handler
		f.mu.Unlock()
		resp := handler(req)
		resp.JSONRPC, resp.ID = "2.0", req.ID
		out, err := json.Marshal(resp)
		if err != nil {
			return
		}
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// seen returns the requests received so far, in order.
func (f *fakeCoord) seen() []protocol.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.Request(nil), f.requests...)
}

// golden reads one of the pinned fixtures.
func golden(t *testing.T, name string) []protocol.Response {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", "coord-v1", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var out []protocol.Response
	for _, line := range splitLines(data) {
		var resp protocol.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("decode golden %s: %v", name, err)
		}
		out = append(out, resp)
	}
	return out
}

// goldenRequests reads the pinned request fixtures as raw params, so a test
// can assert the bridge puts the same bytes on the wire.
func goldenRequests(t *testing.T) []protocol.Request {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", "coord-v1", "requests.ndjson"))
	if err != nil {
		t.Fatalf("read golden requests: %v", err)
	}
	var out []protocol.Request
	for _, line := range splitLines(data) {
		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.Fatalf("decode golden request: %v", err)
		}
		out = append(out, req)
	}
	return out
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i > start {
			out = append(out, data[start:i])
		}
		start = i + 1
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
