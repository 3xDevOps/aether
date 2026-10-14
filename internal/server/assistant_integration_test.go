//go:build integration

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// assistantRuntime serves every managed exec with the acpmock agent in
// process, over an e2eRuntime terminal.
type assistantRuntime struct {
	*e2eRuntime
	fixture acpmock.Fixture

	mu    sync.Mutex
	execs []*assistantExec
}

func (r *assistantRuntime) StartExecTTY(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("assistantRuntime: no terminal execs")
}

func (r *assistantRuntime) StartExecPipe(_ context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	e := &assistantExec{
		identity: runtime.ExecIdentity{ContainerID: id, ExecID: rand.Text(), CreationKey: spec.CreationKey, ClaimToken: "claim"},
		spec:     spec,
		agent:    acpmock.New(r.fixture),
		stdin:    hostOut, stdout: hostIn, agentIn: agentIn, agentOut: agentOut,
		done: make(chan struct{}),
	}
	go func() {
		e.agent.Serve(agentIn, agentOut)
		_ = agentOut.Close()
		close(e.done)
	}()
	r.mu.Lock()
	r.execs = append(r.execs, e)
	r.mu.Unlock()
	return e, nil
}

func (r *assistantRuntime) RecoverExec(context.Context, runtime.ExecIdentity) (runtime.ManagedExec, error) {
	return nil, runtime.ErrExecUnavailable
}

func (r *assistantRuntime) started() []*assistantExec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.execs)
}

type assistantExec struct {
	identity runtime.ExecIdentity
	spec     runtime.ExecSpec
	agent    *acpmock.Agent
	stdin    *io.PipeWriter
	stdout   *io.PipeReader
	agentIn  *io.PipeReader
	agentOut *io.PipeWriter
	done     chan struct{}
}

func (e *assistantExec) Identity() runtime.ExecIdentity { return e.identity }
func (e *assistantExec) Attachment() runtime.Attachment { return e }
func (e *assistantExec) Stdin() io.WriteCloser          { return e.stdin }
func (e *assistantExec) Stdout() io.Reader              { return e.stdout }
func (e *assistantExec) Stderr() io.Reader              { return strings.NewReader("") }
func (e *assistantExec) Resize(context.Context, uint, uint) error {
	return errors.New("no terminal")
}
func (e *assistantExec) Close() error  { return nil }
func (e *assistantExec) Detach() error { return nil }

func (e *assistantExec) Status(context.Context) (runtime.ExecState, error) {
	select {
	case <-e.done:
		code := 0
		return runtime.ExecState{Exited: true, ExitCode: &code}, nil
	default:
		return runtime.ExecState{Running: true, Attached: true}, nil
	}
}

func (e *assistantExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	select {
	case <-e.done:
		return runtime.ExitStatus{}, nil
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
}

func (e *assistantExec) Stop(ctx context.Context, _ time.Duration) (runtime.ExitStatus, error) {
	_ = e.agentOut.Close()
	_ = e.agentIn.Close()
	return e.Wait(ctx)
}

type assistantEnv struct {
	t      *testing.T
	srv    *Server
	rt     *assistantRuntime
	data   string
	member *domain.Member
	cancel context.CancelFunc
}

func (e *assistantEnv) call(method string, params, out any) *protocol.Error {
	e.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		e.t.Fatal(err)
	}
	result, perr := e.srv.ssh.Local(e.member.ID).Call(e.t.Context(), method, raw)
	if perr == nil && out != nil {
		if err := json.Unmarshal(result, out); err != nil {
			e.t.Fatalf("%s result: %v", method, err)
		}
	}
	return perr
}

func (e *assistantEnv) status() protocol.AssistantStatus {
	e.t.Helper()
	var status protocol.AssistantStatus
	if perr := e.call(protocol.MethodAssistantStatus, struct{}{}, &status); perr != nil {
		e.t.Fatalf("assistant.status: %v", perr)
	}
	return status
}

func (e *assistantEnv) stop() {
	e.cancel()
	_ = e.srv.Close()
}

