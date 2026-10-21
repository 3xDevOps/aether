package store

import (
	"strings"
	"testing"
)

func TestHotReadsUseTheirIndexes(t *testing.T) {
	db := openTestDB(t)
	plan := func(query string) string {
		t.Helper()
		rows, err := db.db.Query(`EXPLAIN QUERY PLAN `+query, make([]any, strings.Count(query, "?"))...)
		if err != nil {
			t.Fatal(err)
		}
		var steps []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(steps, "; ")
	}
	for query, want := range map[string][]string{
		runSnapshotQuery(`runs.id = ?`): {
			"SEARCH question USING INDEX idx_room_messages_run_kind (run_id=? AND kind=?)",
			"SEARCH reply USING COVERING INDEX idx_room_messages_run_kind (run_id=? AND kind=? AND correlation_id=?)",
		},
		dueSteerRequestsQuery + ` AND (created_at, rowid) < (?, ?) ORDER BY created_at DESC, rowid DESC LIMIT ?`: {
			"SEARCH room_messages USING INDEX idx_room_messages_queued_steer (workspace_id=? AND created_at<?)",
		},
	} {
		got := plan(query)
		for _, step := range want {
			if !strings.Contains(got, step) {
				t.Errorf("plan %q lacks %q", got, step)
			}
		}
		if strings.Contains(got, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("plan %q sorts its rows", got)
		}
	}
}
