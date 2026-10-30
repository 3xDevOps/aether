package coordcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type cliSocket struct {
	t       *testing.T
	path    string
	ln      net.Listener
	handler func(protocol.Request) protocol.Response

	mu   sync.Mutex
	seen []protocol.Request
}

func newCLISocket(t *testing.T, handler func(protocol.Request) protocol.Response) *cliSocket {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coord3.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &cliSocket{t: t, path: path, ln: ln, handler: handler}
	t.Cleanup(func() { _ = ln.Close() })
	go s.accept()
	return s
}

func (s *cliSocket) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *cliSocket) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := protocol.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return
	}
	var req protocol.Request
	if err = json.Unmarshal(line, &req); err != nil {
		return
	}
	s.mu.Lock()
	s.seen = append(s.seen, req)
	s.mu.Unlock()
	resp := s.handler(req)
	resp.JSONRPC, resp.ID = "2.0", req.ID
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

func (s *cliSocket) requests() []protocol.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Request(nil), s.seen...)
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func runCLI(t *testing.T, socket string, args []string, in string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code, err := Run(context.Background(), args, Config{Socket: socket, In: strings.NewReader(in), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	return code, out.String()
}

func decodeEnvelope(t *testing.T, raw string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", raw, err)
	}
	return env
}

func TestCLIJSONEnvelopeAndStableExit(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordStatus {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected"}}
		}
		return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-1","workspace_id":"ws-1","member_id":"member-1","task":"ship","peers":[],"unread":0,"capabilities":[]}`)}
	})
	code, raw := runCLI(t, s.path, []string{"status", "--json"}, "")
	if code != ExitOK {
		t.Fatalf("status exit = %d, want %d", code, ExitOK)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK || env.SchemaVersion != SchemaVersion {
		t.Fatalf("status envelope = %+v", env)
	}
	bareCode, bareRaw := runCLI(t, s.path, []string{"status"}, "")
	if bareCode != code || bareRaw != raw {
		t.Fatalf("bare status = (%d, %s), --json = (%d, %s)", bareCode, bareRaw, code, raw)
	}
	unknown, raw := runCLI(t, s.path, []string{"wat"}, "")
	if unknown != ExitUsage {
		t.Fatalf("unknown command exit = %d, want %d", unknown, ExitUsage)
	}
	env = decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("unknown command envelope = %+v", env)
	}
}

func TestCLISkillNamesTheLiveRun(t *testing.T) {
	status := protocol.CoordStatusResult{RunID: "run-current"}
	var out bytes.Buffer
	code, err := writeSkill(&out, &status)
	if err != nil || code != ExitOK {
		t.Fatalf("skill = %d, %v", code, err)
	}
	if raw := out.String(); !strings.Contains(raw, "Role: ordinary\n") || !strings.Contains(raw, "Run: run-current\n") {
		t.Fatalf("skill lost live identity: %s", raw)
	}
}

