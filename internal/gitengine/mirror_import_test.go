package gitengine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func mirrorImportGit(t *testing.T, e *Engine, repo string, args ...string) string {
	t.Helper()
	out, err := e.git(t.Context(), repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mirrorImportSource(t *testing.T, e *Engine, commit bool) (string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	mirrorImportGit(t, e, "", "init", "--initial-branch=main", source)
	if !commit {
		return source, ""
	}
	mirrorImportGit(t, e, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "initial source")
	return source, mirrorImportGit(t, e, source, "rev-parse", "HEAD")
}

func mirrorImportTransport(ctx context.Context, repo string, req MirrorRequest, incoming string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--", req.SourceURL, "+refs/heads/"+req.Branch+":"+incoming)
	cmd.Env = gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return &mirrorFetchFailure{err: err, output: string(output)}
	}
	return nil
}

func TestMirrorImportInitializesAndRequiresInitialAdoption(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = mirrorImportTransport
	source, commit := mirrorImportSource(t, e, true)
	const ws domain.WorkspaceID = "fresh-import"
	if generation, err := e.MirrorGeneration(t.Context(), ws); err != nil || generation != 0 {
		t.Fatalf("uninitialized generation = %d, %v", generation, err)
	}
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	observed, refreshErr := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if refreshErr != nil || observed.Status != domain.MirrorStatusPending || observed.CandidateCommit != commit || observed.AcceptedCommit != "" || observed.BaseCommit != "" {
		t.Fatalf("first observation = %+v, %v", observed, refreshErr)
	}
	if _, branchErr := e.WorkspaceBranchCommit(t.Context(), ws, "main"); branchErr == nil {
		t.Fatal("first observation silently accepted the base")
	}
	adopted, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, commit)
	if err != nil || adopted.AcceptedCommit != commit || adopted.BaseCommit != commit || adopted.Status != domain.MirrorStatusReady {
		t.Fatalf("adoption = %+v, %v", adopted, err)
	}
	// Repeating initialization/configuration cannot reset an accepted ref.
	if _, configErr := e.ConfigureWorkspaceMirror(t.Context(), ws, req); configErr != nil {
		t.Fatal(configErr)
	}
	again, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || again.AcceptedCommit != commit || again.BaseCommit != commit {
		t.Fatalf("duplicate configure lost accepted base: %+v, %v", again, err)
	}
}

func TestMirrorImportConcurrentInitializationPreservesRepository(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = mirrorImportTransport
	source, commit := mirrorImportSource(t, e, true)
	const ws domain.WorkspaceID = "concurrent-import"
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	start := make(chan struct{})
	errors := make(chan error, 16)
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if i%2 == 0 {
				_, err := e.InitWorkspaceRepo(t.Context(), ws)
				errors <- err
			} else {
				_, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req)
				errors <- err
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.RefreshWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, commit); err != nil {
		t.Fatal(err)
	}
	repo, err := e.InitWorkspaceRepo(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := mirrorImportGit(t, e, repo, "rev-parse", "refs/heads/main"); got != commit {
		t.Fatalf("reinitialized base = %s, want %s", got, commit)
	}
	if got := mirrorImportGit(t, e, repo, "config", "--get-all", "transfer.hideRefs"); got != "refs/aether" {
		t.Fatalf("concurrent initialization duplicated or lost ref protection: %q", got)
	}
}

