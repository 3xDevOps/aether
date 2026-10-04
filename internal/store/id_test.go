package store

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// IDs are retyped between CLI calls, so they name their kind and differ from
// the first random character rather than sharing a timestamp prefix.
func TestNewIDIsTypedShortAndRandomFirst(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^run-[0-9a-hjkmnp-tv-z]{10}$`)
	seen := make(map[string]bool)
	leads := make(map[byte]bool)
	for range 1000 {
		id, err := newID("run")
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if !shape.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, shape)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		leads[id[len("run-")]] = true
	}
	if len(leads) < 16 {
		t.Fatalf("1000 ids used %d distinct first random characters; want a random lead", len(leads))
	}

	db := openTestDB(t)
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	for kind, id := range map[string]string{"ws": string(w.ID), "mem": string(m.ID), "run": string(r.ID)} {
		if !strings.HasPrefix(id, kind+"-") {
			t.Errorf("created id %q, want kind %q", id, kind)
		}
	}
}

// Inbox delivery follows creation, not ID order: random IDs do not sort by
// time, and databases upgraded in place still hold 26-character legacy IDs.
func TestInboxDeliversInCreationOrderAcrossIDFormats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	base := time.Now().Add(-time.Hour).UnixNano()
	// Each ID sorts after the next one's, so ORDER BY id would reverse them.
	want := []string{"msg-zzzzzzzzzz", "msg-0000000000", "01m42bpe6qgk2trpzpd77a72qj"}
	for i, id := range want {
		if _, err := db.db.ExecContext(ctx,
			`INSERT INTO run_messages (id, workspace_id, from_run, to_run, body, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, w.ID, from.ID, to.ID, "seeded "+id, base+int64(i)); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	latest := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "newest"}
	if err := db.AppendRunMessage(ctx, latest, 100); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	if !strings.HasPrefix(latest.ID, "msg-") {
		t.Fatalf("appended message id %q, want kind msg", latest.ID)
	}
	want = append(want, latest.ID)

	observed, err := db.ListUnackedRunMessageIDs(ctx, to.ID, 10)
	if err != nil {
		t.Fatalf("ListUnackedRunMessageIDs: %v", err)
	}
	if strings.Join(observed, " ") != strings.Join(want, " ") {
		t.Fatalf("unacked ids = %v, want %v", observed, want)
	}

	var got []string
	ack := ""
	for {
		batch, token, err := db.DeliverRunMessages(ctx, to.ID, ack, 2)
		if err != nil {
			t.Fatalf("DeliverRunMessages: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		if !strings.HasPrefix(token, "ack-") {
			t.Fatalf("delivery token %q, want kind ack", token)
		}
		for _, msg := range batch {
			got = append(got, msg.ID)
		}
		ack = token
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("delivered ids = %v, want %v", got, want)
	}
}

// The missions cursor is still a mission ID, but pages follow creation.
func TestMissionPagesFollowCreationNotIDOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	var ids []string
	for _, key := range []string{"page-1", "page-2", "page-3"} {
		ids = append(ids, string(mustCreatePlanningMission(t, db, w.ID, m.ID, key).ID))
	}
	// Make the lowest ID the newest so ORDER BY id DESC would reverse pages.
	slices.Sort(ids)
	base := time.Now().Add(-time.Hour).UnixNano()
	for i, id := range ids {
		if _, err := db.db.ExecContext(ctx, `UPDATE missions SET created_at = ? WHERE id = ?`, base-int64(i), id); err != nil {
			t.Fatalf("age mission %s: %v", id, err)
		}
	}

	var got []string
	cursor := ""
	for range ids {
		page, next, err := db.ListMissionsPage(ctx, w.ID, 1, cursor)
		if err != nil {
			t.Fatalf("ListMissionsPage(%q): %v", cursor, err)
		}
		if len(page) != 1 {
			t.Fatalf("page after %q = %d missions, want 1", cursor, len(page))
		}
		got = append(got, string(page[0].ID))
		cursor = next
	}
	if cursor != "" {
		t.Fatalf("cursor after the last page = %q, want none", cursor)
	}
	if strings.Join(got, " ") != strings.Join(ids, " ") {
		t.Fatalf("pages = %v, want newest first %v", got, ids)
	}
}
