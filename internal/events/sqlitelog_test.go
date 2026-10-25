package events

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

var _ EventLog = (*SQLiteLog)(nil)

func newTestLog(t *testing.T) *SQLiteLog {
	t.Helper()
	log, err := OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open sqlite log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendN(t *testing.T, log *SQLiteLog, events ...Event) {
	t.Helper()
	for _, e := range events {
		if err := log.Append(context.Background(), e); err != nil {
			t.Fatalf("append seq %d: %v", e.Seq, err)
		}
	}
}

func logEvent(seq uint64, workspace domain.WorkspaceID, run domain.RunID, p Payload) Event {
	return Event{
		ID:          newEventID(),
		Seq:         seq,
		Time:        time.Unix(0, int64(seq)*1e6).UTC(),
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     "m1",
		Type:        p.EventType(),
		Payload:     p,
	}
}

func TestSQLiteLogAppendRead(t *testing.T) {
	log := newTestLog(t)
	want := logEvent(1, "w1", "r1", RunStatusPayload{From: domain.RunQueued, To: domain.RunProvisioning})
	appendN(t, log, want)

	got, err := log.Read(context.Background(), Filter{}, 0, 0, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0] != want {
		t.Errorf("got %#v, want %#v", got[0], want)
	}
}

func TestSQLiteLogFiltersAndBounds(t *testing.T) {
	log := newTestLog(t)
	appendN(t, log,
		logEvent(1, "w1", "r1", RunStatusPayload{To: domain.RunRunning}),
		logEvent(2, "w1", "r2", RunDiffPayload{Files: []FileDiffStat{{Path: "a.go", Additions: 1}}}),
		logEvent(3, "w2", "r3", RunStatusPayload{To: domain.RunFailed}),
		logEvent(4, "w1", "r1", TimelinePayload{Kind: TimelineKill}),
		logEvent(5, "w1", "", PresencePayload{State: PresenceOnline}),
	)
	ctx := context.Background()

	seqs := func(f Filter, after, upto uint64, limit int) []uint64 {
		t.Helper()
		got, err := log.Read(ctx, f, after, upto, limit)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		out := make([]uint64, len(got))
		for i, e := range got {
			out[i] = e.Seq
		}
		return out
	}
	eq := func(name string, got, want []uint64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}

	eq("workspace filter", seqs(Filter{Workspace: "w1"}, 0, 0, 10), []uint64{1, 2, 4, 5})
	eq("run filter", seqs(Filter{Run: "r1"}, 0, 0, 10), []uint64{1, 4})
	eq("type filter", seqs(Filter{Types: []Type{TypeRunStatus, TypeTimeline}}, 0, 0, 10), []uint64{1, 3, 4})
	eq("after cursor", seqs(Filter{Workspace: "w1"}, 2, 0, 10), []uint64{4, 5})
	eq("upper bound", seqs(Filter{}, 1, 4, 10), []uint64{2, 3, 4})
	eq("limit", seqs(Filter{}, 0, 0, 2), []uint64{1, 2})
	eq("combined", seqs(Filter{Workspace: "w1", Run: "r1", Types: []Type{TypeTimeline}}, 0, 0, 10), []uint64{4})
}

func TestSQLiteLogLastSeq(t *testing.T) {
	log := newTestLog(t)
	ctx := context.Background()

	last, err := log.LastSeq(ctx)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != 0 {
		t.Fatalf("empty log last seq = %d, want 0", last)
	}

	appendN(t, log,
		logEvent(3, "w1", "", PresencePayload{State: PresenceOnline}),
		logEvent(7, "w1", "", PresencePayload{State: PresenceOffline}),
	)
	last, err = log.LastSeq(ctx)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != 7 {
		t.Fatalf("last seq = %d, want 7", last)
	}

	if _, err = log.db.ExecContext(ctx, `DELETE FROM events WHERE seq = 7`); err != nil {
		t.Fatalf("delete the newest event: %v", err)
	}
	last, err = log.LastSeq(ctx)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != 7 {
		t.Fatalf("last seq after deleting the newest event = %d, want 7", last)
	}
}

func TestSQLiteLogPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendN(t, log, logEvent(1, "w1", "r1", RunCostPayload{InputTokens: 5, OutputTokens: 6, CostUSD: 0.01, Metered: true}))
	if closeErr := log.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	reopened, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Read(context.Background(), Filter{}, 0, 0, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Payload.(RunCostPayload).InputTokens != 5 {
		t.Fatalf("got %#v, want the appended cost event", got)
	}
}

// TestSQLiteLogPathWithURISpecialChars proves the file: DSN survives paths
// containing '?', '#', and '%': without escaping, '?' silently truncates
// the path (creating the database at the wrong file) and '%' fails to open.
func TestSQLiteLogPathWithURISpecialChars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture directory name embeds '?', which NTFS forbids in a filename")
	}
	dir := filepath.Join(t.TempDir(), "cache?v=2#a", "%41dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "events.db")

	log, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("open with special chars in path: %v", err)
	}
	appendN(t, log, logEvent(1, "w1", "r1", RunStatusPayload{To: domain.RunRunning}))
	if closeErr := log.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("database not created at the configured path: %v", statErr)
	}

	reopened, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Read(context.Background(), Filter{}, 0, 0, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events after reopen, want 1", len(got))
	}
}