func TestMirrorImportPreservesSeededBaseAndRejectsRewrite(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = mirrorImportTransport
	source, candidate := mirrorImportSource(t, e, true)
	seed, _ := mirrorImportSource(t, e, false)
	mirrorImportGit(t, e, seed, "-c", "user.name=Seed", "-c", "user.email=seed@example.test", "commit", "--allow-empty", "-m", "unrelated seeded base")
	base := mirrorImportGit(t, e, seed, "rev-parse", "HEAD")
	const ws domain.WorkspaceID = "seeded-import"
	repo, initErr := e.InitWorkspaceRepo(t.Context(), ws)
	if initErr != nil {
		t.Fatal(initErr)
	}
	mirrorImportGit(t, e, repo, "fetch", seed, "refs/heads/main:refs/heads/main")
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, configErr := e.ConfigureWorkspaceMirror(t.Context(), ws, req); configErr != nil {
		t.Fatal(configErr)
	}
	observed, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || observed.BaseCommit != base || observed.CandidateCommit != candidate || observed.AcceptedCommit != "" {
		t.Fatalf("seeded base overwritten: %+v, %v", observed, err)
	}
	if _, adoptErr := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, candidate); adoptErr != nil {
		t.Fatal(adoptErr)
	}
	mirrorImportGit(t, e, source, "fetch", seed, "refs/heads/main")
	mirrorImportGit(t, e, source, "update-ref", "refs/heads/main", base)
	rewritten, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	var typed *MirrorError
	if !errors.As(err, &typed) || typed.Kind != MirrorErrorRewritten || rewritten.AcceptedCommit != candidate || rewritten.BaseCommit != candidate || rewritten.CandidateCommit != base {
		t.Fatalf("rewrite silently adopted: %+v, %v", rewritten, err)
	}
}

func TestMirrorImportMissingSourceBranch(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "missing-branch"
		if empty {
			name = "empty-source"
		}
		t.Run(name, func(t *testing.T) {
			e := newUnitEngine(t)
			e.cfg.MirrorFetch = mirrorImportTransport
			source, _ := mirrorImportSource(t, e, !empty)
			branch := "missing"
			if empty {
				branch = "main"
			}
			req := MirrorRequest{SourceURL: source, Branch: branch, Generation: 1, Auth: domain.MirrorAuthPublic}
			if _, err := e.ConfigureWorkspaceMirror(t.Context(), "missing-source", req); err != nil {
				t.Fatal(err)
			}
			result, err := e.RefreshWorkspaceMirror(t.Context(), "missing-source", req)
			var typed *MirrorError
			if !errors.As(err, &typed) || typed.Kind != MirrorErrorSourceMissing || typed.Branch != branch || result.Status != domain.MirrorStatusSourceMissing || result.BaseCommit != "" || result.CandidateCommit != "" {
				t.Fatalf("source failure = %+v, %v", result, err)
			}
			if _, err := e.AdoptWorkspaceMirror(t.Context(), "missing-source", req.Generation, strings.Repeat("a", 40)); !errors.As(err, &typed) || typed.Kind != MirrorErrorNoCandidate {
				t.Fatalf("missing source created adoptable candidate: %v", err)
			}
		})
	}
}

func TestMirrorAdoptionRequiresReviewedCandidate(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = mirrorImportTransport
	source, reviewed := mirrorImportSource(t, e, true)
	const ws domain.WorkspaceID = "reviewed-import"
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	first, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || first.CandidateCommit != reviewed || first.Status != domain.MirrorStatusPending {
		t.Fatalf("reviewed candidate = %+v, %v", first, err)
	}
	mirrorImportGit(t, e, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "new candidate")
	current := mirrorImportGit(t, e, source, "rev-parse", "HEAD")
	second, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || second.Generation != first.Generation || second.CandidateCommit != current || current == reviewed || second.AcceptedCommit != "" || second.BaseCommit != "" {
		t.Fatalf("new candidate = %+v, %v", second, err)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRef, candidateRef := mirrorRefs(req.Generation)
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		for ref, want := range map[string]string{"refs/heads/main": "", acceptedRef: "", candidateRef: current} {
			if got := readRefBestEffort(t.Context(), e, repo, ref); got != want {
				t.Fatalf("ref %s = %q, want %q", ref, got, want)
			}
		}
	}
	for _, tc := range []struct {
		name     string
		expected string
		kind     MirrorErrorKind
	}{
		{name: "stale review", expected: reviewed, kind: MirrorErrorCASConflict},
		{name: "missing", expected: "", kind: MirrorErrorInvalidRequest},
		{name: "abbreviated", expected: current[:12], kind: MirrorErrorInvalidRequest},
		{name: "revspec", expected: "HEAD", kind: MirrorErrorInvalidRequest},
		{name: "uppercase", expected: strings.Repeat("A", 40), kind: MirrorErrorInvalidRequest},
		{name: "nonhex", expected: strings.Repeat("g", 40), kind: MirrorErrorInvalidRequest},
		{name: "whitespace", expected: current + "\n", kind: MirrorErrorInvalidRequest},
		{name: "wrong length", expected: strings.Repeat("a", 41), kind: MirrorErrorInvalidRequest},
		{name: "complete sha256", expected: strings.Repeat("a", 64), kind: MirrorErrorCASConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, adoptErr := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, tc.expected)
			var failure *MirrorError
			if !errors.As(adoptErr, &failure) || failure.Kind != tc.kind || result.Status == domain.MirrorStatusReady || result.Changed {
				t.Fatalf("rejected adoption = %+v, %v", result, adoptErr)
			}
			if tc.kind == MirrorErrorCASConflict && (result.CandidateCommit != current || result.ObservedCommit != current || result.AcceptedCommit != "" || result.BaseCommit != "") {
				t.Fatalf("conflict omitted current candidate: %+v", result)
			}
			assertUnchanged(t)
		})
	}
	adopted, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, current)
	if err != nil || adopted.Status != domain.MirrorStatusReady || adopted.AcceptedCommit != current || adopted.BaseCommit != current || !adopted.Changed {
		t.Fatalf("current candidate adoption = %+v, %v", adopted, err)
	}
	for _, ref := range []string{"refs/heads/main", acceptedRef, candidateRef} {
		if got := readRefBestEffort(t.Context(), e, repo, ref); got != current {
			t.Fatalf("adopted ref %s = %q, want %q", ref, got, current)
		}
	}
}

