//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type candidateLifecycleFixture struct {
	ctx       context.Context
	root      string
	repos     string
	db        *store.DB
	git       *gitengine.Engine
	evidence  *evidence.Service
	runtime   *runtime.Docker
	service   *Service
	workspace *domain.Workspace
	owner     *domain.Member
	nowMu     sync.Mutex
	now       time.Time
	admitMu   sync.Mutex
	admitted  []Admission
}

type candidateLifecycleFailingStore struct {
	*store.DB
	mu      sync.Mutex
	updates int
	failOn  int
	err     error
}

func (s *candidateLifecycleFailingStore) UpdateIntegrationCandidate(ctx context.Context, record *store.IntegrationCandidate, expectedVersion int64) error {
	s.mu.Lock()
	s.updates++
	fail := s.failOn != 0 && s.updates == s.failOn
	err := s.err
	s.mu.Unlock()
	if fail {
		return err
	}
	return s.DB.UpdateIntegrationCandidate(ctx, record, expectedVersion)
}

type candidateLifecycleSource struct {
	run    *domain.Run
	branch string
	packet protocol.EvidencePacket
}

type candidateLifecycleTranscript struct {
	data []byte
}

func (t candidateLifecycleTranscript) Replay(domain.RunID) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(t.data)), nil
}

