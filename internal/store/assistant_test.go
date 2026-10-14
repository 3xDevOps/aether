package store

import (
	"context"
	"testing"
)

func TestAssistantSessionLifecycle(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ada := mustCreateMember(t, db).ID

	put := func(session, exec string) {
		t.Helper()
		if err := db.PutAssistantSession(ctx, &AssistantSession{MemberID: ada, Agent: "claude", HarnessSessionID: session, ExecIdentity: exec}); err != nil {
			t.Fatal(err)
		}
	}
	get := func() *AssistantSession {
		t.Helper()
		row, err := db.GetAssistantSession(ctx, ada)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	put("s1", "exec-1")
	if err := db.ClearAssistantExec(ctx, ada, 0, "exec-other"); err != nil || get().ExecIdentity != "exec-1" {
		t.Fatalf("clearing another adapter's identity: %v, %+v", err, get())
	}
	if err := db.ClearAssistantExec(ctx, ada, 1, "exec-1"); err != nil || get().ExecIdentity != "exec-1" {
		t.Fatalf("clearing in another generation: %v, %+v", err, get())
	}
	if err := db.ClearAssistantExec(ctx, ada, 0, "exec-1"); err != nil || get().ExecIdentity != "" || get().HarnessSessionID != "s1" {
		t.Fatalf("clearing the recorded adapter: %v, %+v", err, get())
	}

	if err := db.AddAssistantPromptKey(ctx, ada, 0, "k"); err != nil {
		t.Fatal(err)
	}
	generation, err := db.ResetAssistantSession(ctx, ada)
	if err != nil || generation != 1 {
		t.Fatalf("reset = %d, %v, want generation 1", generation, err)
	}
	if row := get(); row.Agent != "" || row.HarnessSessionID != "" || row.Generation != 1 {
		t.Fatalf("row after reset = %+v", row)
	}
	if seen, err := db.HasAssistantPromptKey(ctx, ada, 0, "k"); err != nil || seen {
		t.Fatalf("key survived the reset: %v, %v", seen, err)
	}
	for i := range MaxAssistantPromptKeys + 5 {
		if err := db.AddAssistantPromptKey(ctx, ada, 1, string(rune('a'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_prompt_keys WHERE member_id = ?`, ada).Scan(&n); err != nil || n != MaxAssistantPromptKeys {
		t.Fatalf("%d keys kept, %v, want %d", n, err, MaxAssistantPromptKeys)
	}
}
