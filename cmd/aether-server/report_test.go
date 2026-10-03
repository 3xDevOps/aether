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
		path:     filepath.Join(t.TempDir(), "coord3.sock"),
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

func TestReportHookDoesNotWriteStdout(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"Stop","session_id":"abc"}`, true)
	stdout := capture(t, &os.Stdout)
	report([]string{"claude", "--socket", sock.path})
	// The harness reads the hook's stdout: anything printed there is the
	// reporter talking to the agent instead of to the server.
	if out := stdout(); out != "" {
		t.Fatalf("the reporter wrote %q to stdout, want nothing", out)
	}
}

// TestReportClaudeIgnoresUnmappedEvents: an event that says nothing about
// the agent's state costs no round trip at all.
func TestReportClaudeIgnoresUnmappedEvents(t *testing.T) {
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"SessionStart","source":"startup"}`, true)
	report([]string{"claude", "--socket", sock.path})
	wantNoReport(t, sock)
}

// TestReportSurvivesAMissingSocket: coordination may be off, or the server
// may be restarting. The hook must still end quickly and quietly.
func TestReportSurvivesAMissingSocket(t *testing.T) {
	withStdin(t, `{"hook_event_name":"Stop"}`, true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report([]string{"claude", "--socket", filepath.Join(t.TempDir(), "absent.sock")})
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
	budget := reportBudget
	reportBudget = 200 * time.Millisecond
	t.Cleanup(func() { reportBudget = budget })
	sock := newFakeCoordSocket(t)
	withStdin(t, `{"hook_event_name":"Stop"}`, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report([]string{"claude", "--socket", sock.path})
	}()
	select {
	case <-done:
	case <-time.After(reportBudget + 2*time.Second):
		t.Fatal("the reporter hung on a stdin the harness never closed")
	}
	wantNoReport(t, sock)
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
	report([]string{"claude", "--socket", sock.path})
	if msg := stderr(); !strings.Contains(msg, "larger than") {
		t.Fatalf("stderr = %q, want it to name the size cap", msg)
	}
	wantNoReport(t, sock)
}

// wantNoReport: report has already returned, so a dial it should not have
// made would already be here.
func wantNoReport(t *testing.T, sock *fakeCoordSocket) {
	t.Helper()
	select {
	case req := <-sock.requests:
		t.Fatalf("the reporter dialled when it should not have: %+v", req)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReportJSONDoesNotReadStdin(t *testing.T) {
	sock := newFakeCoordSocket(t)
	// A pipe nobody ever writes to or closes: reading it would cost the
	// whole budget, so returning well inside the budget is what says the
	// reporter never touched it.
	withStdin(t, "", false)
	stderr := capture(t, &os.Stderr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report([]string{"opencode", "--socket", sock.path, "--json", `{"state":"waiting"}`})
	}()
	select {
	case <-done:
	case <-time.After(reportBudget / 2):
		t.Fatal("the reporter waited on a stdin opencode never writes to")
	}
	if msg := stderr(); msg != "" {
		t.Fatalf("stderr = %q, want a successful JSON report", msg)
	}
}

// TestReportIgnoresUnmappedHarnessEvents: an event that says nothing about
// the agent's state costs no round trip, whichever harness sent it.
func TestReportIgnoresUnmappedHarnessEvents(t *testing.T) {
	for _, args := range [][]string{
		{"codex", `{"type":"agent-turn-started"}`},
	} {
		t.Run(args[0], func(t *testing.T) {
			sock := newFakeCoordSocket(t)
			report(append([]string{args[0], "--socket", sock.path}, args[1:]...))
			wantNoReport(t, sock)
		})
	}
}

// TestReportRefusesAMalformedInvocation: the callback shapes are not
// interchangeable, and a harness that got one wrong is told so rather than
// reporting something nobody meant.
func TestReportRefusesAMalformedInvocation(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"codex"},
		{"codex", `{"type":"agent-turn-complete"}`, "extra"},
		{"pi"},
		{"pi", "agent_end"},
		{"opencode"},
		{"pi", "--json", "{"},
		{"claude", "extra"},
		{"cursor", "--event", "idle"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			sock := newFakeCoordSocket(t)
			stderr := capture(t, &os.Stderr)
			if len(args) == 0 {
				report(nil)
			} else {
				report(append([]string{args[0], "--socket", sock.path}, args[1:]...))
			}
			if msg := stderr(); msg == "" {
				t.Error("the reporter said nothing about an invocation it could not use")
			}
			wantNoReport(t, sock)
		})
	}
}
