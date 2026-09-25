package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/store"
)

func importGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newImportService(t *testing.T, st Store) (*Service, *gitengine.Engine, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	importGit(t, root, "init", "--initial-branch=main", source)
	importGit(t, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "source base")
	commit := importGit(t, source, "rev-parse", "HEAD")
	engine, err := gitengine.New(gitengine.Config{
		ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts"),
		MirrorFetch: func(ctx context.Context, repo string, req gitengine.MirrorRequest, incoming string) error {
			cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--", source, "+refs/heads/"+req.Branch+":"+incoming)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("fetch: %w: %s", err, out)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	svc, err := New(Config{Root: filepath.Join(root, "mirrors"), Store: st, Git: engine})
	if err != nil {
		t.Fatal(err)
	}
	return svc, engine, commit
}

func TestImportNewWorkspacePreservesExplicitCheckoutOrigin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &domain.Workspace{Name: "remote-only", BaseBranch: "main"}
	if err := db.CreateWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	const origin = "https://github.com/member/fork.git"
	if err := db.SetWorkspaceOrigin(t.Context(), workspace.ID, origin); err != nil {
		t.Fatal(err)
	}
	svc, engine, commit := newImportService(t, db)
	configured, err := svc.Configure(t.Context(), workspace.ID, ConfigureRequest{
		SourceURL: "https://github.com/upstream/source.git", Branch: "main", Auth: domain.MirrorAuthPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := svc.Refresh(t.Context(), workspace.ID)
	if err != nil || observed.Mirror.Status != domain.MirrorStatusPending || observed.Mirror.ObservedCommit != commit || observed.Mirror.AcceptedCommit != "" {
		t.Fatalf("initial candidate = %+v, %v", observed, err)
	}
	if _, err := svc.Adopt(t.Context(), workspace.ID, configured.Mirror.Generation); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetWorkspace(t.Context(), workspace.ID)
	if err != nil || stored.Origin != origin {
		t.Fatalf("mirror replaced explicit Origin: %+v, %v", stored, err)
	}
	checkout, _, err := engine.CreateRunCheckout(t.Context(), workspace.ID, "import-run", "main", "review imported source", stored.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if got := importGit(t, checkout, "remote", "get-url", "origin"); got != origin {
		t.Fatalf("checkout origin = %q, want %q", got, origin)
	}
	if got := importGit(t, checkout, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("checkout base = %s, want %s", got, commit)
	}
}

func TestImportReportsGitStateWhenPersistenceFails(t *testing.T) {
	for _, phase := range []string{"observe", "adopt"} {
		t.Run(phase, func(t *testing.T) {
			st := newMirrorTestStore()
			svc, engine, commit := newImportService(t, st)
			configured, err := svc.Configure(t.Context(), "import", ConfigureRequest{
				SourceURL: "https://github.com/upstream/source.git", Branch: "main", Auth: domain.MirrorAuthPublic,
			})
			if err != nil {
				t.Fatal(err)
			}
			persistence := errors.New("database write unavailable")
			var result Result
			if phase == "observe" {
				st.setErrors = []error{nil, persistence}
				result, err = svc.Refresh(t.Context(), "import")
			} else {
				if _, err := svc.Refresh(t.Context(), "import"); err != nil {
					t.Fatal(err)
				}
				st.setErrors = []error{persistence}
				result, err = svc.Adopt(t.Context(), "import", configured.Mirror.Generation)
			}
			if !errors.Is(err, persistence) || result.Mirror.ObservedCommit != commit {
				t.Fatalf("lost actual Git state on %s persistence failure: %+v, %v", phase, result, err)
			}
			if phase == "adopt" {
				base, baseErr := engine.WorkspaceBranchCommit(t.Context(), "import", "main")
				if baseErr != nil || base != commit || result.Mirror.AcceptedCommit != commit || result.Mirror.Status != domain.MirrorStatusReady {
					t.Fatalf("adoption partial failure omitted accepted base: %+v, base=%s, %v", result, base, baseErr)
				}
			} else if result.Mirror.AcceptedCommit != "" || result.Mirror.Status != domain.MirrorStatusPending {
				t.Fatalf("observation partial failure claimed accepted base: %+v", result)
			}
		})
	}
}

func TestImportDoesNotInferCheckoutOriginFromSource(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &domain.Workspace{Name: "no-push-target", BaseBranch: "main"}
	if err := db.CreateWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	svc, _, _ := newImportService(t, db)
	configured, err := svc.Configure(t.Context(), workspace.ID, ConfigureRequest{
		SourceURL: "https://github.com/upstream/source.git", Branch: "main", Auth: domain.MirrorAuthPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(t.Context(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Adopt(t.Context(), workspace.ID, configured.Mirror.Generation); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetWorkspace(t.Context(), workspace.ID)
	if err != nil || stored.Origin != "" {
		t.Fatalf("source became implicit push target: %+v, %v", stored, err)
	}
}