func newCandidateLifecycleFixture(t *testing.T, transcript io.Reader, withAdmission bool) *candidateLifecycleFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	repos := filepath.Join(root, "repos")
	checkouts := filepath.Join(root, "checkouts")
	if err := os.MkdirAll(repos, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkouts, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { _ = db.Close() }
	t.Cleanup(cleanup)
	workspace := &domain.Workspace{Name: "candidate-lifecycle", BaseBranch: "main"}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	owner := &domain.Member{
		DisplayName: "Candidate Owner",
		PublicKey:   candidateLifecyclePublicKey(t),
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, owner); err != nil {
		t.Fatal(err)
	}
	git, err := gitengine.New(gitengine.Config{ReposDir: repos, CheckoutsDir: checkouts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	if _, err := git.InitWorkspaceRepo(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	candidateLifecycleSeedRepo(t, filepath.Join(repos, string(workspace.ID)+".git"))
	var exporter evidence.TranscriptExporter
	if transcript != nil {
		data, err := io.ReadAll(transcript)
		if err != nil {
			t.Fatal(err)
		}
		exporter = candidateLifecycleTranscript{data: data}
	}
	evidenceSvc, err := evidence.New(evidence.Config{
		Store:       db,
		Git:         git,
		Runs:        db,
		Transcript:  exporter,
		EvidenceDir: filepath.Join(root, "evidence"),
		Now:         func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	docker, err := runtime.NewDocker(runtime.WithNamePrefix("aether-candidate-test-"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	f := &candidateLifecycleFixture{
		ctx: ctx, root: root, repos: repos, db: db, git: git, evidence: evidenceSvc,
		runtime: docker, workspace: workspace, owner: owner,
		now: time.Now().UTC(),
	}
	var admissionFn AdmissionFunc
	if withAdmission {
		admissionFn = func(_ context.Context, a Admission) (func(), error) {
			f.admitMu.Lock()
			f.admitted = append(f.admitted, a)
			f.admitMu.Unlock()
			return func() {}, nil
		}
	}
	service, err := New(Config{
		Store: db, Git: git, Evidence: evidenceSvc, Runtime: docker,
		Root: filepath.Join(root, "candidate-artifacts"),
		Environment: func(_ context.Context, _ Actor, _ *domain.Workspace, _ string) (runtime.Spec, error) {
			return runtime.Spec{Image: "busybox:1.36", Command: []string{"true"}}, nil
		},
		Admission: admissionFn,
		Now: func() time.Time {
			f.nowMu.Lock()
			defer f.nowMu.Unlock()
			return f.now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	return f
}

func candidateLifecyclePublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func candidateLifecycleSeedRepo(t *testing.T, repo string) {
	t.Helper()
	source := t.TempDir()
	candidateLifecycleGit(t, source, "init")
	candidateLifecycleGit(t, source, "config", "user.name", "seed")
	candidateLifecycleGit(t, source, "config", "user.email", "seed@aether.local")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidateLifecycleGit(t, source, "add", "README.md")
	candidateLifecycleGit(t, source, "commit", "-m", "base")
	candidateLifecycleGit(t, source, "branch", "-M", "main")
	candidateLifecycleGit(t, source, "remote", "add", "origin", repo)
	candidateLifecycleGit(t, source, "push", "origin", "HEAD:refs/heads/main")
}

func candidateLifecycleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *candidateLifecycleFixture) source(t *testing.T, runID, path, content, key string, sourceFacts []store.EvidenceSourceFact) candidateLifecycleSource {
	t.Helper()
	run := &domain.Run{
		ID: domain.RunID(runID), WorkspaceID: f.workspace.ID, MemberID: f.owner.ID,
		Task: "retain candidate source", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunCompleted,
	}
	if err := f.db.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	checkout, branch, err := f.git.CreateRunCheckout(f.ctx, f.workspace.ID, run.ID, "main", run.Task, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.git.CommitAll(f.ctx, run.ID, "source change", f.owner.GitIdentity(), nil); err != nil {
		t.Fatal(err)
	}
	packet, err := f.evidence.Capture(f.ctx, evidence.Request{
		RunID: run.ID, CreatorID: f.owner.ID, IdempotencyKey: key,
		SourceFacts: sourceFacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidateLifecycleSource{run: run, branch: branch, packet: packet}
}

func (f *candidateLifecycleFixture) prepare(t *testing.T, key string, sources ...candidateLifecycleSource) protocol.Candidate {
	t.Helper()
	subs := make([]protocol.SubmissionRef, len(sources))
	for i, source := range sources {
		subs[i] = protocol.SubmissionRef{
			WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID),
			EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision,
		}
	}
	base := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
	candidate, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID), Submissions: subs,
		TargetRef: "refs/heads/main", ExpectedTargetRevision: base, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func (f *candidateLifecycleFixture) removeSource(t *testing.T, source candidateLifecycleSource) {
	t.Helper()
	if err := f.git.RemoveRunCheckout(f.ctx, source.run.ID); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	candidateLifecycleGit(t, repo, "update-ref", "refs/heads/"+source.branch, source.packet.BaseRevision)
	candidateLifecycleGit(t, repo, "update-ref", "-d", "refs/heads/"+source.branch)
	if err := f.evidence.PurgeRun(f.ctx, f.workspace.ID, source.run.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DeleteRun(f.ctx, source.run.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *candidateLifecycleFixture) candidateFile(t *testing.T, c protocol.Candidate, path string) string {
	t.Helper()
	checkout, err := f.git.CandidateCheckout(f.ctx, f.workspace.ID, c.CandidateID, c.ExpectedTargetRevision)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func (f *candidateLifecycleFixture) candidateCommitFile(t *testing.T, c protocol.Candidate, path string) string {
	t.Helper()
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	cmd := exec.Command("git", "--git-dir", repo, "show", c.CandidateRevision+":"+filepath.ToSlash(path))
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show %s:%s: %v\n%s", c.CandidateRevision, path, err, out)
	}
	return string(out)
}

func TestCandidateLifecycleRetainsCombinedInputsAfterSourcePurge(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	first := f.source(t, "candidate-run-one", "one.txt", "one\n", "packet-one", nil)
	second := f.source(t, "candidate-run-two", "two.txt", "two\n", "packet-two", nil)
	candidate := f.prepare(t, "candidate-combined", first, second)
	if candidate.State != protocol.CandidateFrozen || candidate.AppliedInputs != 2 || candidate.CandidateRevision == "" {
		t.Fatalf("prepared candidate = %+v, want frozen two-input candidate", candidate)
	}
	if got := f.candidateFile(t, candidate, "one.txt"); got != "one\n" {
		t.Fatalf("retained one.txt = %q", got)
	}
	if got := f.candidateFile(t, candidate, "two.txt"); got != "two\n" {
		t.Fatalf("retained two.txt = %q", got)
	}
	if got := f.candidateCommitFile(t, candidate, "one.txt"); got != "one\n" {
		t.Fatalf("retained commit one.txt = %q", got)
	}
	if got := f.candidateCommitFile(t, candidate, "two.txt"); got != "two\n" {
		t.Fatalf("retained commit two.txt = %q", got)
	}
	f.removeSource(t, first)
	f.removeSource(t, second)
	shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID})
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != protocol.CandidateFrozen || shown.CandidateRevision != candidate.CandidateRevision {
		t.Fatalf("candidate after source purge = %+v", shown)
	}
	if got := f.candidateFile(t, shown, "one.txt"); got != "one\n" {
		t.Fatalf("owned one.txt after source purge = %q", got)
	}
	if got := f.candidateFile(t, shown, "two.txt"); got != "two\n" {
		t.Fatalf("owned two.txt after source purge = %q", got)
	}
}

func TestCandidateLifecyclePrepareRetryAfterInputSaveFailure(t *testing.T) {
	f := newCandidateLifecycleFixture(t, strings.NewReader("retry transcript\n"), false)
	source := f.source(t, "candidate-prepare-fault", "source.txt", "source\n", "packet-prepare-fault", nil)
	base := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
	params := protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID),
		Submissions: []protocol.SubmissionRef{{
			WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID),
			EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision,
		}},
		TargetRef: "refs/heads/main", ExpectedTargetRevision: base, IdempotencyKey: "prepare-input-save-fault",
	}
	saveErr := errors.New("input append save failed")
	f.service.store = &candidateLifecycleFailingStore{DB: f.db, failOn: 1, err: saveErr}
	if _, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params); !errors.Is(err, saveErr) {
		t.Fatalf("faulted Prepare error = %v, want append-save error", err)
	}
	row, err := f.db.GetIntegrationCandidateByKey(f.ctx, f.workspace.ID, "member:"+string(f.owner.ID), params.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.State != string(protocol.CandidatePreparing) {
		t.Fatalf("durable candidate after append-save failure = %+v", row)
	}
	var durable protocol.Candidate
	if err := json.Unmarshal(row.Payload, &durable); err != nil {
		t.Fatal(err)
	}
	if len(durable.Inputs) != 0 {
		t.Fatalf("durable inputs after append-save failure = %+v, want empty before retry", durable.Inputs)
	}
	candidateID := row.ID
	retried, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatalf("Prepare retry after append-save failure: %v", err)
	}
	if retried.CandidateID != candidateID || retried.State != protocol.CandidateFrozen ||
		retried.AppliedInputs != 1 || len(retried.Inputs) != 1 || retried.CandidateRevision == "" {
		t.Fatalf("retried candidate = %+v", retried)
	}
	if retried.Inputs[0].Submission != params.Submissions[0] {
		t.Fatalf("retried input submission = %+v, want %+v", retried.Inputs[0].Submission, params.Submissions[0])
	}
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	if got := candidateLifecycleGit(t, repo, "rev-parse", "refs/aether/candidates/"+candidateID+"/inputs/0"); got != source.packet.RetainedRevision {
		t.Fatalf("retained candidate input = %s, want %s", got, source.packet.RetainedRevision)
	}
	artifact := filepath.Join(f.root, "candidate-artifacts", "candidates", candidateID, "0.transcript")
	if got, err := os.ReadFile(artifact); err != nil || string(got) != "retry transcript\n" {
		t.Fatalf("retained transcript = %q, %v", got, err)
	}
	f.removeSource(t, source)
	replay, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatalf("completed Prepare replay after source deletion: %v", err)
	}
	if replay.CandidateID != retried.CandidateID || replay.CandidateRevision != retried.CandidateRevision ||
		replay.State != protocol.CandidateFrozen {
		t.Fatalf("completed replay after source deletion = %+v, original = %+v", replay, retried)
	}
}

func TestCandidateLifecycleResolveConflictToImmutableRevision(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	first := f.source(t, "candidate-conflict-one", "conflict.txt", "first\n", "packet-conflict-one", nil)
	second := f.source(t, "candidate-conflict-two", "conflict.txt", "second\n", "packet-conflict-two", nil)
	candidate := f.prepare(t, "candidate-conflict", first, second)
	if candidate.State != protocol.CandidateConflicted || len(candidate.Conflicts) != 1 || candidate.CandidateRevision == "" {
		t.Fatalf("conflicting candidate = %+v", candidate)
	}
	resolved, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: candidate.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "resolved\n"}}, IdempotencyKey: "resolve-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != protocol.CandidateFrozen || len(resolved.Conflicts) != 0 || resolved.CandidateRevision == "" {
		t.Fatalf("resolved candidate = %+v", resolved)
	}
	if got := f.candidateFile(t, resolved, "conflict.txt"); got != "resolved\n" {
		t.Fatalf("resolved conflict content = %q", got)
	}
	retry, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: candidate.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "resolved\n"}}, IdempotencyKey: "resolve-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.CandidateRevision != resolved.CandidateRevision || retry.Version != resolved.Version {
		t.Fatalf("idempotent resolve changed candidate: first=%+v retry=%+v", resolved, retry)
	}
	preparedAgain, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID), Submissions: []protocol.SubmissionRef{
			{WorkspaceID: string(f.workspace.ID), RunID: string(first.run.ID), EvidenceRef: first.packet.ID, RetainedRevision: first.packet.RetainedRevision},
			{WorkspaceID: string(f.workspace.ID), RunID: string(second.run.ID), EvidenceRef: second.packet.ID, RetainedRevision: second.packet.RetainedRevision},
		}, TargetRef: "refs/heads/main", ExpectedTargetRevision: candidate.ExpectedTargetRevision, IdempotencyKey: "candidate-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preparedAgain.CandidateRevision != resolved.CandidateRevision || preparedAgain.Version != resolved.Version || f.candidateFile(t, preparedAgain, "conflict.txt") != "resolved\n" {
		t.Fatalf("idempotent prepare changed frozen tree: %+v", preparedAgain)
	}
}
func TestCandidateLifecycleResolveVersionFencePreservesPartialAssembly(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	first := f.source(t, "candidate-version-one", "conflict.txt", "first\n", "packet-version-one", nil)
	second := f.source(t, "candidate-version-two", "conflict.txt", "second\n", "packet-version-two", nil)
	third := f.source(t, "candidate-version-three", "conflict.txt", "third\n", "packet-version-three", nil)
	candidate := f.prepare(t, "candidate-version-fence", first, second, third)
	if candidate.State != protocol.CandidateConflicted || len(candidate.Conflicts) != 1 || candidate.AppliedInputs != 1 {
		t.Fatalf("initial repeated conflict candidate = %+v", candidate)
	}

	if _, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: 0,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "missing version\n"}}, IdempotencyKey: "resolve-missing-version",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nonpositive expected version error = %v, want invalid request", err)
	}

	partial, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: candidate.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "resolved first\n"}}, IdempotencyKey: "resolve-first-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.State != protocol.CandidateConflicted || len(partial.Conflicts) != 1 ||
		partial.AppliedInputs != 2 || partial.Version <= candidate.Version ||
		partial.CandidateRevision == candidate.CandidateRevision {
		t.Fatalf("partial resolution = %+v, want second input applied and final conflict", partial)
	}
	beforeContent := f.candidateFile(t, partial, "conflict.txt")
	if _, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: candidate.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "stale content\n"}}, IdempotencyKey: "resolve-stale-version",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected version error = %v, want conflict", err)
	}
	unchanged, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.State != partial.State || unchanged.Version != partial.Version ||
		unchanged.CandidateRevision != partial.CandidateRevision || unchanged.AppliedInputs != partial.AppliedInputs ||
		len(unchanged.Conflicts) != len(partial.Conflicts) || f.candidateFile(t, unchanged, "conflict.txt") != beforeContent {
		t.Fatalf("candidate changed after stale resolve: before=%+v after=%+v", partial, unchanged)
	}

	frozen, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: partial.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "resolved final\n"}}, IdempotencyKey: "resolve-final-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.State != protocol.CandidateFrozen || len(frozen.Conflicts) != 0 ||
		frozen.AppliedInputs != 3 || frozen.CandidateRevision == "" ||
		f.candidateFile(t, frozen, "conflict.txt") != "resolved final\n" {
		t.Fatalf("fresh resolution = %+v", frozen)
	}
}

