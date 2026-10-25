package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestSlackLinksAndMessages(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ada, bob := mustCreateMember(t, db), mustCreateTeammate(t, db)
	ws := mustCreateWorkspace(t, db)
	run := mustCreateRun(t, db, ws.ID, ada.ID, domain.RunRunning)
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	link := &SlackLink{Member: ada.ID, TeamID: "T1", UserID: "U1", Channel: "D1", CreatedAt: at}
	if err := db.PutSlackLink(ctx, link); err != nil {
		t.Fatalf("PutSlackLink: %v", err)
	}
	if got, err := db.SlackLinkByUser(ctx, "T1", "U1"); err != nil || *got != *link {
		t.Fatalf("SlackLinkByUser = %+v, %v; want %+v", got, err, link)
	}
	// The same Slack account linked by another member moves to them.
	moved := &SlackLink{Member: bob.ID, TeamID: "T1", UserID: "U1", Channel: "D1", CreatedAt: at.Add(time.Hour)}
	if err := db.PutSlackLink(ctx, moved); err != nil {
		t.Fatalf("PutSlackLink for Bob: %v", err)
	}
	if _, err := db.GetSlackLink(ctx, ada.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Ada's link after Bob linked her Slack account: %v, want ErrNotFound", err)
	}
	if got, err := db.DeleteSlackLink(ctx, bob.ID); err != nil || *got != *moved {
		t.Fatalf("DeleteSlackLink = %+v, %v; want %+v", got, err, moved)
	}
	if _, err := db.DeleteSlackLink(ctx, bob.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSlackLink again: %v, want ErrNotFound", err)
	}

	msg := &SlackMessage{RunID: run.ID, Channel: "D1", TS: "1.1", NeedKey: "k1"}
	if err := db.PutSlackMessage(ctx, msg); err != nil {
		t.Fatalf("PutSlackMessage: %v", err)
	}
	msg.NeedKey = ""
	if err := db.PutSlackMessage(ctx, msg); err != nil {
		t.Fatalf("PutSlackMessage again: %v", err)
	}
	if got, err := db.SlackMessageAt(ctx, "D1", "1.1"); err != nil || *got != *msg {
		t.Fatalf("SlackMessageAt = %+v, %v; want %+v", got, err, msg)
	}
	if err := db.DeleteSlackMessage(ctx, run.ID); err != nil {
		t.Fatalf("DeleteSlackMessage: %v", err)
	}
	if _, err := db.GetSlackMessage(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message after DeleteSlackMessage: %v, want ErrNotFound", err)
	}
	if err := db.PutSlackMessage(ctx, msg); err != nil {
		t.Fatalf("PutSlackMessage after delete: %v", err)
	}
	if err := db.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := db.GetSlackMessage(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message of a deleted run: %v, want ErrNotFound", err)
	}
}