// Outcome guidance is selected by the launch mode and capabilities.
func TestCLISkillOutcomesFollowTheLaunchMode(t *testing.T) {
	reports := []string{protocol.MethodCoordInbox, protocol.MethodCoordReport}
	backgroundRule := "never report needs-input, waiting or answered"
	for _, tc := range []struct {
		name, background, enhanced string
		capabilities               []string
		want, refuse               []string
	}{
		{name: "ordinary", capabilities: reports,
			want: []string{ordinaryOutcomes, "- answered: ", "- needs-input: only", "never report waiting", coordhooks.ReviewRule}, refuse: []string{"- waiting:"}},
		{name: "enhanced", enhanced: "1", capabilities: reports,
			want:   []string{ordinaryOutcomes, "- answered: ", "- needs-input: only", "- waiting: ", "Inbox hooks: not used in Enhanced runs"},
			refuse: []string{"never report waiting", "): missing"}},
		{name: "background", background: "1", capabilities: reports,
			want: []string{backgroundOutcomes, backgroundRule}, refuse: []string{"- needs-input", "- waiting", "- answered"}},
		{name: "no reports", capabilities: []string{protocol.MethodCoordInbox},
			want: []string{"aether-internal inbox --wait 30"}, refuse: []string{"- waiting", "END EVERY TURN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(coordtransport.BackgroundEnv, tc.background)
			t.Setenv(coordtransport.EnhancedEnv, tc.enhanced)
			t.Setenv(coordtransport.WakeEnv, "")
			status := protocol.CoordStatusResult{RunID: "run-current", Capabilities: tc.capabilities}
			var out bytes.Buffer
			if code, err := writeSkill(&out, &status); err != nil || code != ExitOK {
				t.Fatalf("skill = %d, %v", code, err)
			}
			raw := out.String()
			for _, want := range tc.want {
				if !strings.Contains(raw, want) {
					t.Fatalf("skill lacks %q: %s", want, raw)
				}
			}
			for _, refuse := range tc.refuse {
				if strings.Contains(raw, refuse) {
					t.Fatalf("skill carries %q: %s", refuse, raw)
				}
			}
			if tc.name != "no reports" && !strings.Contains(raw, coordhooks.SubagentRule) {
				t.Fatalf("skill lacks the subagent rule: %s", raw)
			}
		})
	}
}

func TestCLIGeneralSkillWithoutSocket(t *testing.T) {
	var out bytes.Buffer
	oldSocketPath := defaultSocketPath
	defaultSocketPath = filepath.Join(t.TempDir(), "missing.sock")
	t.Cleanup(func() { defaultSocketPath = oldSocketPath })
	code, err := Run(context.Background(), []string{"skill"}, Config{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitOK {
		t.Fatalf("skill exit = %d, want %d", code, ExitOK)
	}
	raw := out.String()
	if strings.Contains(raw, "Run:") || strings.Contains(raw, "Mission:") {
		t.Fatalf("general skill claimed live assignment: %q", raw)
	}
	for _, command := range []string{"mission ", "integration ", "worker ", "task "} {
		if strings.Contains(raw, "aether-internal "+command) {
			t.Fatalf("unassigned skill advertised mission command %q: %s", command, raw)
		}
	}
}

func TestCLIHelpIsSocketIndependent(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"send", "--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out bytes.Buffer
			code, err := Run(context.Background(), args, Config{Socket: filepath.Join(t.TempDir(), "missing.sock"), Out: &out})
			if err != nil {
				t.Fatal(err)
			}
			if code != ExitOK {
				t.Fatalf("help exit = %d, want %d; output=%q", code, ExitOK, out.String())
			}
			if !strings.Contains(out.String(), "usage: aether-internal") {
				t.Fatalf("help output = %q", out.String())
			}
		})
	}
}

