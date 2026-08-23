package events

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
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

func logEvent(seq uint64, session domain.SessionID, run domain.RunID, p Payload) Event {
	return Event{
		ID:        newEventID(),
		Seq:       seq,
		Time:      time.Unix(0, int64(seq)*1e6).UTC(),
		SessionID: session,
		RunID:     run,
		ActorID:   "m1",
		Type:      p.EventType(),
		Payload:   p,
	}
}

func TestSQLiteLogAppendRead(t *testing.T) {
	log := newTestLog(t)
	want := logEvent(1, "s1", "r1", RunStatusPayload{From: domain.RunQueued, To: domain.RunProvisioning})
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
		logEvent(1, "s1", "r1", RunStatusPayload{To: domain.RunRunning}),
		logEvent(2, "s1", "r2", RunDiffPayload{Files: []FileDiffStat{{Path: "a.go", Additions: 1}}}),
		logEvent(3, "s2", "r3", RunStatusPayload{To: domain.RunFailed}),
		logEvent(4, "s1", "r1", TimelinePayload{Kind: TimelineKill}),
		logEvent(5, "s1", "", PresencePayload{State: PresenceOnline}),
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

	eq("session filter", seqs(Filter{Session: "s1"}, 0, 0, 10), []uint64{1, 2, 4, 5})
	eq("run filter", seqs(Filter{Run: "r1"}, 0, 0, 10), []uint64{1, 4})
	eq("type filter", seqs(Filter{Types: []Type{TypeRunStatus, TypeTimeline}}, 0, 0, 10), []uint64{1, 3, 4})
	eq("after cursor", seqs(Filter{Session: "s1"}, 2, 0, 10), []uint64{4, 5})
	eq("upper bound", seqs(Filter{}, 1, 4, 10), []uint64{2, 3, 4})
	eq("limit", seqs(Filter{}, 0, 0, 2), []uint64{1, 2})
	eq("combined", seqs(Filter{Session: "s1", Run: "r1", Types: []Type{TypeTimeline}}, 0, 0, 10), []uint64{4})
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
		logEvent(3, "s1", "", PresencePayload{State: PresenceOnline}),
		logEvent(7, "s1", "", PresencePayload{State: PresenceOffline}),
	)
	last, err = log.LastSeq(ctx)
	if err != nil {
		t.Fatalf("last seq: %v", err)
	}
	if last != 7 {
		t.Fatalf("last seq = %d, want 7", last)
	}
}

func TestSQLiteLogPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendN(t, log, logEvent(1, "s1", "r1", RunCostPayload{InputTokens: 5, OutputTokens: 6, CostUSD: 0.01, Metered: true}))
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
	appendN(t, log, logEvent(1, "s1", "r1", RunStatusPayload{To: domain.RunRunning}))
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
	e := logEvent(1, "s1", "", PresencePayload{State: PresenceOnline})
	appendN(t, log, e)
	if err := log.Append(context.Background(), e); err == nil {
		t.Fatal("duplicate seq append succeeded, want error")
	}
}
