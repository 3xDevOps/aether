package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/secretstore"
)

func TestLaunchPutsWorkspaceFilesInTheCheckoutAndOutOfGit(t *testing.T) {
	t.Parallel()
	e, seed := realGitEnv(t)
	bare := seed(e.ws.ID)
	e.sched.cfg.Secrets = secretstore.New(t.TempDir())
	files := map[string]string{
		".env":                "API_URL=${HOST}/api # kept as written\n",
		"apps/web/.env.local": "NEXT_PUBLIC_A=1\n",
		"keys/[env].json":     "{}\n",
	}
	if err := e.sched.cfg.Secrets.PutFiles(e.ws.ID, files); err != nil {
		t.Fatal(err)
	}

	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "use the env", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	for name, want := range files {
		path := filepath.Join(run.Worktree, filepath.FromSlash(name))
		got, readErr := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if readErr != nil || statErr != nil || string(got) != want || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s in the checkout = %q (%v, %v), want %q with mode 0600", name, got, readErr, info, want)
		}
	}
	if status := runGit(t, run.Worktree, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("git status in the checkout = %q, want the workspace files excluded", status)
	}
	if _, err = e.sched.cfg.Git.(*gitengine.Engine).FilesRead(t.Context(), e.ws.ID, run.ID, "", ".env", 0); !errors.Is(err, gitengine.ErrWorkspaceFile) {
		t.Fatalf("FilesRead .env = %v, want ErrWorkspaceFile", err)
	}

	if err = os.WriteFile(filepath.Join(run.Worktree, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if published := runGit(t, bare, "ls-tree", "-r", "--name-only", run.Branch); published != "README.md\nfeature.txt" {
		t.Fatalf("published branch holds %q, want the change and none of the workspace files", published)
	}
}

func TestLaunchRefusesAWorkspaceFileTheRepositoryTracks(t *testing.T) {
	t.Parallel()
	e, seed := realGitEnv(t)
	seed(e.ws.ID)
	e.sched.cfg.Secrets = secretstore.New(t.TempDir())
	if err := e.sched.cfg.Secrets.PutFiles(e.ws.ID, map[string]string{".env": "A=1\n", "README.md": "replaced\n"}); err != nil {
		t.Fatal(err)
	}

	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "use the env", "fake", domain.LaunchTUI)
	want := "workspace file README.md is tracked in the repository; remove it from the repository, or run aether workspace env file rm --workspace " + string(e.ws.ID) + " README.md"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Launch = %v, want it refused with %q", err, want)
	}
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	if len(e.rt.containers) != 0 {
		t.Fatalf("containers created for the refused launch: %v", e.rt.containers)
	}
}

func TestLaunchRefusesAWorkspaceFileTheRepositoryUnignores(t *testing.T) {
	t.Parallel()
	e, seed := realGitEnv(t)
	bare := seed(e.ws.ID)
	work := t.TempDir()
	runGit(t, work, "clone", "-q", bare, ".")
	if err := os.WriteFile(filepath.Join(work, ".gitignore"), []byte("!.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".gitignore")
	runGit(t, work, "commit", "-q", "-m", "keep .env visible")
	runGit(t, work, "push", "-q", "origin", "HEAD:main")
	head := runGit(t, work, "rev-parse", "HEAD")
	e.sched.cfg.Bases = baseCaptureFunc(func(_ context.Context, ws domain.WorkspaceID, _ string) (mirror.CaptureResult, error) {
		return mirror.CaptureResult{WorkspaceID: ws, Commit: head, Branch: "main", CheckedAt: time.Now()}, nil
	})
	e.sched.cfg.Secrets = secretstore.New(t.TempDir())
	if err := e.sched.cfg.Secrets.PutFiles(e.ws.ID, map[string]string{".env": "SECRET=1\n"}); err != nil {
		t.Fatal(err)
	}

	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "use the env", "fake", domain.LaunchTUI)
	want := "workspace file .env would not be ignored by git: the repository's ignore rules un-ignore it; remove the negation or rename the file"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Launch = %v, want it refused with %q", err, want)
	}
	run, err := e.db.GetRun(t.Context(), domain.RunID(strings.Fields(err.Error())[1]))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(run.Worktree, ".env")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf(".env in the refused run's checkout: %v, want it never written", statErr)
	}
}