func TestCLIMutationsBodiesWaitAckAndReceipts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	const ack = "ack-1"
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		switch req.Method {
		case protocol.MethodCoordSend:
			return protocol.Response{Result: json.RawMessage(`{"message_id":"msg-1"}`)}
		case protocol.MethodCoordInbox:
			return protocol.Response{Result: json.RawMessage(`{"messages":[],"ack_token":"` + ack + `"}`)}
		case protocol.MethodCoordAsk:
			return protocol.Response{Result: json.RawMessage(`{"question_id":"q-1"}`)}
		case protocol.MethodCoordReply:
			return protocol.Response{Result: json.RawMessage(`{"message_id":"msg-2"}`)}
		case protocol.MethodCoordReport:
			return protocol.Response{Result: json.RawMessage(`{"report_id":"report-1","evidence_ref":"ev-1"}`)}
		default:
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
	})
	code, raw := runCLI(t, s.path, []string{"send", "--to", "run-2", "--body-file", "-", "--idempotency-key", "send-fixed"}, "hello from stdin")
	if code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("send = code %d, envelope %s", code, raw)
	}
	file := filepath.Join(t.TempDir(), "question.txt")
	if err := os.WriteFile(file, []byte("may I proceed?"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ = runCLI(t, s.path, []string{"inbox", "--wait", "7", "--ack", ack}, ""); code != ExitOK {
		t.Fatalf("inbox exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"ask", "--to", "run-2", "--body-file", file, "--idempotency-key", "ask-fixed"}, ""); code != ExitOK {
		t.Fatalf("ask exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"reply", "--question-id", "q-1", "--body", "yes", "--idempotency-key", "reply-fixed"}, ""); code != ExitOK {
		t.Fatalf("reply exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"report", "--outcome", "success", "--summary", "done", "--evidence-ref", "ev-existing", "--idempotency-key", "report-fixed"}, ""); code != ExitOK {
		t.Fatalf("report exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"report", "--outcome", "needs-input", "--summary", "which region?", "--idempotency-key", "ask-user"}, ""); code != ExitOK {
		t.Fatalf("needs-input report exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"report", "waiting", "--for", "ci", "--summary", "CI on PR 12", "--idempotency-key", "wait-ci"}, ""); code != ExitOK {
		t.Fatalf("waiting report exit = %d, want %d", code, ExitOK)
	}
	if code, _ = runCLI(t, s.path, []string{"send", "--to", "run-2", "--body", "retry", "--idempotency-key", "send-fixed"}, ""); code != ExitOK {
		t.Fatalf("idempotent send retry exit = %d, want %d", code, ExitOK)
	}
	seen := s.requests()
	var asked bool
	for _, req := range seen {
		var p protocol.CoordReportParams
		if req.Method == protocol.MethodCoordReport && json.Unmarshal(req.Params, &p) == nil && p.Outcome == protocol.CoordOutcomeNeedsInput {
			asked = p.Summary == "which region?" && p.IdempotencyKey == "ask-user"
		}
	}
	var waited bool
	for _, req := range seen {
		var p protocol.CoordReportParams
		if req.Method == protocol.MethodCoordReport && json.Unmarshal(req.Params, &p) == nil && p.Outcome == protocol.CoordOutcomeWaiting {
			waited = p.WaitingFor == protocol.CoordWaitingForCI && p.Summary == "CI on PR 12"
		}
	}
	if !asked || !waited {
		t.Fatalf("no needs-input or waiting report reached the socket: %+v", seen)
	}
	var sends []protocol.CoordSendParams
	for _, req := range seen {
		if req.Method != protocol.MethodCoordSend {
			continue
		}
		var p protocol.CoordSendParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		sends = append(sends, p)
	}
	if len(sends) != 2 || sends[0].Body != "hello from stdin" || sends[0].IdempotencyKey != "send-fixed" || sends[1].IdempotencyKey != sends[0].IdempotencyKey {
		t.Fatalf("send requests = %+v", sends)
	}
	for _, req := range seen {
		if req.Method != protocol.MethodCoordInbox {
			continue
		}
		var p protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.WaitSeconds != 7 || p.AckToken != ack {
			t.Fatalf("inbox params = %+v", p)
		}
	}
}

func TestCLIDeniedExitAndGeneratedIdempotency(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordSend {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
		var p protocol.CoordSendParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatalf("decode generated send: %v", err)
		}
		if p.IdempotencyKey == "" {
			t.Error("generated idempotency key is empty")
		}
		return protocol.Response{Error: &protocol.Error{Code: protocol.CodeDenied, Message: "peer denied"}}
	})
	code, raw := runCLI(t, s.path, []string{"send", "--to", "run-2", "--body", "hello"}, "")
	if code != ExitDenied {
		t.Fatalf("denied exit = %d, want %d", code, ExitDenied)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeDenied {
		t.Fatalf("denied envelope = %+v", env)
	}
}

func TestCLIInboxMaximumWaitReturnsEmptyResult(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordInbox {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
		var params protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode inbox params: %v", err)
		}
		if params.WaitSeconds != protocol.CoordMaxInboxWaitSeconds {
			t.Fatalf("wait_seconds = %d, want %d", params.WaitSeconds, protocol.CoordMaxInboxWaitSeconds)
		}
		return protocol.Response{Result: json.RawMessage(`{"messages":[]}`)}
	})

	code, raw := runCLI(t, s.path, []string{"inbox", "--wait", "30"}, "")
	if code != ExitOK {
		t.Fatalf("maximum inbox wait exit = %d, want %d", code, ExitOK)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("maximum inbox wait envelope = %+v", env)
	}
	var result protocol.CoordInboxResult
	data, err := json.Marshal(env.Result)
	if err != nil {
		t.Fatalf("marshal inbox result: %v", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode inbox result: %v", err)
	}
	if result.Messages == nil || len(result.Messages) != 0 {
		t.Fatalf("inbox result = %+v, want a valid empty result", result)
	}
}

func TestCLIFlagParseErrorsAreInvalidParams(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "status unknown", args: []string{"status", "--unknown"}},
		{name: "status malformed", args: []string{"status", "--json=not-bool"}},
		{name: "status positional", args: []string{"status", "extra"}},
		{name: "status json positional", args: []string{"status", "--json", "extra"}},
		{name: "skill unknown", args: []string{"skill", "--unknown"}},
		{name: "send unknown", args: []string{"send", "--unknown"}},
		{name: "inbox unknown", args: []string{"inbox", "--unknown"}},
		{name: "inbox malformed", args: []string{"inbox", "--wait", "not-a-number"}},
		{name: "ask unknown", args: []string{"ask", "--unknown"}},
		{name: "reply unknown", args: []string{"reply", "--unknown"}},
		{name: "report unknown", args: []string{"report", "--unknown"}},
		{name: "report malformed", args: []string{"report", "--evidence-ref", ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, raw := runCLI(t, "", test.args, "")
			if code != ExitUsage {
				t.Fatalf("exit = %d, want %d; output = %s", code, ExitUsage, raw)
			}
			env := decodeEnvelope(t, raw)
			if env.OK || env.Error == nil || env.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("envelope = %+v, want invalid params", env)
			}
		})
	}
}

func TestCLIGeneratedKeyWriteFailurePreventsMutation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		in   string
	}{
		{name: "send", args: []string{"send", "--to", "run-2", "--body", "hello"}},
		{name: "ask", args: []string{"ask", "--to", "run-2", "--body", "may I proceed?"}},
		{name: "reply", args: []string{"reply", "--question-id", "question-1", "--body", "yes"}},
		{name: "report", args: []string{"report", "--outcome", "success", "--summary", "done"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newCLISocket(t, func(req protocol.Request) protocol.Response {
				t.Errorf("mutation request %s issued after idempotency-key write failure", req.Method)
				return protocol.Response{Error: &protocol.Error{Code: protocol.CodeInternal, Message: "unexpected request"}}
			})
			var out bytes.Buffer
			code, runErr := Run(context.Background(), test.args, Config{
				Socket: s.path,
				In:     strings.NewReader(test.in),
				Out:    &out,
				ErrOut: failingWriter{err: errors.New("stderr is closed")},
			})
			if runErr != nil {
				t.Fatalf("run: %v", runErr)
			}
			if code != ExitFailure {
				t.Fatalf("exit = %d, want %d; output = %s", code, ExitFailure, out.String())
			}
			env := decodeEnvelope(t, out.String())
			if env.OK || env.Error == nil ||
				env.Error.Message != "write idempotency key: stderr is closed" {
				t.Fatalf("failure envelope = %+v, want contextual stderr error", env)
			}
			if got := s.requests(); len(got) != 0 {
				t.Fatalf("requests after idempotency-key write failure = %+v, want none", got)
			}
		})
	}
}

