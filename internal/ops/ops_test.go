package ops

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Every op has a unique name, a title, a description and the methods it
// calls, is served somewhere, and is read-only on both surfaces or on
// neither; a read op neither destroys nor reaches the outside world.
func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Ops {
		if seen[o.Name] {
			t.Errorf("%s is registered twice", o.Name)
		}
		seen[o.Name] = true
		if o.Title == "" || o.Does == "" || len(o.Methods) == 0 {
			t.Errorf("%s: title %q, description %q, methods %v", o.Name, o.Title, o.Does, o.Methods)
		}
		if o.Scope == "" && o.Tier == TierNone {
			t.Errorf("%s is served by no surface", o.Name)
		}
		if o.Scope != "" && o.Tier != TierNone && (o.Scope == ScopeRead) != (o.Tier == TierRead) {
			t.Errorf("%s: scope %s and tier %d disagree on read-only", o.Name, o.Scope, o.Tier)
		}
		if o.ReadOnly() && (o.Destructive || o.OpenWorld) {
			t.Errorf("%s is read-only but destructive or open-world", o.Name)
		}
	}
}

func assistantSession(t *testing.T, call Call) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "aether", Version: "v0"}, nil)
	RegisterAssistant(srv, call)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// A keyless message retried within the window carries the first one's key,
// so the server delivers it once; another text or run gets its own key.
func TestMessageRetryWithoutKey(t *testing.T) {
	for _, surface := range []string{"connector", "assistant"} {
		t.Run(surface, func(t *testing.T) {
			f := newFake(t)
			f.answers[protocol.MethodServerInfo] = returns(protocol.ServerInfoResult{Member: protocol.Member{ID: "mem-" + surface}})
			f.answers[protocol.MethodRunInject] = returns(protocol.RunRoomPostResult{Message: protocol.RoomMessage{State: protocol.RoomMessageSent}})
			cs := assistantSession(t, f.call)
			if surface == "connector" {
				cs = connectorSession(t, f, ScopeWrite)
			}
			keys := func() []string {
				var out []string
				for _, c := range called(f, protocol.MethodRunInject) {
					var p protocol.RunInjectParams
					if err := json.Unmarshal([]byte(c.params), &p); err != nil {
						t.Fatal(err)
					}
					out = append(out, p.IdempotencyKey)
				}
				return out
			}
			msg := map[string]any{"run_id": "run-1", "message": "rebase on main"}
			_, first := run(t, cs, "aether_message_run", msg)
			text, again := run(t, cs, "aether_message_run", msg)
			if k := keys(); len(k) != 2 || k[0] == "" || k[0] != k[1] || first["idempotency_key"] != k[0] || again["idempotency_key"] != k[0] {
				t.Fatalf("a retried keyless message used keys %v, results %v %v; want one key", k, first, again)
			}
			if first["duplicate"] != nil || again["duplicate"] != true || !strings.Contains(text, "new idempotency_key") {
				t.Fatalf("a repeated keyless message = %q %v after %v; want it marked a duplicate", text, again, first)
			}
			run(t, cs, "aether_message_run", map[string]any{"run_id": "run-1", "message": "and run the tests"})
			run(t, cs, "aether_message_run", map[string]any{"run_id": "run-2", "message": "rebase on main"})
			run(t, cs, "aether_message_run", map[string]any{"run_id": "run-1", "message": "rebase on main", "idempotency_key": "m1"})
			if k := keys(); len(k) != 5 || slices.Contains(k[2:], k[0]) || k[2] == k[3] || k[4] != "m1" {
				t.Fatalf("keys = %v, want a new one per text and run, and the caller's", k)
			}
		})
	}
}

