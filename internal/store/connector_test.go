package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A connector authorized before consent-time scopes keeps every scope it
// could use then.
func TestConnectorScopesMigrationFromV77(t *testing.T) {
	t.Parallel()
	const previous = 77
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)&_pragma=synchronous(OFF)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v <= previous; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO connectors (id, member_id, client_id, client_name, redirect_host, resource, created_at,
		                        access_hash, access_expires_at, refresh_hash, refresh_expires_at)
			VALUES ('con-1', 'm1', 'https://chatgpt.com/c.json', 'ChatGPT', 'chatgpt.com', 'https://edge/mcp/x', 1, 'a', 2, 'r', 3);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (connector scopes migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	c, err := db.GetConnector(context.Background(), "con-1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Scopes, []string{"read", "write", "control"}) {
		t.Fatalf("migrated connector scopes = %v, want read write control", c.Scopes)
	}
}

// A run launched and a room message posted through a connector keep the
// client they came via.
func TestViaRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunRunning, Via: "ChatGPT"}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	gotRun, err := db.GetRun(ctx, run.ID)
	if err != nil || gotRun.Via != "ChatGPT" {
		t.Fatalf("run via = %+v, %v; want ChatGPT", gotRun, err)
	}
	msg := &RoomMessage{WorkspaceID: workspace.ID, RunID: run.ID, ActorID: member.ID, Kind: RoomMessageSteerRequest,
		Body: "hi", IdempotencyKey: "k", Via: "ChatGPT"}
	if err = db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	gotMsg, err := db.GetRoomMessage(ctx, msg.ID)
	if err != nil || gotMsg.Via != "ChatGPT" {
		t.Fatalf("room message via = %+v, %v; want ChatGPT", gotMsg, err)
	}
}
