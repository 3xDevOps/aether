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

func TestAttachRequestShellWire(t *testing.T) {
	raw, err := json.Marshal(AttachRequest{RunID: "run-1", Shell: "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"shell":"tab-1"`) {
		t.Fatalf("attach request = %s, want shell field", raw)
	}
}
func TestAttachPositionWireCompatibility(t *testing.T) {
	const oldPayload = `{"run_id":"run-1","resume":true,"resume_id":"pty-old","cursor":17}`
	var old AttachRequest
	if err := json.Unmarshal([]byte(oldPayload), &old); err != nil {
		t.Fatal(err)
	}
	wantOld := TerminalPosition{Epoch: "pty-old", Sequence: 17}
	if old.Position != wantOld || old.ResumePosition() != wantOld || old.ResumeID != "pty-old" || old.Cursor != 17 {
		t.Fatalf("old attach position = %+v fields=(%q,%d), want %+v", old.Position, old.ResumeID, old.Cursor, wantOld)
	}

	const maxSequence = TerminalSequence(^uint64(0))
	req := AttachRequest{
		RunID: "run-1", Resume: true,
		Position: TerminalPosition{Epoch: "pty-new", Sequence: maxSequence},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"resume_id":"pty-new"`) ||
		!strings.Contains(got, `"cursor":18446744073709551615`) || strings.Contains(got, `"Position"`) {
		t.Fatalf("new attach request wire shape = %s", got)
	}
	var roundTrip AttachRequest
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Position != req.Position {
		t.Fatalf("large position round trip = %+v, want %+v", roundTrip.Position, req.Position)
	}
}

func TestAttachPositionAcceptsAbsentEpochAndZeroValues(t *testing.T) {
	var req AttachRequest
	if err := json.Unmarshal([]byte(`{"run_id":"run-1","resume":true,"cursor":9}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Position != (TerminalPosition{Sequence: 9}) || req.Position.Valid() {
		t.Fatalf("epochless request position = %+v, want invalid sequence 9", req.Position)
	}

	var empty DashAttachRequest
	if err := json.Unmarshal([]byte(`{"resume":true,"cursor":0}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Position != (TerminalPosition{}) || empty.ResumePosition().Valid() {
		t.Fatalf("zero dashboard position = %+v, want invalid zero position", empty.Position)
	}
}

func TestDashboardPositionWireCompatibility(t *testing.T) {
	want := TerminalPosition{Epoch: "pty-dashboard", Sequence: 42}
	header, err := json.Marshal(DashAttachRequest{Resume: true, Position: want})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(header); !strings.Contains(got, `"resume_id":"pty-dashboard"`) || !strings.Contains(got, `"cursor":42`) {
		t.Fatalf("dashboard header = %s, want flat position", got)
	}
	var decoded DashAttachRequest
	if err := json.Unmarshal(header, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ResumePosition() != want {
		t.Fatalf("dashboard position = %+v, want %+v", decoded.ResumePosition(), want)
	}

	var compatibility DashAttachRequest
	compatibility.SetResumePosition(want)
	if compatibility.ResumeID != "pty-dashboard" || compatibility.Cursor != 42 {
		t.Fatalf("legacy compatibility fields = (%q,%d), want (%q,%d)", compatibility.ResumeID, compatibility.Cursor, "pty-dashboard", 42)
	}

	controlOut := DashAttachControl{Type: DashAttachControlFrame, OK: true}
	controlOut.SetHighWater(want)
	state, err := json.Marshal(controlOut)
	if err != nil {
		t.Fatal(err)
	}
	var control DashAttachControl
	if err := json.Unmarshal(state, &control); err != nil {
		t.Fatal(err)
	}
	if control.HighWater() != want {
		t.Fatalf("control high-water = %+v, want %+v", control.HighWater(), want)
	}
}

func TestDashboardAttachRequestCursorUnmarshal(t *testing.T) {
	valid := []struct {
		name    string
		payload string
		epoch   TerminalEpoch
		want    uint64
	}{
		{
			name:    "safe number",
			payload: `{"resume_id":"pty-safe","cursor":9007199254740991}`,
			epoch:   "pty-safe",
			want:    9007199254740991,
		},
		{
			name:    "quoted max uint64",
			payload: `{"resume_id":"pty-max","cursor":"18446744073709551615"}`,
			epoch:   "pty-max",
			want:    ^uint64(0),
		},
		{
			name:    "zero",
			payload: `{"resume_id":"pty-zero","cursor":0}`,
			epoch:   "pty-zero",
			want:    0,
		},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			var got DashAttachRequest
			if err := json.Unmarshal([]byte(tc.payload), &got); err != nil {
				t.Fatal(err)
			}
			wantPosition := TerminalPosition{Epoch: tc.epoch, Sequence: TerminalSequence(tc.want)}
			if got.Cursor != tc.want || got.Position != wantPosition || got.ResumePosition() != wantPosition {
				t.Fatalf("decoded request = %+v, want cursor %d and position %+v", got, tc.want, wantPosition)
			}
		})
	}

	invalid := []struct {
		name    string
		payload string
	}{
		{name: "malformed string", payload: `{"resume_id":"new","cursor":"12x"}`},
		{name: "overflow", payload: `{"resume_id":"new","cursor":18446744073709551616}`},
		{name: "negative", payload: `{"resume_id":"new","cursor":-1}`},
		{name: "fraction", payload: `{"resume_id":"new","cursor":1.5}`},
		{name: "noncanonical string", payload: `{"resume_id":"new","cursor":"01"}`},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			original := DashAttachRequest{
				ResumeID: "pty-original",
				Cursor:   7,
				Position: TerminalPosition{Epoch: "pty-original", Sequence: 7},
			}
			got := original
			if err := json.Unmarshal([]byte(tc.payload), &got); err == nil {
				t.Fatalf("json.Unmarshal(%s) succeeded, want error", tc.payload)
			}
			if got != original {
				t.Fatalf("failed unmarshal mutated request to %+v, want %+v", got, original)
			}
		})
	}
}

func TestAttachResponsePositionAndResumedWire(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		response := AttachResponse{
			OK: true, Resumed: resumed,
			Position: TerminalPosition{Epoch: "pty-response", Sequence: 23},
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var got AttachResponse
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.HighWater() != response.Position || got.Resumed != resumed {
			t.Fatalf("response resumed=%v decoded as %+v", resumed, got)
		}
		if resumed && !strings.Contains(string(raw), `"resumed":true`) {
			t.Fatalf("resumed response omitted true flag: %s", raw)
		}
		if !resumed && strings.Contains(string(raw), `"resumed"`) {
			t.Fatalf("false resumed response changed legacy omission: %s", raw)
		}
	}
}

func TestAttachResponseQuotesUnsafeJavaScriptCursor(t *testing.T) {
	want := TerminalPosition{Epoch: "pty-max", Sequence: TerminalSequence(^uint64(0))}
	raw, err := json.Marshal(AttachResponse{OK: true, Position: want})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cursor":"18446744073709551615"`) {
		t.Fatalf("response = %s, want lossless quoted cursor", raw)
	}
	var got AttachResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.HighWater() != want {
		t.Fatalf("response high-water = %+v, want %+v", got.HighWater(), want)
	}
}

func TestAttachControlLeaseWire(t *testing.T) {
	req := AttachRequest{
		RunID: "run-1", Screen: true, Interactive: true,
		ControlSessionID: "tab-1", ControlGeneration: 7,
		Takeover: true, ReleaseControl: true,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got AttachRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Screen || !got.Interactive || got.ControlSessionID != req.ControlSessionID ||
		got.ControlGeneration != req.ControlGeneration || !got.Takeover || !got.ReleaseControl {
		t.Fatalf("control attach round trip = %+v, want %+v", got, req)
	}
}

func TestRunWireShape(t *testing.T) {
	created := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	r := &domain.Run{
		ID: "run_1", WorkspaceID: "ws_1", MemberID: "m_1", Task: "fix it",
		Title:   "Run title",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: "aether/run-1-fix-it", Worktree: "/var/lib/aether/checkouts/run_1",
		CreatedAt: created, UnansweredQuestions: 2,
	}
	raw, err := json.Marshal(RunFromDomain(r))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "workspace_id", "member_id", "account_member_id", "task", "title", "harness", "mode", "status", "branch", "created_at", "started_at", "finished_at", "paused", "unanswered_questions"} {
		if _, ok := m[k]; !ok {
			t.Errorf("run wire form missing key %q", k)
		}
	}
	// paused and unanswered_questions remain present on modern gateways even
	// when their values are false/zero; an older gateway may omit the latter.
	if len(m) != 15 {
		t.Errorf("run wire form has %d keys, want 15: %v", len(m), m)
	}
	if m["title"] != "Run title" {
		t.Errorf("title = %v, want Run title", m["title"])
	}
	if m["created_at"] != "2026-08-09T12:00:00Z" {
		t.Errorf("created_at = %v, want RFC3339", m["created_at"])
	}
	if m["unanswered_questions"] != float64(2) {
		t.Errorf("unanswered_questions = %v, want 2", m["unanswered_questions"])
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

func TestRunInjectParamsWireIncludesCallerKey(t *testing.T) {
	params := RunInjectParams{RunID: "run-1", Message: "continue", IdempotencyKey: "inject-1"}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var got RunInjectParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != params {
		t.Fatalf("run.inject params = %+v, want %+v", got, params)
	}
	if !strings.Contains(string(raw), `"idempotency_key":"inject-1"`) {
		t.Fatalf("run.inject params omitted caller key: %s", raw)
	}
}

func TestWorkspaceWireShapeOmitsServerConfig(t *testing.T) {
	w := &domain.Workspace{
		ID: "ws_1", Name: "proj",
		Environment: domain.WorkspaceEnvironment{
			Variables:   map[string]string{"KEY": "value"},
			SetupPolicy: domain.SetupPolicy{Script: "curl | sh"},
		},
		CreatedAt: time.Now(),
	}
	raw, err := json.Marshal(WorkspaceFromDomain(w))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"KEY", "curl"} {
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

func TestResponseLineLimitBoundsBlobMethods(t *testing.T) {
	for _, method := range []string{MethodRunPatch, MethodFilesRead, MethodFilesDiff, MethodFilesWrite, MethodConfigRead, MethodConfigWrite} {
		if got := responseLineLimit(method); got != maxBlobResponseBytes {
			t.Errorf("responseLineLimit(%q) = %d, want %d", method, got, maxBlobResponseBytes)
		}
	}
	if got := responseLineLimit(MethodServerInfo); got != MaxLineBytes {
		t.Fatalf("responseLineLimit(%q) = %d, want %d", MethodServerInfo, got, MaxLineBytes)
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
		WorkspaceID: "ws_1", RunID: "run_1", ActorID: "m_1",
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
