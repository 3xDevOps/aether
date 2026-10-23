package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/mirror"
)

type baseCaptureFunc func(context.Context, domain.WorkspaceID, string) (mirror.CaptureResult, error)

func (f baseCaptureFunc) Capture(ctx context.Context, ws domain.WorkspaceID, cached string) (mirror.CaptureResult, error) {
	return f(ctx, ws, cached)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// realGitEnv is a scheduler over the real git engine, with the fake runtime.
func realGitEnv(t *testing.T) (*testEnv, func(domain.WorkspaceID) string) {
	t.Helper()
	root := t.TempDir()
	engine, err := gitengine.New(gitengine.Config{ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts")})
	if err != nil {
		t.Fatalf("gitengine.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	commits := map[domain.WorkspaceID]string{}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Git = engine
		cfg.ReposDir = filepath.Join(root, "repos")
		cfg.Bases = baseCaptureFunc(func(_ context.Context, ws domain.WorkspaceID, _ string) (mirror.CaptureResult, error) {
			return mirror.CaptureResult{WorkspaceID: ws, Commit: commits[ws], Branch: "main", CheckedAt: time.Now()}, nil
		})
	})
	seed := func(ws domain.WorkspaceID) string {
		t.Helper()
		bare, err := engine.InitWorkspaceRepo(t.Context(), ws)
		if err != nil {
			t.Fatalf("InitWorkspaceRepo: %v", err)
		}
		work := t.TempDir()
		runGit(t, work, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed "+string(ws)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, work, "add", ".")
		runGit(t, work, "commit", "-m", "seed")
		runGit(t, work, "push", bare, "main:refs/heads/main")
		commits[ws] = runGit(t, work, "rev-parse", "HEAD")
		return bare
	}
	return e, seed
}

func TestLaunchChecksOutExtraRepos(t *testing.T) {
	t.Parallel()
	e, seed := realGitEnv(t)
	primaryBare := seed(e.ws.ID)
	api := &domain.Workspace{Name: "my api", BaseBranch: "main"}
	if err := e.db.CreateWorkspace(t.Context(), api); err != nil {
		t.Fatal(err)
	}
	apiBare := seed(api.ID)

	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "wire the api", "fake", domain.LaunchTUI,
		domain.LaunchOptions{ExtraWorkspaces: []domain.WorkspaceID{api.ID}})
	if err != nil {
		t.Fatalf("LaunchWithOptions: %v", err)
	}
	if len(run.Extras) != 1 {
		t.Fatalf("extras = %+v, want one", run.Extras)
	}
	extra := run.Extras[0]
	if extra.WorkspaceID != api.ID || extra.Path != "/repos/my-api" || extra.Worktree == run.Worktree || extra.Branch == "" {
		t.Fatalf("extra = %+v", extra)
	}
	for _, dir := range []string{run.Worktree, extra.Worktree} {
		if _, statErr := os.Stat(filepath.Join(dir, "README.md")); statErr != nil {
			t.Fatalf("checkout %s: %v", dir, statErr)
		}
	}

	spec := e.rt.byName(string(run.ID)).spec
	if spec.WorktreeMountPath != "/workspace" || len(spec.ExtraWorktrees) != 1 ||
		spec.ExtraWorktrees[0].ContainerPath != "/repos/my-api" || spec.ExtraWorktrees[0].HostPath != extra.Worktree {
		t.Fatalf("spec worktrees = %q + %+v", spec.WorktreeMountPath, spec.ExtraWorktrees)
	}
	if len(spec.Mounts) == 0 {
		t.Fatal("home mounts missing")
	}
	for _, m := range spec.Mounts {
		if m.ContainerPath == "/repos/my-api" {
			t.Fatalf("extra repo bound as a plain mount: %+v", m)
		}
	}

	if err = os.WriteFile(filepath.Join(extra.Worktree, "api.txt"), []byte("handler\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)

	if got := runGit(t, apiBare, "show", extra.Branch+":api.txt"); got != "handler" {
		t.Fatalf("published api branch content = %q", got)
	}
	if out := runGit(t, primaryBare, "branch", "--list", extra.Branch); out != "" {
		t.Fatalf("extra branch %s leaked into the primary repo: %q", extra.Branch, out)
	}
	if out := runGit(t, primaryBare, "branch", "--list", run.Branch); !strings.Contains(out, run.Branch) {
		t.Fatalf("primary branch %s not published", run.Branch)
	}

	again, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Extras) != 1 || again.Extras[0].Key != domain.RepoKey(run.ID, 1) || again.Extras[0].Branch != extra.Branch {
		t.Fatalf("stored extras = %+v", again.Extras)
	}
}

func TestLaunchNamesTheRepoThatFails(t *testing.T) {
	t.Parallel()
	e, seed := realGitEnv(t)
	primaryCommit := runGit(t, seed(e.ws.ID), "rev-parse", "main")
	api := &domain.Workspace{Name: "api", BaseBranch: "main"}
	if err := e.db.CreateWorkspace(t.Context(), api); err != nil {
		t.Fatal(err)
	}
	// The API workspace has no repository, so its checkout fails.
	bases := baseCaptureFunc(func(_ context.Context, ws domain.WorkspaceID, _ string) (mirror.CaptureResult, error) {
		commit := primaryCommit
		if ws == api.ID {
			commit = strings.Repeat("a", 40)
		}
		return mirror.CaptureResult{WorkspaceID: ws, Commit: commit, Branch: "main", CheckedAt: time.Now()}, nil
	})
	e.sched.UseBaseCapture(bases)

	_, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "x", "fake", domain.LaunchTUI,
		domain.LaunchOptions{ExtraWorkspaces: []domain.WorkspaceID{api.ID}})
	if err == nil || !strings.Contains(err.Error(), `repo "api"`) {
		t.Fatalf("err = %v, want one naming repo \"api\"", err)
	}
	if _, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "x", "fake", domain.LaunchTUI,
		domain.LaunchOptions{ExtraWorkspaces: []domain.WorkspaceID{"missing"}}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unknown workspace err = %v", err)
	}
}