func TestCLIOutputFailuresReturnContext(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-1","workspace_id":"ws-1","member_id":"member-1","peers":[],"unread":0,"capabilities":[]}`)}
	})
	writeErr := errors.New("stdout is closed")
	tests := []struct {
		name    string
		args    []string
		context string
	}{
		{name: "error envelope", args: []string{"unknown"}, context: "write error response"},
		{name: "success envelope", args: []string{"status", "--json"}, context: "write success response"},
		{name: "skill text", args: []string{"skill"}, context: "write skill header"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, err := Run(context.Background(), test.args, Config{
				Socket: s.path,
				Out:    failingWriter{err: writeErr},
			})
			if code != ExitFailure || !errors.Is(err, writeErr) || !strings.Contains(err.Error(), test.context) {
				t.Fatalf("Run = (%d, %v), want (%d, %q wrapping %v)", code, err, ExitFailure, test.context, writeErr)
			}
		})
	}
}

func TestCLIAckSpendsOneInboxRead(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordInbox {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
		return protocol.Response{Result: json.RawMessage(`{"messages":[],"acked":true}`)}
	})
	if code, raw := runCLI(t, s.path, []string{"ack", "ack-1"}, ""); code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("ack = code %d, envelope %s", code, raw)
	}
	if reads := len(s.requests()); reads != 1 {
		t.Fatalf("ack made %d inbox reads, want 1", reads)
	}
}

