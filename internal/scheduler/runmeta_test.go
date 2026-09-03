package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRecordCommitPersistsMetadata(t *testing.T) {
	e := newTestEnv(t, nil)
	r := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "commit metadata",
		Harness:     "fake",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunRunning,
	}
	if err := e.db.CreateRun(t.Context(), r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if err := e.sched.recordCommit(t.Context(), r.ID, "abc123", at); err != nil {
		t.Fatalf("recordCommit: %v", err)
	}
	got, err := e.db.GetRun(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.LastCommit != "abc123" || !got.LastCommitAt.Equal(at) {
		t.Fatalf("commit metadata = %q/%v, want abc123/%v", got.LastCommit, got.LastCommitAt, at)
	}
}
