package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunWireShape(t *testing.T) {
	created := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	r := &domain.Run{
		ID: "run_1", SessionID: "sess_1", MemberID: "m_1", Task: "fix it",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: "aether/run-1-fix-it", Worktree: "/var/lib/aether/checkouts/run_1",
		CreatedAt: created,
	}
	raw, err := json.Marshal(RunFromDomain(r))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "session_id", "member_id", "task", "harness", "mode", "status", "branch", "created_at", "started_at", "finished_at", "paused"} {
		if _, ok := m[k]; !ok {
			t.Errorf("run wire form missing key %q", k)
		}
	}
	// paused is deliberately not omitempty: absence means "gateway too old
	// to know", so an unpaused run must still serialize paused:false.
	if len(m) != 12 {
		t.Errorf("run wire form has %d keys, want 12: %v", len(m), m)
	}
	if m["started_at"] != nil || m["finished_at"] != nil {
		t.Errorf("unset times must marshal as null, got %v / %v", m["started_at"], m["finished_at"])
	}
	if m["created_at"] != "2026-08-09T12:00:00Z" {
		t.Errorf("created_at = %v, want RFC3339", m["created_at"])
	}
	if strings.Contains(string(raw), "checkouts") {
		t.Error("host paths leaked onto the wire")
	}

	started := created.Add(time.Minute)
	r.StartedAt = &started
	raw, _ = json.Marshal(RunFromDomain(r))
	if !strings.Contains(string(raw), `"started_at":"2026-08-09T12:01:00Z"`) {
		t.Errorf("started_at not RFC3339: %s", raw)
	}
}

func TestWorkspaceWireShapeOmitsServerConfig(t *testing.T) {
	w := &domain.Workspace{
		ID: "ws_1", Name: "proj",
		Environment: domain.WorkspaceEnvironment{
			CustomImage: "secret-image",
			Variables:   map[string]string{"KEY": "value"},
			SetupPolicy: domain.SetupPolicy{Script: "curl | sh"},
		},
		CreatedAt: time.Now(),
	}
	raw, err := json.Marshal(WorkspaceFromDomain(w))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-image", "KEY", "curl"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("workspace wire form leaks %q: %s", leak, raw)
		}
	}
}

func TestErrorImplementsError(t *testing.T) {
	err := &Error{Code: CodeNotFound, Message: "no such run"}
	if !strings.Contains(err.Error(), "no such run") || !strings.Contains(err.Error(), "-32000") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestReadLineEnforcesMaxLine(t *testing.T) {
	long := strings.Repeat("a", MaxLineBytes+2)
	r := bufio.NewReader(strings.NewReader(long))
	if _, err := ReadLine(r); err == nil {
		t.Error("expected error for an oversized line")
	}

	r = bufio.NewReaderSize(strings.NewReader("{\"x\":1}\nrest"), 16)
	line, err := ReadLine(r)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if string(line) != `{"x":1}` {
		t.Errorf("line = %q", line)
	}
}

func TestClientCall(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close() //nolint:errcheck

	go func() {
		r := bufio.NewReader(server)
		for {
			line, err := ReadLine(r)
			if err != nil {
				return
			}
			var req Request
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			var resp Response
			switch req.Method {
			case MethodServerInfo:
				result, _ := json.Marshal(ServerInfoResult{ProtocolVersion: Version, ServerVersion: "test"})
				resp = Response{JSONRPC: "2.0", ID: req.ID, Result: result}
			default:
				resp = Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: "nope"}}
			}
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()

	c := NewClient(client)
	defer c.Close() //nolint:errcheck
	var info ServerInfoResult
	if err := c.Call(MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if info.ProtocolVersion != Version || info.ServerVersion != "test" {
		t.Errorf("result = %+v", info)
	}

	err := c.Call("bogus.method", nil, nil)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeMethodNotFound {
		t.Errorf("err = %v, want method-not-found *Error", err)
	}
}

func TestEventWirePayloadIsRaw(t *testing.T) {
	ev := Event{
		ID: "evt_1", Seq: 42, Time: "2026-08-09T12:00:00Z",
		SessionID: "sess_1", RunID: "run_1", ActorID: "m_1",
		Type: "run.status", Payload: json.RawMessage(`{"to":"running"}`),
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"payload":{"to":"running"}`) {
		t.Errorf("payload not embedded raw: %s", raw)
	}
	if !strings.Contains(string(raw), `"seq":42`) {
		t.Errorf("seq missing: %s", raw)
	}
}