func TestCandidateLifecycleResolveRetryAfterFinalSaveFailure(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	first := f.source(t, "candidate-resolve-fault-one", "conflict.txt", "first\n", "packet-resolve-fault-one", nil)
	second := f.source(t, "candidate-resolve-fault-two", "conflict.txt", "second\n", "packet-resolve-fault-two", nil)
	candidate := f.prepare(t, "candidate-resolve-fault", first, second)
	if candidate.State != protocol.CandidateConflicted {
		t.Fatalf("candidate before faulted Resolve = %+v", candidate)
	}
	saveErr := errors.New("final candidate save failed")
	f.service.store = &candidateLifecycleFailingStore{DB: f.db, failOn: 2, err: saveErr}
	params := protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: candidate.Version,
		Files:           []protocol.CandidateResolution{{Path: "conflict.txt", Content: "resolved after retry\n"}},
		IdempotencyKey:  "resolve-final-save-fault",
	}
	if _, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, params); !errors.Is(err, saveErr) {
		t.Fatalf("faulted Resolve error = %v, want final-save error", err)
	}
	if _, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationResolveParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		ExpectedVersion: params.ExpectedVersion,
		Files:           params.Files, IdempotencyKey: "different-resolve-key",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different-key Resolve while retry pending = %v, want conflict before Git", err)
	}
	assembly, err := f.git.AssembleCandidate(f.ctx, f.workspace.ID, candidate.CandidateID, []gitengine.CandidateRevisionInput{
		{Revision: first.packet.RetainedRevision, Base: first.packet.BaseRevision},
		{Revision: second.packet.RetainedRevision, Base: second.packet.BaseRevision},
	})
	if err != nil || assembly.Revision == "" || assembly.AppliedInputs != 2 || len(assembly.Conflicts) != 0 {
		t.Fatalf("Git assembly after failed final save = %+v, %v", assembly, err)
	}
	retried, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatalf("Resolve retry after frozen Git assembly: %v", err)
	}
	if retried.State != protocol.CandidateFrozen || retried.CandidateRevision == "" ||
		f.candidateFile(t, retried, "conflict.txt") != "resolved after retry\n" {
		t.Fatalf("reconciled candidate = %+v", retried)
	}
	changed := params
	changed.Files = []protocol.CandidateResolution{{Path: "conflict.txt", Content: "changed payload\n"}}
	if _, err := f.service.Resolve(f.ctx, Actor{MemberID: f.owner.ID}, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed same-key Resolve error = %v, want conflict", err)
	}
}

