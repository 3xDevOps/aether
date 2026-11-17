package ops

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// With two calls of an op that waits already running long, a third is
// ended at the bound and told to call again. A call of an op that does
// not wait takes the place of the oldest waiting one instead, and is
// ended itself only when no waiting call is left to end.
func TestLongCallsEndWaitingCallsFirst(t *testing.T) {
	long := &LongCalls{After: 50 * time.Millisecond, Max: 2}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := mcp.NewServer(&mcp.Implementation{Name: "aether", Version: "v0"}, nil)
	RegisterConnector(srv, func(ctx context.Context, method string, _, _ any) error {
		if method != protocol.MethodAssistantAsk && method != protocol.MethodWorkspaceList {
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(Scope) bool { return true }, long)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	start := func(name string, args map[string]any) <-chan string {
		ended := make(chan string, 1)
		go func() {
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				ended <- err.Error()
				return
			}
			ended <- res.Content[0].(*mcp.TextContent).Text
		}()
		return ended
	}
	ask := func() <-chan string { return start("aether_ask_assistant", map[string]any{"turn_id": "turn-1"}) }
	list := func() <-chan string { return start("aether_list_workspaces", nil) }
	running := func(what string, calls ...<-chan string) {
		t.Helper()
		for _, c := range calls {
			select {
			case got := <-c:
				t.Fatalf("%s ended: %q", what, got)
			default:
			}
		}
	}
	const early = "ended early: this server runs at most 2 connector calls longer than 50ms at once; call again shortly: context canceled"

	first := ask()
	time.Sleep(long.After / 2)
	second := ask()
	time.Sleep(2 * long.After)
	third := ask()
	if got := <-third; got != early {
		t.Fatalf("a waiting call past the bound: %q, want %q", got, early)
	}
	running("a waiting call inside the bound", first, second)

	lists := []<-chan string{list()}
	if got := <-first; got != early {
		t.Fatalf("the oldest waiting call, when a call that does not wait reached the bound: %q, want %q", got, early)
	}
	running("the newer waiting call, when the oldest was ended", second)
	lists = append(lists, list())
	if got := <-second; got != early {
		t.Fatalf("the other waiting call, when a second call that does not wait reached the bound: %q, want %q", got, early)
	}
	time.Sleep(2 * long.After)
	running("a call that does not wait, in a waiting call's place", lists...)
	if got := <-list(); got != early {
		t.Fatalf("a call that does not wait, with no waiting call to end: %q, want %q", got, early)
	}
	running("a call that does not wait, inside the bound", lists...)
}
