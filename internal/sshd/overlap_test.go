package sshd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestConflictRadarFlagsBothRunsAndClears drives the radar the way a user
// does: two runs owned by different members edit the same file, both rows
// flag it naming the other member, and the flag clears when one finishes.
func TestConflictRadarFlagsBothRunsAndClears(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log, err := events.OpenSQLiteLog(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	var idx *overlap.Index
	e := newTestEnv(t, func(c *Config) {
		c.Bus = bus
		idx = overlap.NewIndex(bus, c.Store, log)
		c.Services.Overlaps = idx
	})
	if err = idx.Start(ctx); err != nil {
		t.Fatalf("index start: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	grace := &domain.Member{
		DisplayName: "Grace",
		PublicKey:   string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())),
		Color:       "#3cb44b",
		Role:        domain.RoleCollaborator,
	}
	if err = e.store.CreateMember(ctx, grace); err != nil {
		t.Fatalf("create member: %v", err)
	}
	other := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: grace.ID, Task: "also do things",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: "aether/run-y-also-do-things",
	}
	if err = e.store.CreateRun(ctx, other); err != nil {
		t.Fatalf("create run: %v", err)
	}

	diff := func(run domain.RunID, paths ...string) {
		t.Helper()
		files := make([]events.FileDiffStat, 0, len(paths))
		for _, p := range paths {
			files = append(files, events.FileDiffStat{Path: p, Additions: 1})
		}
		if _, perr := bus.Publish(ctx, events.Event{
			WorkspaceID: e.ws.ID, RunID: run, Payload: events.RunDiffPayload{Files: files},
		}); perr != nil {
			t.Fatalf("publish diff: %v", perr)
		}
	}
	diff(e.run.ID, "main.go", "solo.go")
	diff(other.ID, "main.go")

	c := controlClient(t, e)
	overlaps := func() map[string]protocol.Overlap {
		t.Helper()
		var res protocol.RunOverlapsResult
		if cerr := c.Call(protocol.MethodRunOverlaps, struct{}{}, &res); cerr != nil {
			t.Fatalf("run.overlaps: %v", cerr)
		}
		byRun := make(map[string]protocol.Overlap, len(res.Overlaps))
		for _, o := range res.Overlaps {
			byRun[o.RunID] = o
		}
		return byRun
	}
	waitFor := func(want func(map[string]protocol.Overlap) bool, what string) map[string]protocol.Overlap {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			got := overlaps()
			if want(got) {
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; last run.overlaps = %+v", what, got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	got := waitFor(func(m map[string]protocol.Overlap) bool {
		return len(m) == 2
	}, "both runs to flag the shared file")
	for _, pair := range [][2]*domain.Run{{e.run, other}, {other, e.run}} {
		o := got[string(pair[0].ID)]
		if len(o.With) != 1 {
			t.Fatalf("run %s overlaps = %+v, want exactly one peer", pair[0].ID, o.With)
		}
		peer := o.With[0]
		if peer.RunID != string(pair[1].ID) || peer.MemberID != string(pair[1].MemberID) {
			t.Errorf("run %s peer = %+v, want run %s owned by %s", pair[0].ID, peer, pair[1].ID, pair[1].MemberID)
		}
		if len(peer.Files) != 1 || peer.Files[0] != "main.go" {
			t.Errorf("run %s overlap files = %v, want [main.go] only (solo.go is not shared)", pair[0].ID, peer.Files)
		}
	}

	finished := time.Now().UTC()
	if err = e.store.UpdateRunStatus(ctx, other.ID, domain.RunMerged, "", nil, &finished); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if _, err = bus.Publish(ctx, events.Event{
		WorkspaceID: e.ws.ID, RunID: other.ID,
		Payload: events.RunStatusPayload{From: domain.RunRunning, To: domain.RunMerged},
	}); err != nil {
		t.Fatalf("publish status: %v", err)
	}
	waitFor(func(m map[string]protocol.Overlap) bool { return len(m) == 0 }, "the flag to clear when a run finishes")
}