// startAssistantServer runs a server whose claude ACP command is the mock
// agent; a non-nil prior reuses its data directory, runtime and member.
func startAssistantServer(t *testing.T, ctx context.Context, prior *assistantEnv) *assistantEnv {
	t.Helper()
	env := &assistantEnv{t: t}
	if prior != nil {
		env.rt, env.data, env.member = prior.rt, prior.data, prior.member
	} else {
		fix, err := acpmock.Load("claude")
		if err != nil {
			t.Fatal(err)
		}
		env.rt = &assistantRuntime{e2eRuntime: newE2ERuntime(), fixture: fix}
		env.data = filepath.Join(t.TempDir(), "d")
	}
	srv, err := New(ctx, Config{
		DataDir: env.data, Addr: "127.0.0.1:0", Runtime: env.rt, StandardImage: "e2e/fake",
		Harnesses: map[string]scheduler.HarnessSpec{"claude": {ACPArgs: []string{"acp-mock"}}},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	env.srv = srv
	if prior == nil {
		_, signer := writeClientKey(t)
		env.member = &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#4363d8", Role: domain.RoleAdmin}
		if err := srv.Store().CreateMember(ctx, env.member); err != nil {
			t.Fatalf("seed member: %v", err)
		}
		home := filepath.Join(env.data, "homes", string(env.member.ID))
		for _, name := range []string{".local/bin/claude", ".local/bin/claude-agent-acp", ".claude/.credentials.json"} {
			path := filepath.Join(home, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	var runCtx context.Context
	runCtx, env.cancel = context.WithCancel(ctx)
	go func() { _ = srv.Run(runCtx) }()
	waitSSHAddr(t, srv)
	return env
}

type assistantFrames struct {
	frames chan protocol.AssistantFrame
}

func (e *assistantEnv) watch(ctx context.Context, afterSeq int64) *assistantFrames {
	e.t.Helper()
	stream, ack, err := e.srv.ssh.Local(e.member.ID).Assistant(ctx, protocol.AssistantStreamRequest{AfterSeq: afterSeq})
	if err != nil {
		e.t.Fatalf("assistant stream: %v (%+v)", err, ack)
	}
	e.t.Cleanup(func() { _ = stream.Close() })
	out := &assistantFrames{frames: make(chan protocol.AssistantFrame, 256)}
	go func() {
		defer close(out.frames)
		r := bufio.NewReader(stream)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var f protocol.AssistantFrame
			if json.Unmarshal(line, &f) == nil {
				out.frames <- f
			}
		}
	}()
	return out
}

// until returns the first frame match accepts.
func (f *assistantFrames) until(t *testing.T, what string, match func(protocol.AssistantFrame) bool) protocol.AssistantFrame {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case frame, ok := <-f.frames:
			if !ok {
				t.Fatalf("stream ended before %s", what)
			}
			if match(frame) {
				return frame
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (e *assistantEnv) toolCall(conn net.Conn, r *bufio.Reader, method string) (*protocol.Error, string) {
	e.t.Helper()
	req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: json.RawMessage("{}")})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		e.t.Fatal(err)
	}
	line, err := protocol.ReadLine(r)
	if err != nil {
		e.t.Fatalf("tool %s: %v", method, err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		e.t.Fatal(err)
	}
	return resp.Error, string(line)
}

// TestIntegrationAssistant drives a member's assistant through the wired
// server: start, prompt and its streamed items, a confirmation answered
// through the tool socket, reset, and resuming after a server restart.
func TestIntegrationAssistant(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)

	if st := env.status(); st.State != protocol.AssistantStateIdle || st.Agent != "claude" {
		t.Fatalf("status before use = %+v, want idle with claude", st)
	}
	var started protocol.AssistantStatus
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, &started); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	if started.State != protocol.AssistantStateIdle || started.Agent != "claude" || started.Error != "" {
		t.Fatalf("assistant.start = %+v, want an idle claude session", started)
	}
	execs := env.rt.started()
	if len(execs) != 1 {
		t.Fatalf("%d adapters started, want 1", len(execs))
	}
	if got := strings.Join(execs[0].spec.Env, " "); got != "AETHER_ASSISTANT=1 NO_BROWSER=1" {
		t.Fatalf("adapter env = %q", got)
	}
	if !strings.HasSuffix(execs[0].spec.WorkingDir, "/.aether/assistant") {
		t.Fatalf("adapter working directory = %q, want $HOME/.aether/assistant", execs[0].spec.WorkingDir)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(env.data, "homes", string(env.member.ID), ".aether", "assistant", name))
		if err != nil || len(b) == 0 {
			t.Fatalf("%s in the assistant directory = %q, %v", name, b, err)
		}
	}

	frames := env.watch(ctx, 0)
	if first := frames.until(t, "the first status", func(f protocol.AssistantFrame) bool { return f.Status != nil }); first.Status.State != protocol.AssistantStateIdle {
		t.Fatalf("first status frame = %+v, want idle", first.Status)
	}
	prompt := protocol.AssistantPromptParams{Text: "what is running?", IdempotencyKey: "k1"}
	for range 2 {
		if perr := env.call(protocol.MethodAssistantPrompt, prompt, nil); perr != nil {
			t.Fatalf("assistant.prompt: %v", perr)
		}
	}
	frames.until(t, "the turn to end", func(f protocol.AssistantFrame) bool { return strings.Contains(string(f.Item), `"turn_end"`) })
	var history protocol.RunACPHistoryResult
	if perr := env.call(protocol.MethodAssistantHistory, protocol.AssistantHistoryParams{}, &history); perr != nil || len(history.Frames) == 0 {
		t.Fatalf("assistant.history = %+v, %v, want the turn's items", history, perr)
	}
	var item protocol.RunACPItemResult
	if perr := env.call(protocol.MethodAssistantItem, protocol.AssistantItemParams{Seq: history.Frames[0].Seq}, &item); perr != nil || len(item.Item) == 0 {
		t.Fatalf("assistant.item = %s, %v", item.Item, perr)
	}
	turns := 0
	for _, f := range history.Frames {
		if strings.Contains(string(f.Item), `"turn_start"`) {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("history holds %d turns after a duplicated prompt key, want 1", turns)
	}

	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: acpmock.PromptWait, IdempotencyKey: "k-wait"}, nil); perr != nil {
		t.Fatalf("assistant.prompt of a long turn: %v", perr)
	}
	frames.until(t, "the running status", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && f.Status.State == protocol.AssistantStateRunning
	})
	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: "again", IdempotencyKey: "k-busy"}, nil); perr == nil || perr.Code != protocol.CodeInvalidState {
		t.Fatalf("assistant.prompt during a turn = %v, want an invalid-state error", perr)
	}
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr != nil {
		t.Fatalf("assistant.cancel: %v", perr)
	}
	frames.until(t, "the idle status", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && f.Status.State == protocol.AssistantStateIdle
	})

	sock := filepath.Join(env.data, "assistant", string(env.member.ID), "sock", "assistant.sock")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial the tool socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	toolReader := bufio.NewReader(conn)
	type toolResult struct {
		err  *protocol.Error
		line string
	}
	for _, approve := range []bool{false, true} {
		done := make(chan toolResult, 1)
		go func() {
			perr, line := env.toolCall(conn, toolReader, protocol.MethodEnvSave)
			done <- toolResult{perr, line}
		}()
		pending := frames.until(t, "a pending confirmation", func(f protocol.AssistantFrame) bool {
			return f.Status != nil && len(f.Status.Confirmations) == 1
		})
		c := pending.Status.Confirmations[0]
		if c.Method != protocol.MethodEnvSave || c.Summary == "" || !c.ExpiresAt.After(c.CreatedAt) {
			t.Fatalf("confirmation = %+v", c)
		}
		if st := env.status(); len(st.Confirmations) != 1 || st.Confirmations[0].ID != c.ID {
			t.Fatalf("status confirmations = %+v, want %s", st.Confirmations, c.ID)
		}
		if perr := env.call(protocol.MethodAssistantConfirm, protocol.AssistantConfirmParams{ConfirmationID: c.ID, Approve: approve}, nil); perr != nil {
			t.Fatalf("assistant.confirm: %v", perr)
		}
		frames.until(t, "the pending set to clear", func(f protocol.AssistantFrame) bool {
			return f.Status != nil && len(f.Status.Confirmations) == 0
		})
		res := <-done
		declined := res.err != nil && strings.Contains(res.err.Message, "declined")
		if declined == approve {
			t.Fatalf("env.save with approve=%v answered %s, want declined=%v", approve, res.line, !approve)
		}
	}
	if perr := env.call(protocol.MethodAssistantConfirm, protocol.AssistantConfirmParams{ConfirmationID: "gone", Approve: true}, nil); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("assistant.confirm of an unknown id = %v, want not found", perr)
	}
	_ = conn.Close()

	env.stop()
	env = startAssistantServer(t, ctx, env)
	defer env.stop()
	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: "and now?", IdempotencyKey: "k2"}, nil); perr != nil {
		t.Fatalf("assistant.prompt after a restart: %v", perr)
	}
	execs = env.rt.started()
	if len(execs) != 2 {
		t.Fatalf("%d adapters started over both servers, want 2", len(execs))
	}
	methods := execs[1].agent.Methods()
	if slices.Contains(methods, "session/new") || !(slices.Contains(methods, "session/resume") || slices.Contains(methods, "session/load")) {
		t.Fatalf("restarted adapter received %v, want a resume of the stored session", methods)
	}
	if !slices.Contains(methods, "session/set_mode") {
		t.Fatalf("restarted adapter received %v, want the asking mode set", methods)
	}

	var reset protocol.AssistantStatus
	if perr := env.call(protocol.MethodAssistantReset, struct{}{}, &reset); perr != nil || reset.State != protocol.AssistantStateIdle {
		t.Fatalf("assistant.reset = %+v, %v", reset, perr)
	}
	if _, err := os.Stat(filepath.Join(env.data, "assistant", string(env.member.ID), "items.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("item log after reset: %v, want it removed", err)
	}
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("assistant.cancel after reset = %v, want unavailable", perr)
	}
}