// The assistant serves the shared ops by tier with its own defaults and
// descriptions, and a failure keeps the server's message and code.
func TestAssistantSurface(t *testing.T) {
	f := newFake(t)
	cs := assistantSession(t, f.call)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		var o Op
		if i := slices.IndexFunc(Ops, func(o Op) bool { return o.Name == tool.Name }); i >= 0 {
			o = Ops[i]
		}
		if o.Tier == TierNone || !strings.HasSuffix(strings.TrimSuffix(tool.Description, " Fails if the member declines or does not answer."), o.Does) ||
			tool.Annotations.ReadOnlyHint != (o.Tier == TierRead) {
			t.Errorf("%s: tier %d, description %q, annotations %+v", tool.Name, o.Tier, tool.Description, tool.Annotations)
		}
	}
	for _, name := range []string{"search", "fetch", "aether_launch_swarm", "aether_close_run", "aether_cancel_swarm", "aether_mark_reviewed"} {
		if slices.Contains(names, name) {
			t.Errorf("the assistant offers %s", name)
		}
	}

	run(t, cs, "aether_list_runs", nil)
	run(t, cs, "aether_list_runs", map[string]any{"mine": true})
	lists := called(f, protocol.MethodRunList)
	run(t, cs, "aether_list_runs", map[string]any{"mine": nil})
	if len(lists) != 2 || strings.Contains(lists[0].params, `"mine"`) || !strings.Contains(lists[1].params, `"mine":true`) {
		t.Fatalf("run.list = %+v, want everyone's runs by default and the member's with mine", lists)
	}

	f.answers[protocol.MethodWorkspaceGet] = returns(protocol.WorkspaceGetResult{Workspace: protocol.Workspace{ID: "ws-1", Name: "api", SteerOthers: "admins_only"}})
	if _, out := run(t, cs, "aether_get_workspace", map[string]any{"workspace_id": "ws-1"}); strings.Contains(jsonOf(t, out["workspace"]), "steer_others") ||
		jsonOf(t, out["untrusted"]) != `{"steer_others":"admins_only"}` {
		t.Fatalf("aether_get_workspace = %v, want steer_others under untrusted", out)
	}

	f.errs[protocol.MethodRunGet] = &protocol.Error{Code: protocol.CodeNotFound, Message: "run run-x not found"}
	call, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_get_run", Arguments: map[string]any{"run_id": "run-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := call.Content[0].(*mcp.TextContent).Text; !call.IsError || text != "run run-x not found; call aether_list_runs for run ids" ||
		call.Meta[MetaErrorCode] != float64(protocol.CodeNotFound) {
		t.Fatalf("an unknown run = %q %v", text, call.Meta)
	}
}

// A keyless message still in flight past the window, such as one awaiting
// the member's approval, keeps its key; a finished one older than the
// window gives way to a new key.
func TestMessageKeyKeepsInFlight(t *testing.T) {
	p := protocol.RunInjectParams{RunID: "run-1", Message: "wait for approval"}
	start := time.Now()
	key, dup, done := messageKey("mem-inflight", p, start)
	if dup {
		t.Fatal("a first message is a duplicate")
	}
	other, _, otherDone := messageKey("mem-inflight", protocol.RunInjectParams{RunID: "run-1", Message: "another"}, start)
	otherDone(true)
	later := start.Add(2 * duplicateWindow)
	again, dup, againDone := messageKey("mem-inflight", p, later)
	if again != key || !dup {
		t.Fatalf("a repeat while the first is in flight got key %q (dup %t), want %q", again, dup, key)
	}
	done(true)
	againDone(true)
	if k, _, d := messageKey("mem-inflight", protocol.RunInjectParams{RunID: "run-1", Message: "another"}, later); k == other {
		t.Fatal("a finished message older than the window kept its key")
	} else {
		d(false)
	}
	if k, dup, d := messageKey("mem-inflight", p, time.Now().Add(2*duplicateWindow)); k == key || dup {
		t.Fatalf("a message repeated after the window got key %q (dup %t), want a new one", k, dup)
	} else {
		d(false)
	}
}

