package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestStreamWireSendsAWatchChangeToItsOwnerAlone(t *testing.T) {
	ctx := context.Background()
	bus, err := NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close() //nolint:errcheck // test cleanup
	for _, member := range []domain.MemberID{"mem-ada", "mem-bob"} {
		sub, err := bus.Subscribe(ctx, SubscribeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, owner := range []domain.MemberID{"mem-ada", "mem-bob"} {
			if _, err := bus.Publish(ctx, Event{WorkspaceID: ServerScope, Payload: WatchChangedPayload{WatchID: "wat-" + string(owner), MemberID: owner}}); err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		go func() { time.Sleep(100 * time.Millisecond); _ = sub.Close() }()
		if err := StreamWire(sub, member, func(e protocol.Event) error {
			got = append(got, string(e.Payload))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], `"member_id":"`+string(member)+`"`) {
			t.Fatalf("%s streamed %q, want only their own watch change", member, got)
		}
	}
}
