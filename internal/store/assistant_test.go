package store

import (
	"context"
	"errors"
	"testing"
	"time"
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

func TestAssistantSettings(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ada := mustCreateMember(t, db).ID

	if got, err := db.AssistantSettings(ctx, ada); err != nil || got != (AssistantSettings{ShareView: true, PreferencesEnabled: true}) {
		t.Fatalf("settings before any were set = %+v, %v, want only view sharing on", got, err)
	}
	want := AssistantSettings{AutoApprove: true, Approvals: 2, Snoozes: 1, SnoozedUntil: time.Unix(1_800_000_000, 0).UTC(), ShareView: true, RuleStreakKey: `{"method":"env.save"}`, RuleStreak: 2}
	if err := db.PutAssistantSettings(ctx, ada, want); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResetAssistantSession(ctx, ada); err != nil {
		t.Fatal(err)
	}
	if got, err := db.AssistantSettings(ctx, ada); err != nil || got != want {
		t.Fatalf("settings after a new conversation = %+v, %v, want %+v", got, err, want)
	}
	want = AssistantSettings{NoSuggest: true}
	if err := db.PutAssistantSettings(ctx, ada, want); err != nil {
		t.Fatal(err)
	}
	if got, err := db.AssistantSettings(ctx, ada); err != nil || got != want {
		t.Fatalf("settings after an update = %+v, %v, want %+v", got, err, want)
	}
	if err := db.PutAssistantSettings(ctx, "mem-missing", want); !errors.Is(err, ErrNotFound) {
		t.Fatalf("setting an unknown member's settings = %v, want ErrNotFound", err)
	}
}

func TestAssistantRules(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ada, bob := mustCreateMember(t, db).ID, mustCreateMember(t, db).ID
	key := AssistantRuleKey{Method: "run.launch", WorkspaceID: "ws-1", Agent: "claude", Mode: "acp", Permission: "default", NoFallback: true}

	r, err := db.AddAssistantRule(ctx, ada, key, "Always approve launches", "Launch claude")
	if err != nil || r.ID == "" || r.Key != key || r.Uses != 0 || !r.LastUsedAt.IsZero() || r.CreatedAt.IsZero() {
		t.Fatalf("added rule = %+v, %v", r, err)
	}
	again, err := db.AddAssistantRule(ctx, ada, key, "other wording", "other card")
	if err != nil || again.ID != r.ID || again.Wording != r.Wording {
		t.Fatalf("adding the same key again = %+v, %v, want the existing rule", again, err)
	}
	other := key
	other.Mode = "tui"
	if _, err = db.AddAssistantRule(ctx, ada, other, "w", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AddAssistantRule(ctx, bob, key, "w", "s"); err != nil {
		t.Fatal(err)
	}
	if err = db.UseAssistantRule(ctx, ada, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.UseAssistantRule(ctx, bob, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member using the rule = %v, want ErrNotFound", err)
	}
	rules, err := db.AssistantRules(ctx, ada)
	if err != nil || len(rules) != 2 || rules[0].ID != r.ID || rules[0].Uses != 1 || rules[0].LastUsedAt.IsZero() {
		t.Fatalf("rules = %+v, %v, want two with the first used once", rules, err)
	}
	if err = db.DeleteAssistantRule(ctx, bob, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member revoking the rule = %v, want ErrNotFound", err)
	}
	if err = db.DeleteAssistantRule(ctx, ada, r.ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ = db.AssistantRules(ctx, ada); len(rules) != 1 || rules[0].Key != other {
		t.Fatalf("rules after the revoke = %+v", rules)
	}
	if _, err = db.AddAssistantRule(ctx, "mem-missing", key, "w", "s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rule for an unknown member = %v, want ErrNotFound", err)
	}
}
