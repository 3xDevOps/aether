package sshd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/timeline"
)

// workspace.timeline shows a private assistant notice only to its recipient,
// reading forward and newest-first.
func TestWorkspaceTimelineHidesPrivateNotices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEnv(t, func(c *Config) {
		c.Bus = bus
		c.Services.Timeline = timeline.NewReader(log)
	})
	signer, _ := addMember(t, e, "Vic", domain.RoleViewer, false)
	if _, perr := bus.Publish(ctx, events.Event{WorkspaceID: e.ws.ID, RunID: e.run.ID, ActorID: e.member.ID,
		Payload: events.AssistantNoticePayload{MemberID: e.member.ID, RunID: e.run.ID, Kind: events.AssistantNoticeFinished, Title: "private", Body: "x"}}); perr != nil {
		t.Fatal(perr)
	}
	noticeCount := func(c *protocol.Client, newest bool) int {
		t.Helper()
		var res protocol.WorkspaceTimelineResult
		params := protocol.WorkspaceTimelineParams{WorkspaceID: string(e.ws.ID), Newest: newest}
		if err := c.Call(protocol.MethodWorkspaceTimeline, params, &res); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, ev := range res.Events {
			if ev.Type == string(events.TypeAssistantNotice) {
				n++
			}
		}
		return n
	}
	owner, viewer := controlClient(t, e), controlAs(t, e, signer)
	for _, newest := range []bool{false, true} {
		if got := noticeCount(owner, newest); got != 1 {
			t.Fatalf("recipient (newest=%v) saw %d notices, want 1", newest, got)
		}
		if got := noticeCount(viewer, newest); got != 0 {
			t.Fatalf("other member (newest=%v) saw %d notices, want 0", newest, got)
		}
	}
}
