//go:build integration

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
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

func (r *assistantRuntime) RecoverExec(_ context.Context, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	for _, e := range r.started() {
		if e.identity == identity {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, runtime.ErrNotFound)
}

// adapters are the execs the assistant started.
func (r *assistantRuntime) adapters() []*assistantExec {
	return slices.DeleteFunc(r.started(), func(e *assistantExec) bool { return !strings.HasPrefix(e.spec.CreationKey, "assistant-") })
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

func (e *assistantExec) stopped() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
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
		env.member = seedAssistantMember(t, ctx, srv, env.data, domain.RoleAdmin)
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
	execs := env.rt.adapters()
	if len(execs) != 1 {
		t.Fatalf("%d adapters started, want 1", len(execs))
	}
	if got := strings.Join(execs[0].spec.Env, " "); got != "AETHER_ASSISTANT=1 NO_BROWSER=1" {
		t.Fatalf("adapter env = %q", got)
	}
	if !strings.HasSuffix(execs[0].spec.WorkingDir, "/.aether/assistant") {
		t.Fatalf("adapter working directory = %q, want $HOME/.aether/assistant", execs[0].spec.WorkingDir)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".claude/settings.json"} {
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
	stale, err := env.rt.StartExecPipe(ctx, execs[0].identity.ContainerID, runtime.ExecSpec{Argv: []string{"stale"}, CreationKey: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(stale.Identity())
	row, err := env.srv.Store().GetAssistantSession(ctx, env.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.ExecIdentity = string(identity)
	if err := env.srv.Store().PutAssistantSession(ctx, row); err != nil {
		t.Fatal(err)
	}
	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: "and now?", IdempotencyKey: "k2"}, nil); perr != nil {
		t.Fatalf("assistant.prompt after a restart: %v", perr)
	}
	if !stale.(*assistantExec).stopped() {
		t.Fatal("the recorded stale adapter was not stopped before the new one started")
	}
	execs = env.rt.adapters()
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

	viewer := env.watch(ctx, 0)
	viewer.until(t, "the first status", func(f protocol.AssistantFrame) bool { return f.Status != nil })
	var reset protocol.AssistantStatus
	if perr := env.call(protocol.MethodAssistantReset, struct{}{}, &reset); perr != nil || reset.State != protocol.AssistantStateIdle {
		t.Fatalf("assistant.reset = %+v, %v", reset, perr)
	}
	frame := viewer.until(t, "a reset frame", func(f protocol.AssistantFrame) bool { return f.Reset })
	if frame.Epoch/assistant.EpochStride != 2 {
		t.Fatalf("reset frame epoch %d, want the second generation's", frame.Epoch)
	}
	if _, err := os.Stat(filepath.Join(env.data, "assistant", string(env.member.ID), "items.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("item log after reset: %v, want it removed", err)
	}
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("assistant.cancel after reset = %v, want unavailable", perr)
	}
}

// seedAssistantMember adds a member whose claude agent is ready.
func seedAssistantMember(t *testing.T, ctx context.Context, srv *Server, data string, role domain.Role) *domain.Member {
	t.Helper()
	_, signer := writeClientKey(t)
	member := &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#4363d8", Role: role}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	home := filepath.Join(data, "homes", string(member.ID))
	for _, name := range []string{".local/bin/claude", ".local/bin/claude-agent-acp", ".claude/.credentials.json"} {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return member
}

// TestIntegrationAssistantRemoveDuringStart removes a member while their
// assistant starts: each holds a lock the other waits on unless the
// assistant is stopped first.
func TestIntegrationAssistantRemoveDuringStart(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	for range 5 {
		victim := seedAssistantMember(t, ctx, env.srv, env.data, domain.RoleCollaborator)
		done := make(chan string, 3)
		go func() {
			_, _ = env.srv.ssh.Local(victim.ID).Call(ctx, protocol.MethodAssistantStart, json.RawMessage("{}"))
			done <- "start"
		}()
		go func() {
			params, _ := json.Marshal(protocol.AssistantPromptParams{Text: "hi", IdempotencyKey: "p-" + string(victim.ID)})
			for range 20 {
				_, _ = env.srv.ssh.Local(victim.ID).Call(ctx, protocol.MethodAssistantPrompt, params)
			}
			done <- "prompt"
		}()
		go func() {
			params, _ := json.Marshal(protocol.MemberRemoveParams{MemberID: string(victim.ID)})
			if _, perr := env.srv.ssh.Local(env.member.ID).Call(ctx, protocol.MethodMemberRemove, params); perr != nil {
				t.Errorf("member.remove: %v", perr)
			}
			done <- "remove"
		}()
		for range 3 {
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("assistant start and member.remove did not both finish: deadlock")
			}
		}
		if _, err := env.srv.Store().GetMember(ctx, victim.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("member after member.remove: %v, want it gone", err)
		}
		if _, err := os.Stat(filepath.Join(env.data, "assistant", string(victim.ID))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("assistant files after member.remove and prompts: %v, want none", err)
		}
	}
}

// TestIntegrationAssistantCrash ends the adapter behind the assistant's back:
// the next prompt starts a new one whose recorded identity survives the old
// session's late cleanup, and a reset after another crash is not undone.
func TestIntegrationAssistantCrash(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	first := env.rt.adapters()[0]
	if _, err := first.Stop(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: "after the crash", IdempotencyKey: "c1"}, nil); perr != nil {
		t.Fatalf("assistant.prompt after a crash: %v", perr)
	}
	adapters := env.rt.adapters()
	if len(adapters) != 2 {
		t.Fatalf("%d adapters, want a new one after the crash", len(adapters))
	}
	identity, _ := json.Marshal(adapters[1].identity)
	time.Sleep(200 * time.Millisecond)
	row, err := env.srv.Store().GetAssistantSession(ctx, env.member.ID)
	if err != nil || row.ExecIdentity != string(identity) {
		t.Fatalf("row after the old session's cleanup = %+v, %v, want the new adapter's identity %s", row, err, identity)
	}

	if _, err := adapters[1].Stop(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if perr := env.call(protocol.MethodAssistantReset, struct{}{}, nil); perr != nil {
		t.Fatalf("assistant.reset after a crash: %v", perr)
	}
	time.Sleep(200 * time.Millisecond)
	if row, err = env.srv.Store().GetAssistantSession(ctx, env.member.ID); err != nil || row.HarnessSessionID != "" || row.Agent != "" || row.Generation != 1 {
		t.Fatalf("row after a crash and a reset = %+v, %v, want a cleared conversation", row, err)
	}
}

// TestIntegrationAssistantCancelCards cancels a turn while a
// tool call waits for approval: the card goes and the call fails unapproved.
func TestIntegrationAssistantCancelCards(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatal(perr)
	}
	frames := env.watch(ctx, 0)
	if perr := env.call(protocol.MethodAssistantPrompt, protocol.AssistantPromptParams{Text: acpmock.PromptWait, IdempotencyKey: "w"}, nil); perr != nil {
		t.Fatal(perr)
	}
	conn, err := net.Dial("unix", filepath.Join(env.data, "assistant", string(env.member.ID), "sock", "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	result := make(chan *protocol.Error, 1)
	go func() {
		perr, _ := env.toolCall(conn, bufio.NewReader(conn), protocol.MethodEnvSave)
		result <- perr
	}()
	frames.until(t, "a pending confirmation", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && len(f.Status.Confirmations) == 1
	})
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr != nil {
		t.Fatal(perr)
	}
	frames.until(t, "the card to go", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && len(f.Status.Confirmations) == 0
	})
	select {
	case perr := <-result:
		if perr == nil || !strings.Contains(perr.Message, "was not approved") {
			t.Fatalf("env.save after a cancel = %v, want it refused", perr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the tool call stayed blocked after the cancel")
	}
}

// TestIntegrationAssistantKeyKept drops the caller
// while the agent has the prompt: the retry must not run it twice.
func TestIntegrationAssistantKeyKept(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	callerCtx, leave := context.WithCancel(ctx)
	time.AfterFunc(300*time.Millisecond, leave)
	err := env.srv.assistant.Prompt(callerCtx, env.member.ID, acpmock.PromptWait, "gone")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prompt of a caller that left = %v, want the cancellation", err)
	}
	if err := env.srv.assistant.Prompt(ctx, env.member.ID, acpmock.PromptWait, "gone"); err != nil {
		t.Fatalf("retry of a dispatched prompt = %v, want it ignored", err)
	}
	var history protocol.RunACPHistoryResult
	if perr := env.call(protocol.MethodAssistantHistory, protocol.AssistantHistoryParams{}, &history); perr != nil {
		t.Fatal(perr)
	}
	turns := 0
	for _, f := range history.Frames {
		if strings.Contains(string(f.Item), `"turn_start"`) {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("%d turns after the retry, want 1", turns)
	}
	if perr := env.call(protocol.MethodAssistantCancel, struct{}{}, nil); perr != nil {
		t.Fatal(perr)
	}
}
