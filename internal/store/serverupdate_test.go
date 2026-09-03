package store

import (
	"testing"
	"time"
)

func TestServerUpdateStartsEmpty(t *testing.T) {
	db := openTestDB(t)
	got, err := db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending != nil || got.Last != nil {
		t.Fatalf("fresh database reports %+v, want an empty state", got)
	}
}

func TestPendingServerUpdateReplacesAndClears(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Millisecond)

	first := &PendingServerUpdate{Version: "v0.1.0", RequestedBy: "mem_1", RequestedAt: at}
	if err := db.SetPendingServerUpdate(ctx, first); err != nil {
		t.Fatalf("SetPendingServerUpdate: %v", err)
	}
	// A second request replaces the first: one pending update at a time.
	second := &PendingServerUpdate{Version: "v0.2.0", RequestedBy: "mem_2", RequestedAt: at.Add(time.Minute)}
	if err := db.SetPendingServerUpdate(ctx, second); err != nil {
		t.Fatalf("SetPendingServerUpdate replace: %v", err)
	}
	got, err := db.GetServerUpdate(ctx)
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending == nil || got.Pending.Version != "v0.2.0" || got.Pending.RequestedBy != "mem_2" {
		t.Fatalf("pending = %+v, want v0.2.0 by mem_2", got.Pending)
	}
	if !got.Pending.RequestedAt.Equal(second.RequestedAt) {
		t.Fatalf("requested at = %v, want %v", got.Pending.RequestedAt, second.RequestedAt)
	}

	if cerr := db.SetPendingServerUpdate(ctx, nil); cerr != nil {
		t.Fatalf("clear pending: %v", cerr)
	}
	if got, err = db.GetServerUpdate(ctx); err != nil {
		t.Fatalf("GetServerUpdate after clear: %v", err)
	}
	if got.Pending != nil {
		t.Fatalf("pending = %+v after a cancel, want none", got.Pending)
	}
}

func TestSetLastServerUpdateClearsPending(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.SetPendingServerUpdate(ctx, &PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: "mem_1", RequestedAt: at,
	}); err != nil {
		t.Fatalf("SetPendingServerUpdate: %v", err)
	}
	// Recording an outcome retires the pending row in the same write, so
	// the next idle tick cannot retry an attempt that already finished.
	if err := db.SetLastServerUpdate(ctx, ServerUpdateAttempt{
		Version: "v0.2.0", Outcome: ServerUpdateFailed, Detail: "checksum mismatch", At: at,
	}); err != nil {
		t.Fatalf("SetLastServerUpdate: %v", err)
	}
	got, err := db.GetServerUpdate(ctx)
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending != nil {
		t.Fatalf("pending = %+v, want none after an attempt", got.Pending)
	}
	if got.Last == nil || got.Last.Outcome != ServerUpdateFailed || got.Last.Detail != "checksum mismatch" {
		t.Fatalf("last = %+v, want the failed attempt", got.Last)
	}
	if !got.Last.At.Equal(at) {
		t.Fatalf("last at = %v, want %v", got.Last.At, at)
	}
}

func TestSetPendingServerUpdateRejectsEmptyVersion(t *testing.T) {
	db := openTestDB(t)
	if err := db.SetPendingServerUpdate(t.Context(), &PendingServerUpdate{RequestedBy: "mem_1"}); err == nil {
		t.Fatal("expected an error for a pending update with no version")
	}
}