func TestCandidateLifecycleUnavailableRequiredSourceLeavesTargetUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		truncated bool
	}{
		{name: "unavailable"},
		{name: "truncated", truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCandidateLifecycleFixture(t, nil, false)
			available := tc.name != "unavailable"
			source := f.source(t, "candidate-source-"+tc.name, "source.txt", "source\n", "packet-source-"+tc.name, []store.EvidenceSourceFact{{Name: "required", Available: available, Truncated: tc.truncated, Reason: tc.name}})
			before := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
			sub := protocol.SubmissionRef{WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID), EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision}
			_, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationPrepareParams{
				WorkspaceID: string(f.workspace.ID), Submissions: []protocol.SubmissionRef{sub},
				TargetRef: "refs/heads/main", ExpectedTargetRevision: before, RequiredSources: []string{"required"}, IdempotencyKey: "required-" + tc.name,
			})
			if err == nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Prepare error = %v, want unavailable", err)
			}
			after := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
			if after != before {
				t.Fatalf("target changed after refused assembly: before=%s after=%s", before, after)
			}
		})
	}
}

func TestCandidateLifecyclePrepareFailureJoinsSaveError(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	source := f.source(t, "candidate-prepare-source-fault", "source.txt", "source\n", "packet-prepare-source-fault",
		[]store.EvidenceSourceFact{{Name: "required", Available: false, Reason: "fault injected"}})
	base := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
	saveErr := errors.New("prepare failure save failed")
	f.service.store = &candidateLifecycleFailingStore{DB: f.db, failOn: 1, err: saveErr}
	_, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID),
		Submissions: []protocol.SubmissionRef{{
			WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID),
			EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision,
		}},
		TargetRef: "refs/heads/main", ExpectedTargetRevision: base,
		RequiredSources: []string{"required"}, IdempotencyKey: "prepare-source-save-fault",
	})
	if err == nil || !errors.Is(err, ErrUnavailable) || !errors.Is(err, saveErr) ||
		!strings.Contains(err.Error(), "source required unavailable") ||
		!strings.Contains(err.Error(), "prepare failure save failed") {
		t.Fatalf("joined Prepare failure = %v, want source and persistence errors", err)
	}
}