// An agent's name need not equal its command, and AETHER_HARNESS carries the
// name. The skill must still print a worker's commands and say plainly that
// no packaged hooks match the name.
func TestCLISkillForAnAgentNamedApartFromItsCommand(t *testing.T) {
	t.Setenv("AETHER_HARNESS", "mycursor")
	t.Setenv("HOME", t.TempDir())
	status := protocol.CoordStatusResult{
		RunID:        "run-current",
		Capabilities: []string{protocol.MethodCoordInbox, protocol.MethodCoordReport},
	}
	var out bytes.Buffer
	if code, err := writeSkill(&out, &status); err != nil || code != ExitOK {
		t.Fatalf("skill = %d, %v", code, err)
	}
	raw := out.String()
	if !strings.Contains(raw, ordinaryOutcomes) {
		t.Fatalf("skill lacks the outcome guidance: %s", raw)
	}
	if !strings.Contains(raw, "Inbox hooks: no packaged integration for harness mycursor; see aether-internal skill --hooks.") {
		t.Fatalf("skill hook summary = %s", raw)
	}
}

// The shipped cursor agent runs as agent, but AETHER_HARNESS carries its
// name, so the skill reports Cursor's packaged hooks.
func TestCLISkillForCursorReportsCursorHooks(t *testing.T) {
	t.Setenv("AETHER_HARNESS", "cursor")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	status := protocol.CoordStatusResult{
		RunID:        "run-current",
		Capabilities: []string{protocol.MethodCoordInbox},
	}
	var out bytes.Buffer
	if code, err := writeSkill(&out, &status); err != nil || code != ExitOK {
		t.Fatalf("skill = %d, %v", code, err)
	}
	if raw := out.String(); !strings.Contains(raw, "Inbox hooks (cursor): missing") || strings.Contains(raw, "no packaged integration") {
		t.Fatalf("skill hook summary = %s", raw)
	}
}

func TestCLISkillForAntigravityReportsNoPackagedHooks(t *testing.T) {
	t.Setenv("AETHER_HARNESS", "antigravity")
	t.Setenv("HOME", t.TempDir())
	status := protocol.CoordStatusResult{
		RunID:        "run-current",
		Capabilities: []string{protocol.MethodCoordInbox},
	}
	var out bytes.Buffer
	if code, err := writeSkill(&out, &status); err != nil || code != ExitOK {
		t.Fatalf("skill = %d, %v", code, err)
	}
	if raw := out.String(); !strings.Contains(raw, "Inbox hooks: no packaged integration for harness antigravity;") {
		t.Fatalf("skill hook summary = %s", raw)
	}
}
