//go:build integration

package gitengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestCandidateGitAssemblyVerificationAndReceipt(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	ws := domain.WorkspaceID("candidate-git")
	seedWorkspace(t, e, url, ws)
	base := bareRevParse(t, e, ws, "refs/heads/main")

	source := t.TempDir()
	gitc(t, source, "clone", url(ws), "src")
	source = filepath.Join(source, "src")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, source, "commit", "-am", "candidate change")
	revision := gitc(t, source, "rev-parse", "HEAD")
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	packetRef := evidenceRef("packet-1")
	if _, err := e.git(ctxForTest(t), repo, "fetch", "--quiet", source, "HEAD:"+packetRef); err != nil {
		t.Fatal(err)
	}
	if err := e.RetainCandidateInput(ctxForTest(t), ws, "candidate-1", 0, "packet-1", revision, base); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CandidateCheckout(ctxForTest(t), ws, "candidate-1", base); err != nil {
		t.Fatal(err)
	}
	assembly, err := e.AssembleCandidate(ctxForTest(t), ws, "candidate-1", []CandidateRevisionInput{{Revision: revision, Base: base}})
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Revision == "" || assembly.AppliedInputs != 1 || len(assembly.Conflicts) != 0 {
		t.Fatalf("assembly = %+v", assembly)
	}
	again, err := e.AssembleCandidate(ctxForTest(t), ws, "candidate-1", []CandidateRevisionInput{{Revision: revision, Base: base}})
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != assembly.Revision || again.AppliedInputs != assembly.AppliedInputs || !slices.Equal(again.Conflicts, assembly.Conflicts) {
		t.Fatalf("frozen assembly = %+v, want %+v", again, assembly)
	}
	checkout, err := e.candidatePath("candidate-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(checkout, "file.txt"))
	if err != nil || string(data) != "candidate\n" {
		t.Fatalf("candidate content = %q, %v", data, err)
	}

	verification, err := e.CandidateVerificationCheckout(ctxForTest(t), ws, "candidate-1", "verification-1", assembly.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verification, "file.txt"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckCandidateVerification(ctxForTest(t), ws, "candidate-1", "verification-1", assembly.Revision); err == nil {
		t.Fatal("tracked mutation was not detected")
	}
	if err := e.RemoveCandidateVerification(ctxForTest(t), ws, "candidate-1", "verification-1"); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.DeliverCandidate(ctxForTest(t), ws, "candidate-1", "request-1", CandidateDeliveryUpdateRef, "refs/heads/main", base, assembly.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Result != "landed" || receipt.Revision != assembly.Revision || receipt.PreviousRevision != base {
		t.Fatalf("receipt = %+v", receipt)
	}
	retry, err := e.DeliverCandidate(ctxForTest(t), ws, "candidate-1", "request-1", CandidateDeliveryUpdateRef, "refs/heads/main", base, assembly.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DeliverCandidate(ctxForTest(t), ws, "candidate-1", "request-cas", CandidateDeliveryUpdateRef, "refs/heads/main", base, assembly.Revision); err == nil {
		t.Fatal("stale target CAS unexpectedly succeeded")
	}
	proposal, err := e.DeliverCandidate(ctxForTest(t), ws, "candidate-1", "request-proposal", CandidateDeliveryProposal, "refs/heads/main", assembly.Revision, assembly.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Result != "proposed" || proposal.ProposalRef == "" {
		t.Fatalf("proposal receipt = %+v", proposal)
	}
	if _, err := e.DeliverCandidate(ctxForTest(t), ws, "candidate-1", "request-1", CandidateDeliveryProposal, "refs/heads/main", assembly.Revision, assembly.Revision); !errors.Is(err, ErrCandidateReceiptConflict) {
		t.Fatalf("receipt mismatch err = %v", err)
	}
	if retry != receipt {
		t.Fatalf("retry receipt = %+v, want %+v", retry, receipt)
	}
}

func TestCandidateGitPartialConflictResolution(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	ws := domain.WorkspaceID("candidate-partial-conflicts")
	seedWorkspace(t, e, url, ws)
	base := bareRevParse(t, e, ws, "refs/heads/main")
	source := t.TempDir()
	gitc(t, source, "clone", url(ws), "src")
	source = filepath.Join(source, "src")
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	const candidateID = "partial-conflicts"
	var inputs []CandidateRevisionInput
	for i, side := range []string{"left", "right"} {
		gitc(t, source, "reset", "--hard", base)
		for _, path := range []string{"file.txt", "other.txt"} {
			if err := os.WriteFile(filepath.Join(source, path), []byte(side+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitc(t, source, "add", "--", "file.txt", "other.txt")
		gitc(t, source, "commit", "-m", side)
		revision := gitc(t, source, "rev-parse", "HEAD")
		key := "partial-" + side
		if _, err := e.git(t.Context(), repo, "fetch", "--quiet", source, "HEAD:"+evidenceRef(key)); err != nil {
			t.Fatal(err)
		}
		if err := e.RetainCandidateInput(t.Context(), ws, candidateID, i, key, revision, base); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, CandidateRevisionInput{Revision: revision, Base: base})
	}
	checkout, err := e.CandidateCheckout(t.Context(), ws, candidateID, base)
	if err != nil {
		t.Fatal(err)
	}
	assembly, err := e.AssembleCandidate(t.Context(), ws, candidateID, inputs)
	if !errors.Is(err, ErrCandidateConflict) || !slices.Equal(assembly.Conflicts, []string{"file.txt", "other.txt"}) {
		t.Fatalf("initial conflicts = %+v, %v", assembly, err)
	}
	untouched, err := os.ReadFile(filepath.Join(checkout, "other.txt"))
	if err != nil {
		t.Fatal(err)
	}
	stages := gitc(t, checkout, "ls-files", "-u", "--", "other.txt")
	const firstResolution = "first resolved: =======\n"
	partial, err := e.ResolveCandidate(t.Context(), ws, candidateID, []CandidateResolution{{Path: "file.txt", Content: firstResolution}})
	if !errors.Is(err, ErrCandidateConflict) || !slices.Equal(partial.Conflicts, []string{"other.txt"}) {
		t.Fatalf("remaining conflicts = %+v, %v", partial, err)
	}
	after, err := os.ReadFile(filepath.Join(checkout, "other.txt"))
	if err != nil || string(after) != string(untouched) {
		t.Fatalf("unsubmitted conflict changed: %q, %v", after, err)
	}
	if got := gitc(t, checkout, "ls-files", "-u", "--", "other.txt"); got != stages {
		t.Fatalf("unsubmitted conflict stages changed: %q, want %q", got, stages)
	}
	replayed, err := e.AssembleCandidate(t.Context(), ws, candidateID, inputs)
	if !errors.Is(err, ErrCandidateConflict) || !slices.Equal(replayed.Conflicts, []string{"other.txt"}) {
		t.Fatalf("persisted remaining conflicts = %+v, %v", replayed, err)
	}
	frozen, err := e.ResolveCandidate(t.Context(), ws, candidateID, []CandidateResolution{{Path: "other.txt", Content: "second resolved\n"}})
	if err != nil || len(frozen.Conflicts) != 0 || frozen.AppliedInputs != 2 {
		t.Fatalf("final resolution = %+v, %v", frozen, err)
	}
	for path, want := range map[string]string{"file.txt": firstResolution, "other.txt": "second resolved\n"} {
		got, err := os.ReadFile(filepath.Join(checkout, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if got := bareRevParse(t, e, ws, "refs/heads/main"); got != base {
		t.Fatalf("resolution changed target to %s, want %s", got, base)
	}
}

func TestCandidateGitJournalCrashRecovery(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	ws := domain.WorkspaceID("candidate-journal")
	seedWorkspace(t, e, url, ws)
	base := bareRevParse(t, e, ws, "refs/heads/main")
	source := t.TempDir()
	gitc(t, source, "clone", url(ws), "src")
	source = filepath.Join(source, "src")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("journal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, source, "commit", "-am", "journal change")
	revision := gitc(t, source, "rev-parse", "HEAD")
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"candidate-partial", "candidate-ref-moved"} {
		packetRef := evidenceRef("packet-" + id)
		if _, err := e.git(ctxForTest(t), repo, "fetch", "--quiet", source, "HEAD:"+packetRef); err != nil {
			t.Fatal(err)
		}
		if err := e.RetainCandidateInput(ctxForTest(t), ws, id, 0, "packet-"+id, revision, base); err != nil {
			t.Fatal(err)
		}
	}

	partialCheckout, err := e.CandidateCheckout(ctxForTest(t), ws, "candidate-partial", base)
	if err != nil {
		t.Fatal(err)
	}
	partialMeta, err := e.loadCandidateMeta("candidate-partial")
	if err != nil {
		t.Fatal(err)
	}
	partialMeta.Inputs = []CandidateRevisionInput{{Revision: revision, Base: base}}
	if err := e.saveCandidateMeta("candidate-partial", partialMeta); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctxForTest(t), partialCheckout, "cherry-pick", "--no-commit", "--no-edit", revision); err != nil {
		t.Fatal(err)
	}
	partial, err := e.AssembleCandidate(ctxForTest(t), ws, "candidate-partial", partialMeta.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if partial.AppliedInputs != 1 || partial.Revision == base {
		t.Fatalf("partial replay = %+v", partial)
	}

	movedCheckout, err := e.CandidateCheckout(ctxForTest(t), ws, "candidate-ref-moved", base)
	if err != nil {
		t.Fatal(err)
	}
	movedMeta, err := e.loadCandidateMeta("candidate-ref-moved")
	if err != nil {
		t.Fatal(err)
	}
	movedMeta.Inputs = []CandidateRevisionInput{{Revision: revision, Base: base}}
	if err := e.saveCandidateMeta("candidate-ref-moved", movedMeta); err != nil {
		t.Fatal(err)
	}
	if _, err := e.git(ctxForTest(t), movedCheckout, "cherry-pick", "--no-commit", "--no-edit", revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.candidateCommit(ctxForTest(t), movedCheckout, repo, base, revision); err != nil {
		t.Fatal(err)
	}
	moved, err := e.AssembleCandidate(ctxForTest(t), ws, "candidate-ref-moved", movedMeta.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if moved.AppliedInputs != 1 || moved.Revision == base {
		t.Fatalf("ref-moved replay = %+v", moved)
	}
}

func TestRemoveCandidateAfterWorkspaceRemoval(t *testing.T) {
	e := newTestEngine(t, nil)
	ws := domain.WorkspaceID("candidate-expired")
	id := "expired-candidate"
	checkout := filepath.Join(e.cfg.CheckoutsDir, "candidates", id)
	verificationRoot := filepath.Join(e.cfg.CheckoutsDir, "verifications", id)
	scratchRoot := filepath.Join(e.cfg.CheckoutsDir, "verification-git", id)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(verificationRoot, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scratchRoot, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "orphan.txt"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout+".json"), []byte(`{"candidate_id":"expired-candidate"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verificationRoot, "orphan", "verification.json"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratchRoot, "orphan", "index"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := e.repoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveCandidate(ctxForTest(t), ws, id); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{checkout, checkout + ".json", verificationRoot, scratchRoot} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan path %s remains: %v", p, err)
		}
	}
}

func ctxForTest(t *testing.T) context.Context { t.Helper(); return t.Context() }