// A keyed answer retried after its question stopped waiting repeats the
// key on the one call that answers that question, so the server returns
// the saved answer; an id that is neither a swarm question nor a teammate
// question on the run, nor one the key replied to, writes nothing.
func TestKeyedAnswerRetry(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodMissionShow] = func(raw json.RawMessage) (any, error) {
		var p protocol.MissionShowParams
		if err := json.Unmarshal(raw, &p); err != nil || p.QuestionID != "q-1" {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "mission question not found"}
		}
		return protocol.MissionShowResult{Mission: protocol.Mission{ID: "mis-1"}}, nil
	}
	f.answers[protocol.MethodRunRoomList] = returns(protocol.RoomMessageListResult{Messages: []protocol.RoomMessage{
		{ID: "room-1", RunID: "run-1", Kind: protocol.RoomMessageQuestion, Body: "which db?"},
		{ID: "room-9", RunID: "run-1", Kind: protocol.RoomMessageReply, Body: "main", CorrelationID: "room-old", IdempotencyKey: "a2"},
	}})
	f.answers[protocol.MethodMissionQuestionAnswer] = returns(protocol.MissionQuestionResult{})
	f.answers[protocol.MethodRunRoomPost] = returns(protocol.RunRoomPostResult{})
	cs := connectorSession(t, f, ScopeWrite)
	answer := func(id, key string) (string, bool) {
		f.calls = nil
		text, _, failed := try(t, cs, "aether_answer", map[string]any{"run_id": "run-1", "answer_id": id, "answer": "main", "idempotency_key": key})
		return text, failed
	}

	if _, failed := answer("q-1", "a1"); failed {
		t.Fatal("a keyed swarm answer retry failed")
	}
	if got := f.acted(); got.method != protocol.MethodMissionQuestionAnswer || got.params != `{"question_id":"q-1","answer":"main","idempotency_key":"a1"}` {
		t.Fatalf("a keyed swarm answer retry called %+v", got)
	}

	for id, key := range map[string]string{"room-1": "a1", "room-old": "a2"} {
		if _, failed := answer(id, key); failed {
			t.Fatalf("a keyed teammate reply retry to %s failed", id)
		}
		want := `{"workspace_id":"ws-1","run_id":"run-1","kind":"reply","body":"main","correlation_id":"` + id + `","idempotency_key":"` + key + `"}`
		if got := f.acted(); got.method != protocol.MethodRunRoomPost || got.params != want {
			t.Fatalf("a keyed teammate reply retry to %s called %+v", id, got)
		}
	}

	if text, failed := answer("room-stale", "a3"); !failed || !strings.Contains(text, "nothing pending") {
		t.Fatalf("a keyed answer to an unknown id = %q", text)
	}
	for _, c := range f.calls {
		if c.scope != ScopeRead {
			t.Fatalf("a keyed answer to an unknown id wrote %+v", c)
		}
	}

	if text, _, failed := try(t, cs, "aether_answer", map[string]any{"run_id": "run-1", "answer_id": "q-1", "answer": "main"}); !failed || !strings.Contains(text, "nothing pending") {
		t.Fatalf("a keyless answer to nothing pending = %q", text)
	}
}

// A keyless launch whose call failed without saying whether the run
// started keeps its key, so a keyless retry replays it instead of
// launching again; a refused launch does not.
func TestKeylessLaunchKeepsKeyAfterUncertainFailure(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodServerInfo] = returns(protocol.ServerInfoResult{Member: protocol.Member{ID: "mem-uncertain"}})
	f.answers[protocol.MethodRunLaunch] = returns(protocol.RunLaunchResult{Run: protocol.Run{ID: "run-9", Status: "starting"}})
	cs := assistantSession(t, f.call)
	keys := func() []string {
		var out []string
		for _, c := range called(f, protocol.MethodRunLaunch) {
			var p protocol.RunLaunchParams
			if err := json.Unmarshal([]byte(c.params), &p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p.IdempotencyKey)
		}
		return out
	}
	args := map[string]any{"workspace_id": "ws-1", "harness": "claude", "task": "add a health check"}
	f.errs[protocol.MethodRunLaunch] = &protocol.Error{Code: protocol.CodeUnavailable, Message: "connection closed"}
	if text, _, failed := try(t, cs, "aether_launch_run", args); !failed || !strings.Contains(text, "retry with idempotency_key") {
		t.Fatalf("an uncertain launch failure = %q", text)
	}
	delete(f.errs, protocol.MethodRunLaunch)
	if _, out := run(t, cs, "aether_launch_run", args); out["run_id"] != "run-9" {
		t.Fatalf("the retry = %v", out)
	}
	if k := keys(); len(k) != 2 || k[0] == "" || k[0] != k[1] {
		t.Fatalf("a keyless retry after an uncertain failure used keys %v, want the first one", k)
	}

	args["task"] = "add a readiness check"
	f.errs[protocol.MethodRunLaunch] = &protocol.Error{Code: protocol.CodeDenied, Message: "declined"}
	try(t, cs, "aether_launch_run", args)
	delete(f.errs, protocol.MethodRunLaunch)
	run(t, cs, "aether_launch_run", args)
	if k := keys(); len(k) != 4 || k[2] == k[3] {
		t.Fatalf("a retry after a refused launch used keys %v, want a new one", k)
	}
}
