package ops

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

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
			_, again := run(t, cs, "aether_message_run", msg)
			if k := keys(); len(k) != 2 || k[0] == "" || k[0] != k[1] || first["idempotency_key"] != k[0] || again["idempotency_key"] != k[0] {
				t.Fatalf("a retried keyless message used keys %v, results %v %v; want one key", k, first, again)
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
		o, _ := Lookup(tool.Name)
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
	if len(lists) != 2 || strings.Contains(lists[0].params, `"mine"`) || !strings.Contains(lists[1].params, `"mine":true`) {
		t.Fatalf("run.list = %+v, want everyone's runs by default and the member's with mine", lists)
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
