package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// Finished, unretained rows must never need the container daemon for deletion.
type workspaceDeletionRuntime struct{ runtime.Runtime }

func newWorkspaceDeletionServer(t *testing.T) (*Server, string, *domain.Member, *domain.Workspace) {
	t.Helper()
	root := t.TempDir()
	s, err := New(t.Context(), Config{DataDir: root, Runtime: workspaceDeletionRuntime{}, StandardImage: "busybox:1.36"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	member := &domain.Member{DisplayName: "Admin", TailnetLogin: "admin@example.test", Role: domain.RoleAdmin}
	if err := s.db.CreateMember(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	ws := &domain.Workspace{Name: "delete-me", BaseBranch: "main"}
	if err := s.db.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git.InitWorkspaceRepo(t.Context(), ws.ID); err != nil {
		t.Fatal(err)
	}
	return s, root, member, ws
}

func callWorkspaceDeletion(t *testing.T, s *Server, member domain.MemberID, ws domain.WorkspaceID) *protocol.Error {
	t.Helper()
	params, err := json.Marshal(protocol.WorkspaceDeleteParams{WorkspaceID: string(ws)})
	if err != nil {
		t.Fatal(err)
	}
	_, perr := s.ssh.Local(member).Call(t.Context(), protocol.MethodWorkspaceDelete, params)
	return perr
}

func TestWorkspaceDeletePopulatedInactiveWorkspace(t *testing.T) {
	t.Parallel()
	s, root, member, ws := newWorkspaceDeletionServer(t)
	ctx := t.Context()
	other := &domain.Workspace{Name: "keep-me", BaseBranch: "main"}
	if err := s.db.CreateWorkspace(ctx, other); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Task: "finished", Harness: "fake", Mode: domain.LaunchHeadless, Status: domain.RunCompleted}
	if err := s.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateApproval(ctx, &store.Approval{WorkspaceID: ws.ID, RunID: run.ID, Action: "Bash"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.PutRunCost(ctx, &store.RunCost{RunID: run.ID, WorkspaceID: ws.ID, MemberID: member.ID, CostUSD: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetWorkspaceBudget(ctx, &store.WorkspaceBudget{WorkspaceID: ws.ID, LimitUSD: 10}); err != nil {
		t.Fatal(err)
	}
	template := &store.Template{WorkspaceID: ws.ID, Name: "saved", Task: "task", Harness: "fake", Mode: domain.LaunchHeadless}
	if err := s.db.SaveTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	mirrorParams, _ := json.Marshal(protocol.WorkspaceMirrorConfigureParams{WorkspaceID: string(ws.ID), SourceURL: "https://github.com/example/project.git", Branch: "main", Auth: "deploy-key"})
	if _, err := s.ssh.Local(member.ID).Call(ctx, protocol.MethodWorkspaceMirrorConfigure, mirrorParams); err != nil {
		t.Fatal(err)
	}
	candidateID := "finished-candidate"
	candidate, _ := json.Marshal(protocol.Candidate{CandidateID: candidateID, WorkspaceID: string(ws.ID), State: protocol.CandidateUnavailable})
	if err := s.db.CreateIntegrationCandidate(ctx, &store.IntegrationCandidate{ID: candidateID, WorkspaceID: ws.ID, ActorKey: "member:" + string(member.ID), IdempotencyKey: "prepare", Digest: "digest", State: "unavailable", Payload: candidate, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	stagedID := "evidence-" + strings.Repeat("a", 64)
	if err := s.db.CreateEvidenceStaging(ctx, &store.EvidenceStaging{ID: stagedID, WorkspaceID: ws.ID, RunID: run.ID, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginServer}, IdempotencyKey: "interrupted-capture", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	removed := []string{
		filepath.Join(root, "checkouts", string(run.ID), "result.txt"),
		filepath.Join(root, "candidates", candidateID, "transcript.txt"),
		filepath.Join(root, "evidence", stagedID+".transcript"),
	}
	for _, path := range removed {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("workspace data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(root, "homes", string(member.ID), "keep.txt")
	if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("member data"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Workspace: ws.ID, Types: []events.Type{events.TypeWorkspaceDeleted}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.GetWorkspace(ctx, ws.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted workspace: %v", err)
	}
	if _, err := s.db.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted run: %v", err)
	}
	if _, err := s.db.GetIntegrationCandidate(ctx, candidateID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted candidate: %v", err)
	}
	if _, err := s.db.GetWorkspaceBudget(ctx, ws.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted budget: %v", err)
	}
	removed = append(removed, filepath.Join(root, "repos", string(ws.ID)+".git"), filepath.Join(root, "mirrors", string(ws.ID)))
	for _, path := range removed {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("deleted artifact %s: %v", path, err)
		}
	}
	if _, err := s.db.GetWorkspace(ctx, other.ID); err != nil {
		t.Fatalf("unrelated workspace: %v", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "member data" {
		t.Fatalf("member home changed: %q, %v", got, err)
	}
	select {
	case event := <-sub.Events():
		if event.WorkspaceID != ws.ID || event.ActorID != member.ID {
			t.Fatalf("deletion event: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace deletion was not published")
	}
}

func TestWorkspaceDeleteRefusesPermissionsAndScheduledWork(t *testing.T) {
	t.Parallel()
	s, _, member, ws := newWorkspaceDeletionServer(t)
	ctx := context.Background()
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleCollaborator} {
		member.Role = role
		if err := s.db.UpdateMember(ctx, member); err != nil {
			t.Fatal(err)
		}
		if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err == nil || err.Code != protocol.CodeDenied {
			t.Fatalf("%s deletion: %v", role, err)
		}
	}
	member.Role = domain.RoleAdmin
	if err := s.db.UpdateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	template := &store.Template{WorkspaceID: ws.ID, Name: "scheduled", Task: "task", Harness: "fake", Mode: domain.LaunchHeadless}
	if err := s.db.SaveTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SaveSchedule(ctx, &store.Schedule{TemplateID: template.ID, Cron: "0 * * * *", MemberID: member.ID}); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err == nil || err.Code != protocol.CodeConflict || !strings.Contains(err.Message, "schedules") {
		t.Fatalf("scheduled workspace deletion: %v", err)
	}
	if _, err := s.db.GetTemplate(ctx, ws.ID, template.Name); err != nil {
		t.Fatalf("refused deletion changed workspace: %v", err)
	}
	if err := s.db.DeleteSchedule(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err == nil || err.Code != protocol.CodeNotFound {
		t.Fatalf("second deletion: %v", err)
	}
}

func TestWorkspaceDeleteCleanupFailureRetainsRetryableWorkspace(t *testing.T) {
	t.Parallel()
	s, root, member, ws := newWorkspaceDeletionServer(t)
	blocker := filepath.Join(root, "mirrors", string(ws.ID), "unexpected-file")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("preserve on failed cleanup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err == nil || !strings.Contains(err.Message, "remove key directory") {
		t.Fatalf("failed cleanup = %v", err)
	}
	if _, err := s.db.GetWorkspace(t.Context(), ws.ID); err != nil {
		t.Fatalf("cleanup failure removed workspace: %v", err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestWorkspaceDeleteRefusesActiveCandidateVerification(t *testing.T) {
	t.Parallel()
	s, _, member, ws := newWorkspaceDeletionServer(t)
	candidateID := "active-verification"
	candidate, _ := json.Marshal(protocol.Candidate{
		CandidateID: candidateID, WorkspaceID: string(ws.ID), State: protocol.CandidateFrozen,
		Verifications: []protocol.Verification{{VerificationID: "check", Status: protocol.VerificationRunning}},
	})
	if err := s.db.CreateIntegrationCandidate(t.Context(), &store.IntegrationCandidate{
		ID: candidateID, WorkspaceID: ws.ID, ActorKey: "member:" + string(member.ID),
		IdempotencyKey: "prepare", Digest: "digest", State: "frozen", Payload: candidate,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := callWorkspaceDeletion(t, s, member.ID, ws.ID); err == nil || err.Code != protocol.CodeConflict || !strings.Contains(err.Message, "verification") {
		t.Fatalf("active verification deletion = %v", err)
	}
	if _, err := s.db.GetWorkspace(t.Context(), ws.ID); err != nil {
		t.Fatalf("active workspace removed: %v", err)
	}
	if _, err := s.db.GetIntegrationCandidate(t.Context(), candidateID); err != nil {
		t.Fatalf("active candidate removed: %v", err)
	}
}
