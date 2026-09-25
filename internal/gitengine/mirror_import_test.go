package gitengine

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
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
	observed, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || observed.Status != domain.MirrorStatusPending || observed.CandidateCommit != commit || observed.AcceptedCommit != "" || observed.BaseCommit != "" {
		t.Fatalf("first observation = %+v, %v", observed, err)
	}
	if _, err := e.WorkspaceBranchCommit(t.Context(), ws, "main"); err == nil {
		t.Fatal("first observation silently accepted the base")
	}
	adopted, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation)
	if err != nil || adopted.AcceptedCommit != commit || adopted.BaseCommit != commit || adopted.Status != domain.MirrorStatusReady {
		t.Fatalf("adoption = %+v, %v", adopted, err)
	}
	// Repeating initialization/configuration cannot reset an accepted ref.
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
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
	if _, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation); err != nil {
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
	seed, base := mirrorImportSource(t, e, false)
	mirrorImportGit(t, e, seed, "-c", "user.name=Seed", "-c", "user.email=seed@example.test", "commit", "--allow-empty", "-m", "unrelated seeded base")
	base = mirrorImportGit(t, e, seed, "rev-parse", "HEAD")
	const ws domain.WorkspaceID = "seeded-import"
	repo, err := e.InitWorkspaceRepo(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	mirrorImportGit(t, e, repo, "fetch", seed, "refs/heads/main:refs/heads/main")
	req := MirrorRequest{SourceURL: source, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic}
	if _, err := e.ConfigureWorkspaceMirror(t.Context(), ws, req); err != nil {
		t.Fatal(err)
	}
	observed, err := e.RefreshWorkspaceMirror(t.Context(), ws, req)
	if err != nil || observed.BaseCommit != base || observed.CandidateCommit != candidate || observed.AcceptedCommit != "" {
		t.Fatalf("seeded base overwritten: %+v, %v", observed, err)
	}
	if _, err := e.AdoptWorkspaceMirror(t.Context(), ws, req.Generation); err != nil {
		t.Fatal(err)
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
			if _, err := e.AdoptWorkspaceMirror(t.Context(), "missing-source", req.Generation); !errors.As(err, &typed) || typed.Kind != MirrorErrorNoCandidate {
				t.Fatalf("missing source created adoptable candidate: %v", err)
			}
		})
	}
}