func TestMirrorAdoptionRechecksReviewedCandidateAfterCASConflict(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = mirrorImportTransport
	source, reviewed := mirrorImportSource(t, e, true)
	const ws domain.WorkspaceID = "reviewed-retry"
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RefreshWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	mirrorImportGit(t, e, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "candidate during CAS")
	current := mirrorImportGit(t, e, source, "rev-parse", "HEAD")
	mirrorImportGit(t, e, repo, "fetch", source, "refs/heads/main")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	acceptedRef, candidateRef := mirrorRefs(req.Generation)
	wrapperDir := t.TempDir()
	wrapper, marker := filepath.Join(wrapperDir, "git-wrapper"), filepath.Join(wrapperDir, "moved")
	// Interpose a native ref writer after adoption reads A but before its
	// transaction verifies A. Git must reject that transaction atomically,
	// and the next attempt must reject B rather than adopting it.
	script := "#!/bin/sh\nset -e\n" +
		"case \"$*\" in *'update-ref --stdin'*)\n" +
		"if [ ! -e " + shellQuoteMirror(marker) + " ]; then\n" +
		shellQuoteMirror(realGit) + " -C " + shellQuoteMirror(repo) + " update-ref " + shellQuoteMirror(candidateRef) + " " + current + " " + reviewed + "\n" +
		": > " + shellQuoteMirror(marker) + "\nfi\n;; esac\n" +
		"exec " + shellQuoteMirror(realGit) + " \"$@\"\n"
	if writeErr := os.WriteFile(wrapper, []byte(script), 0o755); writeErr != nil {
		t.Fatal(writeErr)
	}
	e.cfg.GitPath = wrapper
	result, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, reviewed)
	var failure *MirrorError
	if !errors.As(err, &failure) || failure.Kind != MirrorErrorCASConflict || result.Status != domain.MirrorStatusError || result.Changed || result.CandidateCommit != current || result.AcceptedCommit != "" || result.BaseCommit != "" {
		t.Fatalf("retried stale adoption = %+v, %v", result, err)
	}
	for ref, want := range map[string]string{"refs/heads/main": "", acceptedRef: "", candidateRef: current} {
		if got := readRefBestEffort(t.Context(), e, repo, ref); got != want {
			t.Fatalf("ref after CAS conflict %s = %q, want %q", ref, got, want)
		}
	}
	adopted, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation, current)
	if err != nil || adopted.Status != domain.MirrorStatusReady || adopted.AcceptedCommit != current || adopted.BaseCommit != current {
		t.Fatalf("reviewed candidate after CAS conflict = %+v, %v", adopted, err)
	}
}
