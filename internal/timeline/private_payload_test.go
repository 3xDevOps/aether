package timeline

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
)

// A private assistant notice must reach only its recipient through a stored-log
// page, matching the live and replayed streams.
func TestPageHidesAnotherMembersPrivateNotice(t *testing.T) {
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	const ws domain.WorkspaceID = "ws-1"
	const run domain.RunID = "run-1"
	const owner domain.MemberID = "mem-owner"
	const other domain.MemberID = "mem-other"
	if _, perr := bus.Publish(ctx, events.Event{ID: "notice", WorkspaceID: ws, RunID: run, ActorID: owner,
		Payload: events.AssistantNoticePayload{MemberID: owner, RunID: run, Kind: events.AssistantNoticeFinished, Title: "secret", Body: "private"}}); perr != nil {
		t.Fatalf("publish: %v", perr)
	}

	reader := NewReader(log)
	ids := func(readerID domain.MemberID) []string {
		t.Helper()
		page, perr := reader.Page(ctx, Filter{Workspace: ws, Run: run, Reader: readerID}, 0, 0)
		if perr != nil {
			t.Fatalf("Page: %v", perr)
		}
		var out []string
		for _, e := range page.Events {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(owner); !slices.Equal(got, []string{"notice"}) {
		t.Fatalf("recipient page = %v, want the notice", got)
	}
	if got := ids(other); len(got) != 0 {
		t.Fatalf("non-recipient page = %v, want the private notice hidden", got)
	}
}
