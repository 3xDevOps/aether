package mission

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type mutableEvidenceReader struct {
	packet protocol.EvidencePacket
	err    error
}

func (r *mutableEvidenceReader) Get(_ context.Context, _ domain.WorkspaceID, id string) (protocol.EvidencePacket, error) {
	if r.err != nil {
		return protocol.EvidencePacket{}, r.err
	}
	if id != r.packet.ID {
		return protocol.EvidencePacket{}, store.ErrNotFound
	}
	return r.packet, nil
}

func openMissionRegressionDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mission.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func regressionMember(t *testing.T, db *store.DB, name string) *domain.Member {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate member key: %v", err)
	}
	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("encode member key: %v", err)
	}
	m := &domain.Member{
		DisplayName: name,
		PublicKey:   string(ssh.MarshalAuthorizedKey(sshKey)),
		Role:        domain.RoleCollaborator,
	}
	if err := db.CreateMember(context.Background(), m); err != nil {
		t.Fatalf("create member %s: %v", name, err)
	}
	return m
}

func regressionWorkspace(t *testing.T, db *store.DB) *domain.Workspace {
	t.Helper()
	w := &domain.Workspace{Name: "mission-regression"}
	if err := db.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return w
}

func regressionMission(t *testing.T, db *store.DB, workspace domain.WorkspaceID, accountable domain.MemberID) *domain.Mission {
	t.Helper()
	m := &domain.Mission{
		WorkspaceID: workspace, Objective: "regression mission", AccountableHumanID: accountable,
		Integrator:            domain.MissionIntegrator{AccountMemberID: accountable, Harness: "claude", Mode: domain.LaunchHeadless},
		ExecutionChoices:      []domain.MissionExecutionChoice{{AccountMemberID: accountable, Harness: "claude", Mode: domain.LaunchHeadless}},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 2, IdempotencyKey: "regression-mission",
	}
	if err := db.CreateMission(context.Background(), m); err != nil {
		t.Fatalf("create mission: %v", err)
	}
	return m
}
func regressionRun(t *testing.T, db *store.DB, runID domain.RunID, workspace domain.WorkspaceID, member domain.MemberID, task string) {
	t.Helper()
	r := &domain.Run{
		ID: runID, WorkspaceID: workspace, MemberID: member, Task: task,
		Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued,
	}
	if err := db.CreateRunWithID(context.Background(), r); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}

func TestTaskMutationUsesReplacementIntegratorAuthorizerAfterOriginalRevocation(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	original := regressionMember(t, db, "original")
	replacement := regressionMember(t, db, "replacement")
	mission := regressionMission(t, db, workspace.ID, original.ID)

	choice := domain.MissionIntegrator{AccountMemberID: replacement.ID, Harness: "claude", Mode: domain.LaunchHeadless}
	mission, err := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, choice, replacement.ID, replacement.ID, "replacement-1")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, db, mission.CurrentIntegratorRunID, workspace.ID, replacement.ID, "integrator")
	original.Pending = true
	if err := db.UpdateMember(ctx, original); err != nil {
		t.Fatalf("revoke original member: %v", err)
	}

	svc, err := New(Config{Store: db, Missions: db, AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	proposal, err := json.Marshal(protocol.TaskProposeParams{
		MissionID:      string(mission.ID),
		Revision:       protocol.TaskRevision{Title: "replacement task", Objective: "replacement task"},
		IdempotencyKey: "task-propose-1",
	})
	if err != nil {
		t.Fatalf("marshal proposal: %v", err)
	}
	result, err := svc.HandleAgent(ctx, mission.CurrentIntegratorRunID, protocol.MethodTaskPropose, proposal)
	if err != nil {
		t.Fatalf("replacement integrator mutation: %v", err)
	}
	mutation, ok := result.(protocol.TaskMutationResult)
	if !ok || mutation.Task.ID == "" {
		t.Fatalf("task proposal result = %#v", result)
	}

	replacement.Pending = true
	if err := db.UpdateMember(ctx, replacement); err != nil {
		t.Fatalf("revoke replacement member: %v", err)
	}
	revise, err := json.Marshal(protocol.TaskReviseParams{
		TaskID:         mutation.Task.ID,
		Revision:       protocol.TaskRevision{Title: "must be denied", Objective: "must be denied"},
		IdempotencyKey: "task-revise-after-revoke",
	})
	if err != nil {
		t.Fatalf("marshal revision: %v", err)
	}
	if _, err := svc.HandleAgent(ctx, mission.CurrentIntegratorRunID, protocol.MethodTaskRevise, revise); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("revoked replacement mutation error = %v, want ErrDenied", err)
	}
}

