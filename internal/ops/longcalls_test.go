package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// With two calls already running long, a third that reaches the bound is
// ended and told why, the two finish, and their places come free.
func TestLongCallsEndTheCallPastTheBound(t *testing.T) {
	long := &LongCalls{After: 50 * time.Millisecond, Max: 2}
	release := make(chan struct{})
	srv := mcp.NewServer(&mcp.Implementation{Name: "aether", Version: "v0"}, nil)
	RegisterConnector(srv, func(ctx context.Context, _ string, _, _ any) error {
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

	type answer struct {
		text   string
		failed bool
	}
	answers := make(chan answer, 3)
	call := func() {
		text, _, failed := try(t, cs, "aether_list_workspaces", nil)
		answers <- answer{text, failed}
	}
	for range 3 {
		go call()
	}
	const want = "ended after 50ms: this server already runs 2 connector calls longer than that, the most it runs at once; call again shortly: context canceled"
	if got := <-answers; !got.failed || got.text != want {
		t.Fatalf("the call past the bound: failed %v %q, want %q", got.failed, got.text, want)
	}
	select {
	case got := <-answers:
		t.Fatalf("a call inside the bound ended early: %q", got.text)
	case <-time.After(4 * long.After):
	}
	close(release)
	for range 2 {
		if got := <-answers; got.failed {
			t.Fatalf("a call inside the bound failed: %s", got.text)
		}
	}
	release = make(chan struct{})
	for range 2 {
		go call()
	}
	select {
	case got := <-answers:
		t.Fatalf("a long call after the others finished ended early: %q", got.text)
	case <-time.After(4 * long.After):
	}
	close(release)
	for range 2 {
		if got := <-answers; got.failed || strings.Contains(got.text, "ended after") {
			t.Fatalf("a long call after the others finished: %q", got.text)
		}
	}
}
