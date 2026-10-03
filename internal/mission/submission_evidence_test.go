package mission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// The exporter is finite, but generates bytes incrementally so the cap regression
// does not need another >16 MiB allocation just to prepare its source.
type submissionTranscript struct {
	size int64
	err  error
}

type submissionBytes struct{}

func (submissionBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (s submissionTranscript) Replay(domain.RunID) (io.ReadCloser, error) {
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(io.LimitReader(submissionBytes{}, s.size)), nil
}

// All writes and normal reads use real private files. Failures are injected only
// after Capture has successfully retained the transcript.
type submissionFS struct {
	transcript      string
	openErr         error
	readErr         error
	readBytes       int64
	transcriptOpens int
}

func (*submissionFS) MkdirAll(path string, mode fs.FileMode) error { return os.MkdirAll(path, mode) }
func (*submissionFS) CreateTemp(dir, pattern string) (evidence.File, error) {
	return os.CreateTemp(dir, pattern)
}
func (f *submissionFS) Rename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if strings.HasSuffix(to, ".transcript") {
		f.transcript = to
	}
	return nil
}
func (*submissionFS) SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (*submissionFS) Remove(path string) error                   { return os.Remove(path) }
func (*submissionFS) Stat(path string) (fs.FileInfo, error)      { return os.Stat(path) }
func (*submissionFS) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }
func (f *submissionFS) Open(path string) (io.ReadCloser, error) {
	if strings.HasSuffix(path, ".transcript") {
		f.transcriptOpens++
		if f.openErr != nil {
			return nil, f.openErr
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".transcript") {
		return &submissionReadCloser{ReadCloser: file, fs: f}, nil
	}
	return file, nil
}

type submissionReadCloser struct {
	io.ReadCloser
	fs *submissionFS
}

func (r *submissionReadCloser) Read(p []byte) (int, error) {
	if r.fs.readErr != nil {
		return 0, r.fs.readErr
	}
	n, err := r.ReadCloser.Read(p)
	r.fs.readBytes += int64(n)
	return n, err
}

type retainedSubmissionFixture struct {
	reconcileReportFixture
	report   *store.CoordReport
	retained *evidence.Service
	fs       *submissionFS
	clock    time.Time
}

func newRetainedSubmissionFixture(t *testing.T, requirements []domain.EvidenceRequirement, transcript evidence.TranscriptExporter) *retainedSubmissionFixture {
	t.Helper()
	fix, report := setupReconcileReportRequirements(t, store.CoordOutcomeSuccess, domain.LaunchHeadless, requirements)
	f := &retainedSubmissionFixture{reconcileReportFixture: fix, report: report, fs: &submissionFS{}, clock: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	repos := filepath.Join(root, "repos")
	git, err := gitengine.New(gitengine.Config{ReposDir: repos, CheckoutsDir: filepath.Join(root, "checkouts")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	ctx := context.Background()
	if _, initErr := git.InitWorkspaceRepo(ctx, f.mission.WorkspaceID); initErr != nil {
		t.Fatal(initErr)
	}
	seed := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = seed
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		if out, commandErr := cmd.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, out)
		}
	}
	runGit("init")
	runGit("-c", "user.name=Evidence", "-c", "user.email=evidence@aether.local", "commit", "--allow-empty", "-m", "base")
	runGit("push", filepath.Join(repos, string(f.mission.WorkspaceID)+".git"), "HEAD:refs/heads/main")
	checkout, _, err := git.CreateRunCheckout(ctx, f.mission.WorkspaceID, f.attempt.RunID, "main", "retained source", "")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "result.txt"), []byte("captured result\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	f.retained, err = evidence.New(evidence.Config{
		Store: f.db, Git: git, Runs: f.db, Transcript: transcript,
		EvidenceDir: filepath.Join(root, "evidence"), FS: f.fs, Now: func() time.Time { return f.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.cfg.Evidence = f.retained
	f.svc.cfg.Now = func() time.Time { return f.clock }
	return f
}

func (f *retainedSubmissionFixture) capture(t *testing.T, sourceFacts []store.EvidenceSourceFact) {
	t.Helper()
	packet, err := f.retained.Capture(context.Background(), evidence.Request{
		RunID: f.attempt.RunID, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(f.attempt.RunID)},
		IdempotencyKey: "retained-submission", SourceFacts: sourceFacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.packet = packet
	f.report.EvidenceRefs = []string{packet.ID}
}

func (f *retainedSubmissionFixture) reportSubmission(t *testing.T) *domain.Submission {
	t.Helper()
	if err := f.svc.ReconcileReport(context.Background(), f.attempt.RunID, f.report, f.packet); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.ListSubmissions(context.Background(), f.mission.ID, f.task.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("reported submissions = %v, %v", rows, err)
	}
	return rows[0]
}

func (f *retainedSubmissionFixture) proposeHistorical(t *testing.T, ref domain.SubmissionRef, facts []domain.SubmissionEvidence) *domain.Submission {
	t.Helper()
	submission, err := f.db.SubmitAttempt(context.Background(), f.attempt.ID, f.attempt.AuthorityGeneration, f.attempt.IntegratorGeneration, ref, facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func (f *retainedSubmissionFixture) ref() domain.SubmissionRef {
	return domain.SubmissionRef{WorkspaceID: f.mission.WorkspaceID, RunID: f.attempt.RunID, EvidenceRef: f.packet.ID, RetainedRevision: f.packet.RetainedRevision}
}

func (f *retainedSubmissionFixture) params(submission *domain.Submission) protocol.TaskAcceptSubmissionParams {
	return protocol.TaskAcceptSubmissionParams{
		SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		ExpectedAcceptedSetVersion: f.mission.AcceptedSetVersion, IdempotencyKey: "accept-retained",
		ScopeDisposition: "reviewed captured result.txt outside the declared scope",
	}
}

func (f *retainedSubmissionFixture) accept(t *testing.T, run domain.RunID, params protocol.TaskAcceptSubmissionParams) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return f.svc.HandleAgent(context.Background(), run, protocol.MethodTaskAcceptSubmission, raw)
}

func (f *retainedSubmissionFixture) show(t *testing.T) protocol.Submission {
	t.Helper()
	out, err := f.svc.Show(context.Background(), protocol.MissionShowParams{MissionID: string(f.mission.ID)})
	if err != nil || len(out.Submissions) != 1 {
		t.Fatalf("mission show = %#v, %v", out, err)
	}
	// Exercise the public serialization surface, not only the in-memory fact.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.MissionShowResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Submissions[0]
}

func requireTruncatedTranscript(t *testing.T, submission protocol.Submission) {
	t.Helper()
	for _, fact := range submission.Evidence {
		if fact.Kind == "transcript" {
			if !fact.Available || !fact.Truncated {
				t.Fatalf("visible retained transcript = %#v", fact)
			}
			return
		}
	}
	t.Fatal("visible transcript fact missing")
}

func TestCappedTranscriptCaptureReportAndAcceptance(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical=%t", historical), func(t *testing.T) {
			f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: "transcript"}}, submissionTranscript{size: evidence.MaxTranscriptBytes + 4096})
			f.capture(t, nil)
			var submission *domain.Submission
			if historical {
				facts := packetSubmissionEvidence(f.svc.cfg.Now, f.packet)
				for i := range facts {
					if facts[i].Kind == "transcript" {
						// Historical schema lacked Truncated and persisted false.
						// Detail is deliberately not a recognizable cap string.
						facts[i].Available, facts[i].Truncated, facts[i].Detail = false, false, "old observation"
					}
				}
				submission = f.proposeHistorical(t, f.ref(), facts)
				if replay := f.reportSubmission(t); replay.ID != submission.ID {
					t.Fatal("report replay replaced historical proposal")
				}
			} else {
				submission = f.reportSubmission(t)
				requireTruncatedTranscript(t, f.show(t))
			}
			params := f.params(submission)
			f.fs.readBytes = 0
			accepted, err := f.accept(t, f.mission.CurrentIntegratorRunID, params)
			if err != nil {
				t.Fatalf("accept bounded transcript: %v", err)
			}
			if f.fs.readBytes != evidence.MaxTranscriptBytes {
				t.Fatalf("acceptance read %d retained bytes, want %d", f.fs.readBytes, evidence.MaxTranscriptBytes)
			}
			visible := f.show(t)
			requireTruncatedTranscript(t, visible)
			if visible.State != string(domain.SubmissionAccepted) || visible.Acceptance == nil || visible.Acceptance.SubmissionID != string(submission.ID) {
				t.Fatalf("accepted submission = %#v", visible)
			}
			if visible.AttemptID != string(submission.AttemptID) || visible.CreatedAt != submission.CreatedAt.UTC().Format(time.RFC3339Nano) || visible.ProposedByRunID != string(submission.ProposedByRunID) {
				t.Fatal("evidence refresh rewrote submission provenance")
			}
			reader, err := f.retained.OpenTranscript(context.Background(), f.mission.WorkspaceID, f.packet.ID)
			if err != nil {
				t.Fatal(err)
			}
			hash, expected := sha256.New(), sha256.New()
			n, readErr := io.Copy(hash, reader)
			closeErr := reader.Close()
			_, _ = io.Copy(expected, io.LimitReader(submissionBytes{}, evidence.MaxTranscriptBytes))
			if readErr != nil || closeErr != nil || n != evidence.MaxTranscriptBytes || !bytes.Equal(hash.Sum(nil), expected.Sum(nil)) {
				t.Fatalf("retained bytes changed: size=%d read=%v close=%v", n, readErr, closeErr)
			}
			f.clock = f.clock.Add(evidence.DefaultRetention + time.Second)
			if _, cleanupErr := f.retained.CleanupExpired(context.Background()); cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			if _, expiredErr := f.retained.OpenTranscript(context.Background(), f.mission.WorkspaceID, f.packet.ID); !errors.Is(expiredErr, evidence.ErrExpired) {
				t.Fatalf("expired source read = %v", expiredErr)
			}
			f.fs.openErr = errors.New("accepted replay must not reopen evidence")
			replayed, err := f.accept(t, f.mission.CurrentIntegratorRunID, params)
			if err != nil || !reflect.DeepEqual(accepted, replayed) || !reflect.DeepEqual(visible, f.show(t)) {
				t.Fatalf("immutable receipt replay = %#v, %v", replayed, err)
			}
			stale := params
			stale.ExpectedIntegratorGeneration++
			if _, staleErr := f.accept(t, f.mission.CurrentIntegratorRunID, stale); !errors.Is(staleErr, store.ErrMissionStale) {
				t.Fatalf("stale-authority receipt replay = %v", staleErr)
			}
			member, err := f.db.GetMember(context.Background(), f.mission.Integrator.AccountMemberID)
			if err != nil {
				t.Fatal(err)
			}
			member.Pending = true
			if err := f.db.UpdateMember(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			if _, err := f.accept(t, f.mission.CurrentIntegratorRunID, params); !errors.Is(err, permissions.ErrDenied) {
				t.Fatalf("revoked-authority receipt replay = %v", err)
			}
		})
	}
}