func setupSubmissionRegression(t *testing.T) (*store.DB, *domain.Mission, *domain.Task, *domain.Submission, *mutableEvidenceReader, *time.Time) {
	t.Helper()
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "integrator")
	mission := regressionMission(t, db, workspace.ID, member.ID)
	regressionRun(t, db, mission.CurrentIntegratorRunID, workspace.ID, member.ID, "integrator")
	task := &domain.Task{
		MissionID: mission.ID,
		Revision: &domain.TaskRevision{
			Title: "evidence task", Objective: "evidence task", Status: domain.TaskRevisionAccepted,
			EvidenceRequirements: []domain.EvidenceRequirement{{Kind: "test"}},
		},
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	attempt, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: "evidence-dispatch", Harness: "claude", Mode: domain.LaunchHeadless,
		ActorRunID: mission.CurrentIntegratorRunID, AuthorizingHumanID: member.ID, RunOwnerID: member.ID, AccountOwnerID: member.ID,
		AuthorityGeneration: mission.IntegratorGeneration, IntegratorGeneration: mission.IntegratorGeneration,
	})
	if err != nil {
		t.Fatalf("reserve attempt: %v", err)
	}
	regressionRun(t, db, attempt.RunID, workspace.ID, member.ID, "worker")
	clock := time.Now().UTC()
	expires := clock.Add(time.Hour).Format(time.RFC3339Nano)
	packet := protocol.EvidencePacket{
		ID: "packet-1", WorkspaceID: string(workspace.ID), RunID: string(attempt.RunID),
		Availability: protocol.EvidenceAvailable, RetainedRevision: "revision-1", ExpiresAt: &expires,
		Sources: []protocol.EvidenceSourceFact{{Name: "test", Available: true}},
	}
	reader := &mutableEvidenceReader{packet: packet}
	submission, err := db.SubmitAttempt(ctx, attempt.ID, mission.IntegratorGeneration, mission.IntegratorGeneration, domain.SubmissionRef{
		WorkspaceID: workspace.ID, RunID: attempt.RunID, EvidenceRef: packet.ID, RetainedRevision: packet.RetainedRevision,
	}, []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: packet.ID, Available: true}, {Kind: "test", Ref: packet.ID, Available: true}}, nil)
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}
	if submission.State != domain.SubmissionProposed {
		t.Fatalf("submission state at report time = %q, want proposed", submission.State)
	}
	return db, mission, task, submission, reader, &clock
}

func TestSubmissionAcceptanceRechecksRetainedEvidenceButReplaysAcceptedReceipt(t *testing.T) {
	t.Run("expired-before-new-acceptance", func(t *testing.T) {
		ctx := context.Background()
		db, mission, task, submission, reader, clock := setupSubmissionRegression(t)
		reader.packet.Availability = protocol.EvidenceExpired
		svc, err := New(Config{Store: db, Missions: db, Evidence: reader, AuthorizationMu: &sync.Mutex{}, Now: func() time.Time { return *clock }})
		if err != nil {
			t.Fatalf("new mission service: %v", err)
		}
		raw, err := json.Marshal(protocol.TaskAcceptSubmissionParams{
			SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: mission.IntegratorGeneration,
			ExpectedAcceptedSetVersion: mission.AcceptedSetVersion, IdempotencyKey: "accept-evidence-expired",
		})
		if err != nil {
			t.Fatalf("marshal acceptance: %v", err)
		}
		if _, err := svc.HandleAgent(ctx, mission.CurrentIntegratorRunID, protocol.MethodTaskAcceptSubmission, raw); !errors.Is(err, store.ErrMissionNotReady) {
			t.Fatalf("expired evidence acceptance error = %v, want ErrMissionNotReady", err)
		}
		stillProposed, err := db.GetSubmission(ctx, submission.ID)
		if err != nil {
			t.Fatalf("get blocked submission: %v", err)
		}
		if stillProposed.State != domain.SubmissionProposed {
			t.Fatalf("blocked submission state = %q, want proposed", stillProposed.State)
		}
		taskAfterBlock, err := db.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("get blocked task: %v", err)
		}
		if taskAfterBlock.Status != domain.TaskReview {
			t.Fatalf("blocked task status = %q, want review", taskAfterBlock.Status)
		}
	})

	t.Run("accepted-replay-after-expiry", func(t *testing.T) {
		ctx := context.Background()
		db, mission, _, submission, reader, clock := setupSubmissionRegression(t)
		svc, err := New(Config{Store: db, Missions: db, Evidence: reader, AuthorizationMu: &sync.Mutex{}, Now: func() time.Time { return *clock }})
		if err != nil {
			t.Fatalf("new mission service: %v", err)
		}
		params := protocol.TaskAcceptSubmissionParams{
			SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: mission.IntegratorGeneration,
			ExpectedAcceptedSetVersion: mission.AcceptedSetVersion, IdempotencyKey: "accept-evidence-replay",
		}
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal acceptance: %v", err)
		}
		if _, err := svc.HandleAgent(ctx, mission.CurrentIntegratorRunID, protocol.MethodTaskAcceptSubmission, raw); err != nil {
			t.Fatalf("available evidence acceptance: %v", err)
		}
		expires, err := time.Parse(time.RFC3339Nano, *reader.packet.ExpiresAt)
		if err != nil {
			t.Fatalf("parse packet expiry: %v", err)
		}
		*clock = expires.Add(time.Second)
		if _, err := svc.HandleAgent(ctx, mission.CurrentIntegratorRunID, protocol.MethodTaskAcceptSubmission, raw); err != nil {
			t.Fatalf("accepted receipt replay after expiry: %v", err)
		}
		accepted, err := db.GetSubmission(ctx, submission.ID)
		if err != nil {
			t.Fatalf("get accepted submission: %v", err)
		}
		if accepted.State != domain.SubmissionAccepted {
			t.Fatalf("accepted submission state = %q, want accepted", accepted.State)
		}
	})
}
