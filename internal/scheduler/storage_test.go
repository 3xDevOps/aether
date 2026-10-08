package scheduler

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestStorageRetentionUsesLiveOwnerAfterSidecarWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry *supervised
		want  string
	}{
		{"evidence", &supervised{evidencePending: true}, "Evidence preservation pending"},
		{"destroy", &supervised{destroyPending: true}, "Execution cleanup pending"},
		{"container", &supervised{containerID: "live-container"}, "Retained execution environment"},
		{"finalizing", &supervised{finalizing: true}, "Run finalization in progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			finished := time.Now().UTC().Add(-time.Hour)
			run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "storage ownership", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunCompleted, FinishedAt: &finished}
			if err := e.db.CreateRun(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			old := sidecar{RunID: string(run.ID), ContainerID: "stale-container", RetainedUntil: &finished}
			if err := e.sched.writeSidecar(old); err != nil {
				t.Fatal(err)
			}
			marker := e.sched.sidecarPath(run.ID)
			before, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().UTC().Add(time.Hour)
			entry := tc.entry
			entry.runID, entry.retainedUntil = run.ID, &deadline
			e.sched.mu.Lock()
			e.sched.runs[run.ID] = entry
			e.sched.mu.Unlock()
			defer func() {
				e.sched.mu.Lock()
				delete(e.sched.runs, run.ID)
				e.sched.mu.Unlock()
			}()
			// Force an actual persistence failure without changing the older marker.
			stateDir := e.sched.cfg.StateDir
			e.sched.cfg.StateDir = marker
			err = e.sched.writeSidecar(sidecar{RunID: string(run.ID), RetainedUntil: &deadline, EvidencePending: entry.evidencePending, DestroyPending: entry.destroyPending})
			e.sched.cfg.StateDir = stateDir
			if err == nil {
				t.Fatal("expected sidecar persistence failure")
			}
			until, reason, err := e.sched.StorageRetention(t.Context(), run.ID)
			info, retentionErr := e.sched.Retention(run)
			wantPending := entry.evidencePending || entry.destroyPending || entry.finalizing
			if retentionErr != nil || info.RetainedUntil == nil || !info.RetainedUntil.Equal(deadline) ||
				info.CleanupPending != wantPending {
				t.Fatalf("public live retention = %+v, %v", info, retentionErr)
			}
			if err != nil || !strings.Contains(reason, tc.want) || until == nil || !until.Equal(deadline) {
				t.Fatalf("live ownership lost to stale marker: %v, %q, %v", until, reason, err)
			}
			if until == entry.retainedUntil {
				t.Fatal("retention returned mutable lifecycle deadline pointer")
			}
			*until = finished
			if !entry.retainedUntil.Equal(deadline) {
				t.Fatal("caller mutated lifecycle deadline")
			}
			after, err := os.ReadFile(marker)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("report mutated durable sidecar: %v", err)
			}
			got, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil || got.Status != run.Status || got.Worktree != run.Worktree {
				t.Fatalf("report mutated durable run: %+v, %v", got, err)
			}
		})
	}
}

func TestRetentionSidecarOwnershipIsReadOnlyAndExplicit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		status  domain.RunStatus
		pending bool
		cause   string
		wantErr bool
	}{
		{name: "missing", status: domain.RunCompleted},
		{name: "corrupt", raw: "{", status: domain.RunCompleted, wantErr: true},
		{name: "wrong owner", raw: `{"run_id":"another","destroy_pending":true}`, status: domain.RunCompleted, wantErr: true},
		{name: "unknown owner", raw: `{}`, status: domain.RunCompleted, wantErr: true},
		{name: "expired", raw: `{"run_id":"retention","container_id":"owned","retained_until":"2000-01-01T00:00:00Z"}`, status: domain.RunCompleted, pending: true},
		{name: "evidence without container", raw: `{"run_id":"retention","evidence_pending":true}`, status: domain.RunCompleted, pending: true},
		{name: "active retry", raw: `{"run_id":"retention","destroy_pending":true}`, status: domain.RunProvisioning, pending: true},
		{name: "reopened", raw: `{"run_id":"retention","container_id":"owned","retained_until":"2000-01-01T00:00:00Z"}`, status: domain.RunRunning},
		{name: "private diagnostic", raw: `{"run_id":"retention","destroy_pending":true,"cleanup_error":"/srv/private secret=token"}`, status: domain.RunCompleted, pending: true, cause: cleanupUnknownError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately no Store or Runtime: this API must only read ownership.
			s := &Scheduler{cfg: Config{StateDir: t.TempDir()}}
			run := &domain.Run{ID: "retention", Status: tc.status}
			path := s.sidecarPath(run.ID)
			if tc.raw != "" {
				if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			info, err := s.Retention(run)
			if (err != nil) != tc.wantErr || info.CleanupPending != tc.pending || info.CleanupError != tc.cause {
				t.Fatalf("Retention = %+v, %v", info, err)
			}
			if !run.Status.Terminal() && info.RetainedUntil != nil {
				t.Fatal("active run exposed a stale retention deadline")
			}
			if tc.raw != "" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != tc.raw {
					t.Fatalf("retention read mutated sidecar: %q, %v", after, err)
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("missing ownership read created metadata: %v", err)
			}
		})
	}
}