func TestSubmissionAcceptanceRejectsDamagedCappedTranscript(t *testing.T) {
	for _, size := range []int64{0, 4096, evidence.MaxTranscriptBytes - 1, evidence.MaxTranscriptBytes + 1} {
		t.Run(fmt.Sprintf("retained-bytes=%d", size), func(t *testing.T) {
			f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: "transcript"}}, submissionTranscript{size: evidence.MaxTranscriptBytes + 4096})
			f.capture(t, nil)
			submission := f.reportSubmission(t)
			if truncateErr := os.Truncate(f.fs.transcript, size); truncateErr != nil {
				t.Fatal(truncateErr)
			}
			if _, acceptErr := f.accept(t, f.mission.CurrentIntegratorRunID, f.params(submission)); !errors.Is(acceptErr, store.ErrMissionNotReady) {
				t.Fatalf("accept damaged capped transcript = %v, want not ready", acceptErr)
			}
			after, submissionErr := f.db.GetSubmission(context.Background(), submission.ID)
			if submissionErr != nil || !reflect.DeepEqual(submission, after) {
				t.Fatalf("damaged source acceptance mutated proposal: %#v, %v", after, submissionErr)
			}
			current, missionErr := f.db.GetMission(context.Background(), f.mission.ID)
			if missionErr != nil || current.AcceptedSetVersion != f.mission.AcceptedSetVersion {
				t.Fatalf("damaged source acceptance advanced accepted set: %#v, %v", current, missionErr)
			}
		})
	}
}

