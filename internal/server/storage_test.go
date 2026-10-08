package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestServerDiskDurableOwnershipAndPrivacy(t *testing.T) {
	s, root, admin, workspace := newWorkspaceDeletionServer(t)
	write := func(name string, body []byte) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	finished, future, past := now.Add(-96*time.Hour), now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name    string
		until   *time.Time
		pending bool
		corrupt bool
	}{
		{name: "expired checkout"},
		{name: "retained", until: &future},
		{name: "expired owner", until: &past},
		{name: "evidence pending", pending: true},
		{name: "unknown lifecycle", corrupt: true},
	}
	runs := make([]*domain.Run, 0, len(cases))
	for _, tc := range cases {
		run := &domain.Run{WorkspaceID: workspace.ID, MemberID: admin.ID, Task: tc.name, Harness: "fake", Mode: domain.LaunchHeadless, Status: domain.RunCompleted, FinishedAt: &finished}
		if err := s.db.CreateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		run.Worktree = filepath.Join(root, "checkouts", string(run.ID))
		if err := s.db.UpdateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("checkouts", string(run.ID), "result"), []byte("retained work"))
		write(filepath.Join("checkouts", string(run.ID)+".diffsnap", "object"), []byte("history"))
		write(filepath.Join("transcripts", string(run.ID)+".cast"), []byte("run transcript"))
		if tc.until != nil || tc.pending {
			body, err := json.Marshal(map[string]any{"run_id": run.ID, "container_id": "retained-container", "retained": true, "retained_until": tc.until, "evidence_pending": tc.pending})
			if err != nil {
				t.Fatal(err)
			}
			write(filepath.Join("scheduler", string(run.ID)+".json"), body)
		} else if tc.corrupt {
			write(filepath.Join("scheduler", string(run.ID)+".json"), []byte("not JSON"))
		}
		runs = append(runs, run)
	}
	write(filepath.Join("homes", string(admin.ID), "private-token"), []byte("secret content"))
	for _, pool := range []string{"runs", "terminal"} {
		write(filepath.Join("home-caches", string(admin.ID), pool, "data", "package"), []byte("cache"))
	}
	write("profiles/profile", []byte("profile bytes"))
	packet := &store.EvidencePacket{WorkspaceID: workspace.ID, RunID: runs[0].ID, CreatorID: admin.ID, Trigger: store.EvidenceFinish, Objective: "evidence", ExpiresAt: &past, IdempotencyKey: "storage-expiry"}
	if err := s.db.CreateEvidencePacket(t.Context(), packet); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("evidence", evidence.StorageKey(packet)+".transcript"), []byte("durable evidence"))
	readDisk := func(member domain.MemberID) (protocol.ServerDiskResult, string) {
		t.Helper()
		raw, err := s.ssh.Local(member).Call(t.Context(), protocol.MethodServerDisk, nil)
		if err != nil {
			t.Fatal(err)
		}
		var result protocol.ServerDiskResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result, string(raw)
	}
	result, raw := readDisk(admin.ID)
	if result.TotalBytes == 0 || result.HomeBytes == 0 || result.EvidenceBytes == 0 || result.OtherBytes == 0 || result.SnapshotBytes == 0 || result.SnapshotBytes >= result.WorktreeBytes {
		t.Fatalf("missing storage data: %+v", result)
	}
	if result.CacheBytes != 10 {
		t.Fatalf("cache aggregate = %d, want independent pool bytes", result.CacheBytes)
	}
	cachePools := make(map[string]bool)
	for _, entry := range result.Entries {
		if entry.Kind != "cache" {
			continue
		}
		cachePools[entry.Pool] = true
		if entry.OwnerKind != "member" || entry.OwnerID != string(admin.ID) || entry.Bytes != 5 || entry.ReclaimableBytes != nil || entry.Reason == "" {
			t.Fatalf("cache pool attribution: %+v", entry)
		}
		if entry.Error == "" || entry.RetainedUntil != "" {
			t.Fatalf("missing ownership metadata must not promise age-based cleanup: %+v", entry)
		}
	}
	if !cachePools["runs"] || !cachePools["terminal"] || len(cachePools) != 2 {
		t.Fatalf("missing cache ownership pools: %v", cachePools)
	}
	if result.Docker == nil || result.Docker.Error == "" || result.Docker.ImagesBytes != nil {
		t.Fatalf("unavailable Docker was presented as measured: %+v", result.Docker)
	}
	for _, private := range []string{root, "private-token", "secret content", "retained-container"} {
		if strings.Contains(raw, private) {
			t.Fatalf("inventory leaked %q", private)
		}
	}
	for i, tc := range cases {
		for _, kind := range []string{"checkout", "snapshot", "transcript"} {
			found := false
			for _, entry := range result.Entries {
				if entry.OwnerID == string(runs[i].ID) && entry.Kind == kind {
					found = true
					if entry.ReclaimableBytes != nil {
						t.Errorf("%s/%s: %+v", tc.name, kind, entry)
					}
					if kind != "checkout" {
						if entry.RetainedUntil != "" || entry.Error != "" {
							t.Errorf("history inherited compute expiry or uncertainty: %+v", entry)
						}
						continue
					}
					if tc.until != nil && entry.RetainedUntil != tc.until.Format(time.RFC3339Nano) {
						t.Errorf("%s: lost actual deadline: %+v", tc.name, entry)
					}
					if tc.corrupt && entry.Error == "" {
						t.Errorf("corrupt ownership has no error: %+v", entry)
					}
				}
			}
			if !found {
				t.Errorf("missing %s/%s", tc.name, kind)
			}
		}
	}
	foundEvidence := false
	for _, entry := range result.Entries {
		if entry.Kind == "evidence" && entry.OwnerID == string(runs[0].ID) && strings.Contains(entry.Reason, "expiry passed") && entry.RetainedUntil == past.Format(time.RFC3339Nano) {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatal("durable evidence expiry/owner not attributed")
	}
	for _, role := range []domain.Role{domain.RoleCollaborator, domain.RoleViewer} {
		member := &domain.Member{DisplayName: string(role), TailnetLogin: fmt.Sprintf("%s@example.test", role), Role: role}
		if err := s.db.CreateMember(t.Context(), member); err != nil {
			t.Fatal(err)
		}
		got, body := readDisk(member.ID)
		if got.CacheBytes != result.CacheBytes {
			t.Fatalf("%s did not receive safe aggregate cache usage", role)
		}
		if len(got.Entries) != 0 || got.Truncated || strings.Contains(body, string(runs[0].ID)) || strings.Contains(body, string(admin.ID)) {
			t.Fatalf("%s received private owners: %s", role, body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "checkouts", string(runs[0].ID), "result")); err != nil {
		t.Fatalf("report removed expired data: %v", err)
	}
}

func TestServerDiskBoundsLargestUnknownOwners(t *testing.T) {
	s, root, admin, _ := newWorkspaceDeletionServer(t)
	var expected uint64
	for i := 1; i <= 60; i++ {
		path := filepath.Join(root, "checkouts", fmt.Sprintf("unknown-%02d", i), "file")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, i), 0o600); err != nil {
			t.Fatal(err)
		}
		expected += uint64(i)
	}
	raw, err := s.ssh.Local(admin.ID).Call(t.Context(), protocol.MethodServerDisk, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.ServerDiskResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != storageEntryLimit || !result.Truncated || result.WorktreeBytes != expected {
		t.Fatalf("bounded inventory lost aggregate usage: %+v", result)
	}
	var previous = ^uint64(0)
	for _, entry := range result.Entries {
		if entry.Bytes > previous {
			t.Fatal("inventory is not largest first")
		}
		previous = entry.Bytes
		if entry.Kind == "checkout" && (entry.OwnerID != "" || entry.ReclaimableBytes != nil || !strings.Contains(entry.Reason, "No durable owner")) {
			t.Fatalf("unknown storage given deletion authority: %+v", entry)
		}
	}
	if strings.Contains(string(raw), "unknown-") {
		t.Fatal("unowned private directory names leaked")
	}
}

func TestHistoryRetentionIsIndependentOfCheckout(t *testing.T) {
	s, root, admin, workspace := newWorkspaceDeletionServer(t)
	now := time.Now().UTC()
	finished := now.Add(-30 * 24 * time.Hour)
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: admin.ID, Task: "durable history", Harness: "fake", Mode: domain.LaunchHeadless, Status: domain.RunCompleted, FinishedAt: &finished}
	if err := s.db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: s.db, Runs: s.sched, DataDir: root, Config: Config{CheckoutTTL: time.Hour}}
	for _, checkout := range []string{filepath.Join(root, "checkouts", string(run.ID)), ""} {
		run.Worktree = checkout
		if err := s.db.UpdateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		u := disk.Usage{Entries: []disk.Entry{
			{Kind: "transcript", Key: string(run.ID)},
			{Kind: "snapshot", Key: string(run.ID)},
		}}
		attributeStorage(t.Context(), deps, &u, now)
		for _, entry := range u.Entries {
			if entry.OwnerKind != "run" || entry.OwnerID != string(run.ID) || entry.RetainedUntil != nil || entry.ReclaimableBytes != nil || entry.Error != "" {
				t.Fatalf("history inherited checkout cleanup policy: %+v", entry)
			}
		}
	}
}
