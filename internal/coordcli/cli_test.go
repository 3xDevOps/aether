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
	unknown, raw := runCLI(t, s.path, []string{"wat"}, "")
	if unknown != ExitUsage {
		t.Fatalf("unknown command exit = %d, want %d", unknown, ExitUsage)
	}
	env = decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("unknown command envelope = %+v", env)
	}
}

func TestCLISkillPrintsAssignmentAndWorkflow(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-1","workspace_id":"ws-1","member_id":"member-1","task":"ship the release","assignment":{"mission_id":"mission-1","role":"integrator","integrator_generation":3,"execution_choices":[{"account_member_id":"member-1","harness":"claude","mode":"headless"}],"max_concurrent_attempts":2,"max_total_attempts":5,"active_attempts":1,"total_attempts":2},"peers":[],"unread":0,"capabilities":["coord.report"]}`)}
	})
	code, raw := runCLI(t, s.path, []string{"skill"}, "")
	if code != ExitOK {
		t.Fatalf("skill exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(raw, "Approved execution choices: account=member-1 harness=claude mode=headless") ||
		!strings.Contains(raw, "remaining_concurrent=1") ||
		!strings.Contains(raw, "remaining_total=3") ||
		!strings.Contains(raw, "integration request-delivery --params-file") ||
		!strings.Contains(raw, "human-decision boundary") ||
		!strings.Contains(raw, "same idempotency_key") ||
		!strings.Contains(raw, "aether-internal report is one-shot and terminal") ||
		!strings.Contains(raw, "When blocked on a peer, ask them; do not report.") {
		t.Fatalf("skill output %q does not include live identity, assignment, integrator allowance, integration recovery workflow, or terminal-report guidance", raw)
	}
	if strings.Contains(raw, "No coordination socket") {
		t.Fatalf("skill treated a reachable socket as unavailable: %q", raw)
	}
}

func TestCLISkillPrintsWorkerScope(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-worker","workspace_id":"ws-1","member_id":"member-2","task":"implement the assigned task","assignment":{"mission_id":"mission-1","role":"worker","task_id":"task-1","task_revision":2,"attempt_id":"attempt-1","capabilities":["task.show","task.propose","task.revise","integration.deliver"]},"peers":[],"unread":0,"capabilities":["coord.report","integration.deliver","integration.decide"]}`)}
	})
	code, raw := runCLI(t, s.path, []string{"skill"}, "")
	if code != ExitOK {
		t.Fatalf("skill exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(raw, "Worker scope: read and propose changes only for the assigned task; do not spawn workers. Report only after the assigned task is finished or irrecoverable; a report is terminal.") {
		t.Fatalf("worker skill output = %q", raw)
	}
	if strings.Contains(raw, "integration.") || strings.Contains(raw, "human-decision boundary") {
		t.Fatalf("worker skill granted integrator integration guidance: %q", raw)
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
	if raw == "" {
		t.Fatal("skill output is empty without a socket")
	}
	if strings.Contains(raw, "Run:") || strings.Contains(raw, "Assignment:") {
		t.Fatalf("general skill claimed live assignment: %q", raw)
	}
	if !strings.Contains(raw, "aether-internal report is one-shot and terminal") ||
		!strings.Contains(raw, "When blocked on a peer, ask them; do not report.") {
		t.Fatalf("general skill omitted terminal-report workflow: %q", raw)
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

func TestCLIMissionTaskAndWorkerAdapters(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		switch req.Method {
		case protocol.MethodTaskShow:
			return protocol.Response{Result: json.RawMessage(`{"task":{"id":"task-1","status":"ready"}}`)}
		case protocol.MethodTaskAcceptSubmission:
			var p protocol.TaskAcceptSubmissionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode accept submission: %v", err)
			}
			if p.SubmissionID != "submission-1" || p.ExpectedIntegratorGeneration != 4 || p.ExpectedAcceptedSetVersion != 8 || p.IdempotencyKey != "accept-1" || p.ScopeDisposition != "approved with documented deviation" {
				t.Fatalf("accept submission params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"task":{"id":"task-1","status":"accepted"},"acceptance":{"accepted_set_version":9}}`)}
		case protocol.MethodWorkerStart:
			var p protocol.WorkerStartParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode worker start: %v", err)
			}
			if p.DispatchKey != "dispatch-1" || p.ExpectedIntegratorGeneration != 4 {
				t.Fatalf("worker start params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"attempt":{"id":"attempt-1","state":"reserved"}}`)}
		default:
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
	})
	if code, raw := runCLI(t, s.path, []string{"task", "show", "--task-id", "task-1"}, ""); code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("task show = code %d, envelope %s", code, raw)
	}
	if code, raw := runCLI(t, s.path, []string{"task", "accept-submission", "--submission-id", "submission-1", "--expected-integrator-generation", "4", "--expected-accepted-set-version", "8", "--idempotency-key", "accept-1", "--scope-disposition", "approved with documented deviation"}, ""); code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("task accept-submission = code %d, envelope %s", code, raw)
	}
	if code, raw := runCLI(t, s.path, []string{"worker", "start", "--mission-id", "mission-1", "--task-id", "task-1", "--task-revision", "2", "--dispatch-key", "dispatch-1", "--harness", "claude", "--mode", "headless", "--account-owner-id", "member-1", "--run-owner-id", "member-1", "--expected-integrator-generation", "4"}, ""); code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("worker start = code %d, envelope %s", code, raw)
	}
}
func TestCLIIntegrationFiveCommandSurface(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		switch req.Method {
		case protocol.MethodIntegrationPrepare, protocol.MethodIntegrationShow,
			protocol.MethodIntegrationVerify, protocol.MethodIntegrationRequestDelivery,
			protocol.MethodIntegrationDeliver:
			var raw map[string]any
			if err := json.Unmarshal(req.Params, &raw); err != nil {
				t.Fatalf("decode integration params: %v", err)
			}
			if raw["actor"] != nil || raw["run_id"] != nil {
				t.Fatalf("integration params carried transport identity: %+v", raw)
			}
			return protocol.Response{Result: json.RawMessage(`{"candidate":{"candidate_id":"candidate-1"}}`)}
		default:
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
	})
	file := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(file, []byte(`{"workspace_id":"ws-1","candidate_id":"candidate-1","idempotency_key":"fixed-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"prepare", "show", "verify", "request-delivery", "deliver"} {
		args := []string{"integration", command, "--params-file", file, "--json"}
		if command == "verify" {
			args = []string{"integration", command, "--params-file", "-", "--json"}
		}
		input := ""
		if command == "verify" {
			input = `{"workspace_id":"ws-1","candidate_id":"candidate-1","candidate_revision":"rev-1","idempotency_key":"verify-1"}`
		}
		code, raw := runCLI(t, s.path, args, input)
		if code != ExitOK || !decodeEnvelope(t, raw).OK {
			t.Fatalf("integration %s = code %d, envelope %s", command, code, raw)
		}
	}
}
func TestCLIIntegrationErrorCodesMapToExitStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		exit int
	}{
		{name: "denied", code: protocol.CodeDenied, exit: ExitDenied},
		{name: "conflict", code: protocol.CodeConflict, exit: ExitDenied},
		{name: "invalid params", code: protocol.CodeInvalidParams, exit: ExitUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newCLISocket(t, func(req protocol.Request) protocol.Response {
				if req.Method != protocol.MethodIntegrationShow {
					return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
				}
				return protocol.Response{Error: &protocol.Error{Code: test.code, Message: "integration.show: " + test.name}}
			})
			code, raw := runCLI(t, s.path, []string{"integration", "show", "--params-file", "-", "--json"}, `{}`)
			if code != test.exit {
				t.Fatalf("integration %s exit = %d, want %d; output=%s", test.name, code, test.exit, raw)
			}
			env := decodeEnvelope(t, raw)
			if env.OK || env.Error == nil || env.Error.Code != test.code {
				t.Fatalf("integration %s envelope = %+v", test.name, env)
			}
		})
	}
}

func TestCLIMutationsBodiesWaitAckAndReceipts(t *testing.T) {
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
	if code, _ = runCLI(t, s.path, []string{"send", "--to", "run-2", "--body", "retry", "--idempotency-key", "send-fixed"}, ""); code != ExitOK {
		t.Fatalf("idempotent send retry exit = %d, want %d", code, ExitOK)
	}
	seen := s.requests()
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