func TestSubmissionAcceptanceRefusesUnavailableRetainedTranscript(t *testing.T) {
	for _, scenario := range []string{"absent-fact", "unavailable-at-capture", "missing-packet", "missing-after-capture", "open-error", "read-error", "expired", "wrong-revision", "wrong-run", "wrong-origin", "wrong-workspace"} {
		t.Run(scenario, func(t *testing.T) {
			exporter := submissionTranscript{size: 4096}
			if scenario == "unavailable-at-capture" {
				exporter.err = errors.New("capture source unavailable")
			}
			f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: "transcript"}}, exporter)
			var sources []store.EvidenceSourceFact
			if scenario == "absent-fact" {
				for i := range evidence.MaxSourceFacts {
					sources = append(sources, store.EvidenceSourceFact{Name: fmt.Sprintf("observed-%d", i), Available: true})
				}
			}
			f.capture(t, sources)
			var submission *domain.Submission
			if strings.HasPrefix(scenario, "wrong-") {
				ref := f.ref()
				if scenario == "wrong-revision" {
					ref.RetainedRevision = strings.Repeat("0", 40)
				} else {
					packet, err := f.db.GetEvidencePacket(context.Background(), f.packet.ID)
					if err != nil {
						t.Fatal(err)
					}
					packet.ID, packet.IdempotencyKey = "", "other-identity"
					switch scenario {
					case "wrong-workspace":
						packet.WorkspaceID = regressionWorkspace(t, f.db).ID
					case "wrong-run":
						packet.RunID = f.mission.CurrentIntegratorRunID
						packet.Origin.ID = string(packet.RunID)
					case "wrong-origin":
						packet.Origin = store.EvidenceOrigin{Kind: store.EvidenceOriginHuman, ID: string(f.mission.AccountableHumanID)}
					}
					if err := f.db.CreateEvidencePacket(context.Background(), packet); err != nil {
						t.Fatal(err)
					}
					ref.EvidenceRef = packet.ID
				}
				facts := []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: ref.EvidenceRef, Available: true}, {Kind: "transcript", Ref: ref.EvidenceRef, Available: true}}
				submission = f.proposeHistorical(t, ref, facts)
			} else {
				submission = f.reportSubmission(t)
			}
			switch scenario {
			case "missing-packet":
				if err := f.retained.PurgeRun(context.Background(), f.mission.WorkspaceID, f.attempt.RunID, nil); err != nil {
					t.Fatal(err)
				}
			case "missing-after-capture":
				if err := os.Remove(f.fs.transcript); err != nil {
					t.Fatal(err)
				}
			case "open-error":
				f.fs.openErr = os.ErrPermission
			case "read-error":
				f.fs.readErr = io.ErrUnexpectedEOF
			case "expired":
				f.clock = f.clock.Add(evidence.DefaultRetention + time.Second)
			}
			if _, err := f.accept(t, f.mission.CurrentIntegratorRunID, f.params(submission)); !errors.Is(err, store.ErrMissionNotReady) {
				t.Fatalf("accept %s = %v, want not ready", scenario, err)
			}
			after, err := f.db.GetSubmission(context.Background(), submission.ID)
			if err != nil || !reflect.DeepEqual(submission, after) {
				t.Fatalf("refusal mutated historical evidence: %#v, %v", after, err)
			}
		})
	}
}

