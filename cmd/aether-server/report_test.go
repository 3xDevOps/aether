package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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
// hook hands the reporter its event. closes says whether the harness closes
// its end afterwards.
func withStdin(t *testing.T, payload string, closes bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() {
		_, _ = w.WriteString(payload)
		if closes {
			_ = w.Close()
		}
	}()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = w.Close(); _ = r.Close() })
}

// capture points one of the process's standard streams at a pipe and
// returns what the reporter wrote to it.
func capture(t *testing.T, stream **os.File) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := *stream
	*stream = w
	t.Cleanup(func() { *stream = saved; _ = r.Close() })
	return func() string {
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("close captured stream: %v", cerr)
		}
		out, rerr := io.ReadAll(r)
		if rerr != nil {
			t.Fatalf("read captured stream: %v", rerr)
		}
		return string(out)
	}
}

// TestReportClaudeCallsRunReport: a Stop payload becomes exactly one
// run.report naming the waiting state and the reason a member reads.
func TestReportClaudeCallsRunReport(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"Stop","session_id":"abc"}`, true)
	stdout := capture(t, &os.Stdout)
	report([]string{"--socket", sock.path, "claude"})
	// The harness reads the hook's stdout: anything printed there is the
	// reporter talking to the agent instead of to the server.
	if out := stdout(); out != "" {
		t.Fatalf("the reporter wrote %q to stdout, want nothing", out)
	}

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
	withStdin(t, `{"hook_event_name":"SessionStart","source":"startup"}`, true)
	report([]string{"--socket", sock.path, "claude"})
	if req, ok := sock.next(t); ok {
		t.Fatalf("the reporter dialled for an unmapped event: %+v", req)
	}
}

// TestReportSurvivesAMissingSocket: coordination may be off, or the server
// may be restarting. The hook must still end quickly and quietly.
func TestReportSurvivesAMissingSocket(t *testing.T) {
	withStdin(t, `{"hook_event_name":"Stop"}`, true)
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

// TestReportGivesUpOnAnUnclosedStdin: the budget is the whole run, not just
// the call. A harness that writes the payload and leaves the pipe open must
// not leave the reporter sitting between the agent and its next turn.
func TestReportGivesUpOnAnUnclosedStdin(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"Stop"}`, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report([]string{"--socket", sock.path, "claude"})
	}()
	select {
	case <-done:
	case <-time.After(reportBudget + 2*time.Second):
		t.Fatal("the reporter hung on a stdin the harness never closed")
	}
	// report has already returned, so a dial would already be here.
	select {
	case req := <-sock.requests:
		t.Fatalf("the reporter dialled without a payload it could read: %+v", req)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestReportRefusesAnOversizedPayload: a hook payload carries the tool's
// own input and response verbatim, so writing a large file produces a large
// payload. One past the cap is named on stderr, not truncated - half a JSON
// document maps to nothing, which would look exactly like an event Aether
// ignores and would drop the report without a word.
func TestReportRefusesAnOversizedPayload(t *testing.T) {
	sock := newFakeCoordSocket(t)
	head, tail := `{"hook_event_name":"Stop","tool_input":"`, `"}`
	oversized := head + strings.Repeat("x", maxHookPayload+1-len(head)-len(tail)) + tail
	withStdin(t, oversized, true)
	stderr := capture(t, &os.Stderr)
	report([]string{"--socket", sock.path, "claude"})
	if msg := stderr(); !strings.Contains(msg, "larger than") {
		t.Fatalf("stderr = %q, want it to name the size cap", msg)
	}
	if req, ok := sock.next(t); ok {
		t.Fatalf("the reporter dialled with a payload it could not read: %+v", req)
	}
}