func TestCandidateLifecycleOwnedInputAndArtifactLossInvalidatesCandidate(t *testing.T) {
	f := newCandidateLifecycleFixture(t, strings.NewReader("durable transcript\n"), false)
	source := f.source(t, "candidate-owned-input", "owned.txt", "owned\n", "packet-owned-input", nil)
	first := f.prepare(t, "candidate-input-loss", source)
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	candidateLifecycleGit(t, repo, "update-ref", "-d", "refs/aether/candidates/"+first.CandidateID+"/inputs/0")
	shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{WorkspaceID: string(f.workspace.ID), CandidateID: first.CandidateID})
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != protocol.CandidateUnavailable || shown.Error == "" {
		t.Fatalf("missing owned input did not invalidate candidate: %+v", shown)
	}

	second := f.prepare(t, "candidate-artifact-loss", source)
	artifact := filepath.Join(f.root, "candidate-artifacts", "candidates", second.CandidateID, "0.transcript")
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	shown, err = f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{WorkspaceID: string(f.workspace.ID), CandidateID: second.CandidateID})
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != protocol.CandidateUnavailable || shown.Error == "" {
		t.Fatalf("missing owned transcript did not invalidate candidate: %+v", shown)
	}
}

func TestCandidateLifecycleSourceCallbackErrorRetainsRecoverablePacket(t *testing.T) {
	f := newCandidateLifecycleFixture(t, strings.NewReader("recoverable transcript\n"), false)
	source := f.source(t, "candidate-callback-error", "source.txt", "source\n", "packet-callback-error", nil)
	wantErr := errors.New("preservation callback failed")
	err := f.evidence.WithCandidateSource(f.ctx, f.workspace.ID, source.packet.ID, func(_ *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
		if transcript == nil {
			t.Fatal("candidate callback received nil transcript")
		}
		if _, err := io.ReadAll(transcript); err != nil {
			t.Fatal(err)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("callback error = %v, want wrapped callback error", err)
	}
	packet, err := f.db.GetEvidencePacket(f.ctx, source.packet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if packet == nil || packet.RetainedRevision != source.packet.RetainedRevision {
		t.Fatalf("recoverable packet after callback error = %+v", packet)
	}
	if err := f.evidence.WithCandidateSource(f.ctx, f.workspace.ID, source.packet.ID, func(got *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
		if got == nil || transcript == nil {
			return fmt.Errorf("source was not recoverable")
		}
		_, err := io.ReadAll(transcript)
		return err
	}); err != nil {
		t.Fatalf("retry source preservation: %v", err)
	}
}

func TestCandidateLifecycleViewerReadDeleteFenceAndAdmission(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, true)
	source := f.source(t, "candidate-permissions", "source.txt", "source\n", "packet-permissions", nil)
	candidate := f.prepare(t, "candidate-permissions", source)
	f.admitMu.Lock()
	if len(f.admitted) == 0 || f.admitted[0].Operation != protocol.MethodIntegrationPrepare || f.admitted[0].MissionID != "" {
		admitted := append([]Admission(nil), f.admitted...)
		f.admitMu.Unlock()
		t.Fatalf("empty-mission admission records = %+v", admitted)
	}
	f.admitMu.Unlock()
	viewer := &domain.Member{DisplayName: "Candidate Viewer", PublicKey: candidateLifecyclePublicKey(t), Color: "#3cb44b", Role: domain.RoleViewer}
	if err := f.db.CreateMember(f.ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Show(f.ctx, Actor{MemberID: viewer.ID}, protocol.IntegrationShowParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID}); err != nil {
		t.Fatalf("viewer Show: %v", err)
	}
	if err := f.service.Delete(f.ctx, Actor{MemberID: viewer.ID}, protocol.IntegrationDeleteParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID}); err == nil || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("viewer Delete error = %v, want unauthorized", err)
	}
	if err := f.service.Delete(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeleteParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID}); err != nil {
		t.Fatal(err)
	}
	row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.State != string(protocol.CandidateExpired) {
		t.Fatalf("delete tombstone = %+v", row)
	}
}

