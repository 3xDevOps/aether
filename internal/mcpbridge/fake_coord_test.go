package mcpbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// readUntilAcked calls the inbox tool until the service sees a read that
// carries token, then returns every inbox call seen so far.
//
// The token a read acknowledges is promoted when that read's response is
// written, which the bridge does after the calling client has already
// decoded the result. A client that reads twice in a row can therefore
// send the second read before the first read's acknowledgement exists, and
// the batch is legitimately redelivered. Retrying is what a real agent
// does; asserting a fixed call count races the promotion.
func (f *fakeCoord) readUntilAcked(t *testing.T, cs *mcp.ClientSession, token string, out *inboxOutput) []protocol.Request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var inboxes []protocol.Request
		for _, req := range f.seen() {
			if req.Method != protocol.MethodCoordInbox {
				continue
			}
			inboxes = append(inboxes, req)
			if bytes.Contains(req.Params, []byte(token)) {
				return inboxes
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no inbox read acknowledged %q after %d calls", token, len(inboxes))
		}
		// Yield between reads: the acknowledgement is promoted on the
		// bridge's write path in another goroutine, and an unthrottled
		// retry loop can starve that goroutine past the deadline on a
		// loaded CI runner.
		time.Sleep(time.Millisecond)
		callTool(t, cs, toolInbox, nil, out)
	}
}

// readUntilEmpty calls the inbox tool until it comes back empty, meaning
// the batch it last delivered has been acknowledged and retired.
//
// Same reason as readUntilAcked: the acknowledgement token is promoted on
// the bridge's write path, after the calling client already decoded its
// result, so a read issued immediately afterwards can race the promotion
// and be handed the same batch again. At-least-once delivery makes that
// correct, not a failure, so the test retries the way an agent would.
func readUntilEmpty(t *testing.T, cs *mcp.ClientSession, out *inboxOutput) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		callTool(t, cs, toolInbox, nil, out)
		if len(out.Messages) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbox never drained; still holding %+v", out.Messages)
		}
		// Same yield as readUntilAcked, for the same starvation reason.
		time.Sleep(time.Millisecond)
	}
}

// golden reads one of the pinned fixtures.
func golden(t *testing.T, name string) []protocol.Response {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", "coord-v2", name))
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
	data, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", "coord-v2", "requests.ndjson"))
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