func TestSQLiteLogAppendDuplicateSeqFails(t *testing.T) {
	log := newTestLog(t)
	e := logEvent(1, "w1", "", PresencePayload{State: PresenceOnline})
	appendN(t, log, e)
	if err := log.Append(context.Background(), e); err == nil {
		t.Fatal("duplicate seq append succeeded, want error")
	}
}

func TestSQLiteLogAppendIDempotencyDistinguishesCollision(t *testing.T) {
	log := newTestLog(t)
	ctx := context.Background()
	e := logEvent(1, "w1", "r1", PresencePayload{State: PresenceOnline})
	appendN(t, log, e)
	if err := log.Append(ctx, e); !errors.Is(err, ErrEventAlreadyExists) {
		t.Fatalf("same event append = %v, want ErrEventAlreadyExists", err)
	}
	conflict := e
	conflict.WorkspaceID = "w2"
	if err := log.Append(ctx, conflict); !errors.Is(err, ErrEventIDConflict) {
		t.Fatalf("conflicting event ID append = %v, want ErrEventIDConflict", err)
	}
	got, err := log.Get(ctx, e.ID)
	if err != nil || got.Seq != e.Seq {
		t.Fatalf("lookup after idempotent append = %+v (err %v), want original", got, err)
	}
}

func TestSQLiteLogGetPreservesQueryError(t *testing.T) {
	log := newTestLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := log.Get(ctx, "event-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("get after cancellation = %v, want context.Canceled", err)
	}
}

func TestSQLiteLogReadsOneRunThroughIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	// The store's migrations create the events indexes, as on a server.
	db, err := storetest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	appendN(t, log,
		logEvent(1, "w1", "r1", RunStatusPayload{To: domain.RunRunning}),
		logEvent(2, "w1", "r2", CoordMessagePayload{FromRunID: "r2", ToRunID: "r1"}),
		logEvent(3, "w1", "r1", CoordMessagePayload{FromRunID: "r1", ToRunID: "r1"}),
		logEvent(4, "w1", "r2", CoordMessagePayload{FromRunID: "r2", ToRunID: "r3"}),
		logEvent(5, "w1", "r2", CoordMessageAckedPayload{ToRunID: "r1"}),
		logEvent(6, "w2", "r9", CoordMessagePayload{FromRunID: "r9", ToRunID: "r1"}),
		logEvent(7, "w1", "r1", TimelinePayload{Kind: TimelineKill}),
	)
	ctx := context.Background()
	f := Filter{Workspace: "w1", Runs: []domain.RunID{"r1"}}
	seqs := func(got []Event, err error) []uint64 {
		t.Helper()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		out := make([]uint64, len(got))
		for i, e := range got {
			out[i] = e.Seq
		}
		return out
	}
	for name, tc := range map[string]struct {
		got, want []uint64
	}{
		"oldest first": {seqs(log.Read(ctx, f, 0, 0, 10)), []uint64{1, 2, 3, 7}},
		"after cursor": {seqs(log.Read(ctx, f, 1, 0, 2)), []uint64{2, 3}},
		"newest first": {seqs(log.ReadBefore(ctx, f, 8, 3)), []uint64{7, 3, 2}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s: seqs = %v, want %v", name, tc.got, tc.want)
		}
	}

	query, args := readQuery(f, 0, 7, 10, "DESC")
	rows, err := log.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
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
	plan := strings.Join(steps, "; ")
	for _, step := range []string{
		"SEARCH events USING INDEX idx_events_run (run_id=? AND rowid>? AND rowid<?)",
		"SEARCH events USING INDEX idx_events_coord_to_run (<expr>=? AND rowid>? AND rowid<?)",
	} {
		if !strings.Contains(plan, step) {
			t.Errorf("plan %q lacks %q", plan, step)
		}
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("plan %q sorts its rows", plan)
	}
}