func TestCandidateLifecycleDeleteFenceCleansArtifactsAndTombstone(t *testing.T) {
	f := newCandidateLifecycleFixture(t, strings.NewReader("delete transcript\n"), false)
	source := f.source(t, "candidate-delete-fence", "source.txt", "source\n", "packet-delete-fence", nil)
	candidate := f.prepare(t, "candidate-delete-fence", source)
	candidatePath, err := f.git.CandidateCheckout(f.ctx, f.workspace.ID, candidate.CandidateID, candidate.ExpectedTargetRevision)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(f.root, "candidate-artifacts", "candidates", candidate.CandidateID, "0.transcript")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Delete(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeleteParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate checkout after delete = %v, want removed", err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate artifact after delete = %v, want removed", err)
	}
	f.nowMu.Lock()
	f.now = f.now.Add(protocol.IntegrationTombstoneRetention + time.Second)
	f.nowMu.Unlock()
	if _, err := f.service.Cleanup(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tombstone after retention = %v, want not found", err)
	}
}

func TestCandidateLifecycleExpiryFenceRemovesOwnedGit(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	source := f.source(t, "candidate-expiry-fence", "source.txt", "source\n", "packet-expiry-fence", nil)
	candidate := f.prepare(t, "candidate-expiry-fence", source)
	f.nowMu.Lock()
	f.now = candidate.ExpiresAt.Add(time.Second)
	f.nowMu.Unlock()
	if n, err := f.service.Cleanup(f.ctx); err != nil || n != 1 {
		t.Fatalf("expiry cleanup = %d, %v", n, err)
	}
	row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != string(protocol.CandidateExpired) {
		t.Fatalf("expiry tombstone state = %q", row.State)
	}
	f.nowMu.Lock()
	f.now = f.now.Add(protocol.IntegrationTombstoneRetention + time.Second)
	f.nowMu.Unlock()
	if n, err := f.service.Cleanup(f.ctx); err != nil || n != 1 {
		t.Fatalf("tombstone cleanup = %d, %v", n, err)
	}
	if _, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired candidate after tombstone retention = %v", err)
	}
}