func TestSubmissionAcceptanceDoesNotRequireOptionalTranscript(t *testing.T) {
	f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: "git"}}, submissionTranscript{size: 1024})
	f.capture(t, nil)
	submission := f.reportSubmission(t)
	if err := os.Remove(f.fs.transcript); err != nil {
		t.Fatal(err)
	}
	if _, err := f.accept(t, f.mission.CurrentIntegratorRunID, f.params(submission)); err != nil {
		t.Fatalf("optional transcript blocked acceptance: %v", err)
	}
	for _, fact := range f.show(t).Evidence {
		if fact.Kind == "transcript" && fact.Available {
			t.Fatal("missing optional transcript was reclassified available")
		}
	}
}

func TestSubmissionAcceptanceRetainsAuthorityAndAcceptedSetFences(t *testing.T) {
	for _, scenario := range []string{"worker", "generation", "accepted-set", "replaced-integrator", "revoked-account"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: "transcript"}}, submissionTranscript{size: 1024})
			f.capture(t, nil)
			submission := f.reportSubmission(t)
			params := f.params(submission)
			run := f.mission.CurrentIntegratorRunID
			want := store.ErrMissionStale
			switch scenario {
			case "worker":
				run, want = f.attempt.RunID, permissions.ErrDenied
			case "generation":
				params.ExpectedIntegratorGeneration++
			case "accepted-set":
				params.ExpectedAcceptedSetVersion++
			case "replaced-integrator":
				if _, err := f.db.ReplaceIntegrator(context.Background(), f.mission.ID, f.mission.IntegratorGeneration, f.mission.Integrator, f.mission.AccountableHumanID, f.mission.Integrator.AccountMemberID, "replacement"); err != nil {
					t.Fatal(err)
				}
			case "revoked-account":
				member, err := f.db.GetMember(context.Background(), f.mission.Integrator.AccountMemberID)
				if err != nil {
					t.Fatal(err)
				}
				member.Pending = true
				if err := f.db.UpdateMember(context.Background(), member); err != nil {
					t.Fatal(err)
				}
				want = permissions.ErrDenied
			}
			if _, err := f.accept(t, run, params); !errors.Is(err, want) {
				t.Fatalf("accept %s = %v, want %v", scenario, err, want)
			}
			after, err := f.db.GetSubmission(context.Background(), submission.ID)
			if err != nil || !reflect.DeepEqual(submission, after) {
				t.Fatalf("stale acceptance changed proposal: %#v, %v", after, err)
			}
		})
	}
}

