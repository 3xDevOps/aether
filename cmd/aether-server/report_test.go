package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeCoordSocket is the server side of a run's coordination socket: it
// answers one request per connection and hands it back to the test.
type fakeCoordSocket struct {
	path     string
	requests chan protocol.Request
}

func newFakeCoordSocket(t *testing.T) *fakeCoordSocket {
	t.Helper()
	f := &fakeCoordSocket{
		path:     filepath.Join(t.TempDir(), "coord2.sock"),
		requests: make(chan protocol.Request, 4),
	}
	l, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, aerr := l.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck // test server
				line, rerr := protocol.ReadLine(bufio.NewReader(conn))
				if rerr != nil {
					return
				}
				var req protocol.Request
				if json.Unmarshal(line, &req) != nil {
					return
				}
				f.requests <- req
				_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
			}()
		}
	}()
	return f
}

func (f *fakeCoordSocket) next(t *testing.T) (protocol.Request, bool) {
	t.Helper()
	select {
	case req := <-f.requests:
		return req, true
	case <-time.After(2 * time.Second):
		return protocol.Request{}, false
	}
}

// withStdin points os.Stdin at a pipe carrying payload, the way a harness
// hook hands the reporter its event.
func withStdin(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() {
		_, _ = w.WriteString(payload)
		_ = w.Close()
	}()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// TestReportClaudeCallsRunReport: a Stop payload becomes exactly one
// run.report naming the waiting state and the reason a member reads.
func TestReportClaudeCallsRunReport(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"Stop","session_id":"abc"}`)
	report([]string{"--socket", sock.path, "claude"})

	req, ok := sock.next(t)
	if !ok {
		t.Fatal("the reporter never dialled the coordination socket")
	}
	if req.Method != protocol.MethodRunReport {
		t.Fatalf("method = %q, want %q", req.Method, protocol.MethodRunReport)
	}
	var params protocol.RunReportParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	want := protocol.RunReportParams{State: string(agentstatus.Waiting), Reason: agentstatus.ReasonInput}
	if params != want {
		t.Fatalf("params = %+v, want %+v", params, want)
	}
}

// TestReportClaudeIgnoresUnmappedEvents: an event that says nothing about
// the agent's state costs no round trip at all.
func TestReportClaudeIgnoresUnmappedEvents(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"SessionStart","source":"startup"}`)
	report([]string{"--socket", sock.path, "claude"})
	if req, ok := sock.next(t); ok {
		t.Fatalf("the reporter dialled for an unmapped event: %+v", req)
	}
}

// TestReportSurvivesAMissingSocket: coordination may be off, or the server
// may be restarting. The hook must still end quickly and quietly.
func TestReportSurvivesAMissingSocket(t *testing.T) {
	withStdin(t, `{"hook_event_name":"Stop"}`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report([]string{"--socket", filepath.Join(t.TempDir(), "absent.sock"), "claude"})
	}()
	select {
	case <-done:
	case <-time.After(reportBudget + 2*time.Second):
		t.Fatal("the reporter hung on a socket that is not there")
	}
}