func TestCandidateLifecyclePrepareReplayAfterSourceRunDeletion(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, true)
	source := f.source(t, "candidate-run-deleted", "source.txt", "source\n", "packet-run-deleted", nil)
	candidate := f.prepare(t, "prepare-after-run-delete", source)
	params := protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID),
		Submissions: []protocol.SubmissionRef{{
			WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID),
			EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision,
		}},
		TargetRef: "refs/heads/main", ExpectedTargetRevision: candidate.ExpectedTargetRevision,
		IdempotencyKey: "prepare-after-run-delete",
	}
	f.removeSource(t, source)
	replay, err := f.service.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatalf("Prepare replay after source run deletion: %v", err)
	}
	if replay.CandidateID != candidate.CandidateID || replay.CandidateRevision != candidate.CandidateRevision ||
		replay.State != protocol.CandidateFrozen || f.candidateFile(t, replay, "source.txt") != "source\n" {
		t.Fatalf("replayed candidate = %+v, original = %+v", replay, candidate)
	}
	f.admitMu.Lock()
	defer f.admitMu.Unlock()
	var prepares int
	for _, admission := range f.admitted {
		if admission.Operation == protocol.MethodIntegrationPrepare && admission.MissionID == "" {
			prepares++
		}
	}
	if prepares < 2 {
		t.Fatalf("Prepare replay did not cross empty-mission Admission boundary twice: %+v", f.admitted)
	}
}

func candidateLifecycleSetState(t *testing.T, f *candidateLifecycleFixture, c protocol.Candidate, state protocol.CandidateState) {
	t.Helper()
	row, err := f.db.GetIntegrationCandidate(f.ctx, c.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	var stored protocol.Candidate
	if err := json.Unmarshal(row.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	stored.State = state
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	expected := row.Version
	row.Payload = payload
	row.State = string(state)
	row.Version++
	if err := f.db.UpdateIntegrationCandidate(f.ctx, row, expected); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateLifecycleShowPreservesDeletingAndExpiredTombstones(t *testing.T) {
	for _, state := range []protocol.CandidateState{protocol.CandidateDeleting, protocol.CandidateExpired} {
		t.Run(string(state), func(t *testing.T) {
			f := newCandidateLifecycleFixture(t, nil, false)
			source := f.source(t, "candidate-tombstone-"+string(state), "source.txt", "source\n", "packet-tombstone-"+string(state), nil)
			candidate := f.prepare(t, "candidate-tombstone-"+string(state), source)
			repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
			candidateLifecycleGit(t, repo, "update-ref", "-d", "refs/aether/candidates/"+candidate.CandidateID+"/inputs/0")
			candidateLifecycleSetState(t, f, candidate, state)
			shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
				WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if shown.State != state {
				t.Fatalf("Show changed %s tombstone to %s", state, shown.State)
			}
			row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
			if err != nil {
				t.Fatal(err)
			}
			if row.State != string(state) {
				t.Fatalf("durable tombstone state = %q, want %q", row.State, state)
			}
		})
	}
}
