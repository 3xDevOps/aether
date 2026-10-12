package store

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunTitleMigrationAndScan(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunQueued)

	if got, err := db.db.QueryContext(context.Background(), `SELECT title FROM runs WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("query title column: %v", err)
	} else {
		defer func() { _ = got.Close() }()
		if !got.Next() {
			t.Fatal("title row missing")
		}
		var title string
		if err := got.Scan(&title); err != nil {
			t.Fatalf("scan title column: %v", err)
		}
		if title != "" {
			t.Fatalf("new run title = %q, want empty", title)
		}
	}

	if err := db.SetRunTitle(context.Background(), run.ID, "Terminal title"); err != nil {
		t.Fatalf("set title: %v", err)
	}
	got, err := db.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Title != "Terminal title" {
		t.Fatalf("scanned title = %q, want %q", got.Title, "Terminal title")
	}
	if err := db.SetRunTitle(context.Background(), "missing", "orphaned"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set title missing run = %v, want ErrNotFound", err)
	}
}

func TestSetRunAgentSession(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)

	if err := db.SetRunAgentSession(context.Background(), run.ID, "session-1"); err != nil {
		t.Fatalf("set agent session: %v", err)
	}
	got, err := db.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.HarnessSessionID != "session-1" || got.Status != domain.RunRunning {
		t.Fatalf("run after set = session %q status %s, want session-1 running", got.HarnessSessionID, got.Status)
	}
	if err := db.SetRunAgentSession(context.Background(), "missing", "session-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set agent session on missing run = %v, want ErrNotFound", err)
	}
}

func TestRunModelRoundTripsAndCanBeChanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Model: "opus", Status: domain.RunQueued}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if got, err := db.GetRun(ctx, run.ID); err != nil || got.Model != "opus" {
		t.Fatalf("created run model = %+v, %v, want opus", got, err)
	}
	if err := db.SetRunModel(ctx, run.ID, "sonnet"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if got, err := db.GetRun(ctx, run.ID); err != nil || got.Model != "sonnet" {
		t.Fatalf("changed run model = %+v, %v, want sonnet", got, err)
	}
	if err := db.SetRunModel(ctx, run.ID, "x y"); err == nil {
		t.Fatal("set model with whitespace succeeded")
	}
}