func TestSubmissionInputRefsCannotLaunderSourceKinds(t *testing.T) {
	for _, scenario := range []string{"input-is-not-transcript", "forged-transcript-kind", "required-input", "missing-input", "expired-input"} {
		t.Run(scenario, func(t *testing.T) {
			required := "transcript"
			if scenario == "required-input" || scenario == "missing-input" || scenario == "expired-input" {
				required = "input"
			}
			exporter := &submissionTranscript{err: errors.New("main transcript unavailable")}
			f := newRetainedSubmissionFixture(t, []domain.EvidenceRequirement{{Kind: required}}, exporter)
			f.capture(t, nil)
			exporter.err, exporter.size = nil, 4096
			if scenario == "expired-input" {
				// Keep the primary fresh while this input expires just after
				// proposal, before acceptance revalidates packet presence.
				f.clock = f.clock.Add(-evidence.DefaultRetention + time.Second)
			}
			alternate, err := f.retained.Capture(context.Background(), evidence.Request{
				RunID: f.attempt.RunID, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(f.attempt.RunID)},
				IdempotencyKey: "alternate-input",
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired-input" {
				f.clock = f.clock.Add(evidence.DefaultRetention - time.Second)
			}
			ref := alternate.ID
			if scenario == "missing-input" {
				ref = "missing-retained-packet"
			}
			f.report.InputEvidenceRefs = []string{ref}
			var submission *domain.Submission
			if scenario == "forged-transcript-kind" {
				facts := packetSubmissionEvidence(f.svc.cfg.Now, f.packet)
				facts = append(facts, domain.SubmissionEvidence{Kind: "transcript", Ref: ref, Available: true})
				submission = f.proposeHistorical(t, f.ref(), facts)
			} else {
				submission = f.reportSubmission(t)
			}
			if scenario == "expired-input" {
				f.clock = f.clock.Add(2 * time.Second)
			}
			f.fs.transcriptOpens, f.fs.readBytes = 0, 0
			f.fs.openErr = errors.New("input-only payload must not be opened")
			_, err = f.accept(t, f.mission.CurrentIntegratorRunID, f.params(submission))
			if f.fs.transcriptOpens != 0 || f.fs.readBytes != 0 {
				t.Fatalf("input-only validation opened %d transcripts and read %d bytes", f.fs.transcriptOpens, f.fs.readBytes)
			}
			if scenario != "required-input" {
				if !errors.Is(err, store.ErrMissionNotReady) {
					t.Fatalf("alternate input accepted as %s: %v", required, err)
				}
				after, submissionErr := f.db.GetSubmission(context.Background(), submission.ID)
				if submissionErr != nil || !reflect.DeepEqual(submission, after) {
					t.Fatalf("unavailable input mutated proposal: %#v, %v", after, submissionErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readable input packet was not accepted: %v", err)
			}
			visible := f.show(t)
			found := false
			for _, fact := range visible.Evidence {
				if fact.Ref == alternate.ID {
					if fact.Kind != "input" || !fact.Available {
						t.Fatalf("alternate reference changed type or availability: %#v", fact)
					}
					found = true
				}
				if fact.Kind == "transcript" && fact.Available {
					t.Fatal("alternate input laundered unavailable main transcript")
				}
			}
			if !found {
				t.Fatal("accepted input provenance missing")
			}
		})
	}
}
