package sshd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func workspaceImportGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = workspaceImportGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func workspaceImportGitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
}

func workspaceImportCommit(t *testing.T, source string) string {
	t.Helper()
	workspaceImportGit(t, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "source base")
	return workspaceImportGit(t, source, "rev-parse", "HEAD")
}

// The sole transport seam substitutes a local repository for the public URL.
// All fetches, protected refs, adoption, key generation, persistence, authority,
// and control-channel serialization use their production implementations.
func workspaceImportEnv(t *testing.T, seeded bool) (*testEnv, *gitengine.Engine, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	workspaceImportGit(t, root, "init", "--initial-branch=main", source)
	commit := ""
	if seeded {
		commit = workspaceImportCommit(t, source)
	}
	engine, err := gitengine.New(gitengine.Config{
		ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts"),
		MirrorFetch: func(ctx context.Context, repo string, req gitengine.MirrorRequest, incoming string) error {
			cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", "--", source, "+refs/heads/"+req.Branch+":"+incoming)
			cmd.Env = workspaceImportGitEnv()
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
	e := newTestEnv(t, func(c *Config) {
		svc, err := mirrorservice.New(mirrorservice.Config{Root: filepath.Join(root, "mirrors"), Store: c.Store, Git: engine})
		if err != nil {
			t.Fatal(err)
		}
		c.Services.Mirrors = svc
	})
	return e, engine, source, commit
}

func workspaceImportParams(name string) protocol.WorkspaceImportParams {
	return protocol.WorkspaceImportParams{
		Name: name, SourceURL: "https://github.com/upstream/source.git", BaseBranch: "main", Auth: "public",
	}
}

func TestWorkspaceImportFreshBaseAndExplicitOrigin(t *testing.T) {
	for _, origin := range []string{"git@github.com:member/fork.git", ""} {
		t.Run(origin, func(t *testing.T) {
			e, engine, _, commit := workspaceImportEnv(t, true)
			client := controlClient(t, e)
			p := workspaceImportParams("remote-only")
			p.Origin = origin
			var imported protocol.WorkspaceImportResult
			if err := client.Call(protocol.MethodWorkspaceImport, p, &imported); err != nil {
				t.Fatal(err)
			}
			if !imported.Created || imported.Workspace.ID == "" || imported.Error != "" || !imported.Mirror.Enabled || imported.Mirror.Status != string(domain.MirrorStatusPending) || imported.Mirror.ObservedCommit != commit || imported.Mirror.AcceptedCommit != "" {
				t.Fatalf("import = %+v", imported)
			}
			id := domain.WorkspaceID(imported.Workspace.ID)
			if _, err := engine.WorkspaceBranchCommit(t.Context(), id, "main"); err == nil {
				t.Fatal("initial fetch silently adopted a base")
			}
			var adopted protocol.WorkspaceMirrorResult
			if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: string(id), Generation: imported.Mirror.Generation}, &adopted); err != nil {
				t.Fatal(err)
			}
			stored, err := e.store.GetWorkspace(t.Context(), id)
			if err != nil || stored.Origin != domain.NormalizeOrigin(origin) || imported.Workspace.Origin != stored.Origin || adopted.AcceptedCommit != commit {
				t.Fatalf("workspace/adoption = %+v, %+v, %v", stored, adopted, err)
			}
			checkout, _, err := engine.CreateRunCheckout(t.Context(), id, "import-run", "main", "inspect source", stored.Origin)
			if err != nil {
				t.Fatal(err)
			}
			if got := workspaceImportGit(t, checkout, "rev-parse", "HEAD"); got != commit {
				t.Fatalf("checkout HEAD = %s, want %s", got, commit)
			}
			if origin != "" {
				if got := workspaceImportGit(t, checkout, "remote", "get-url", "origin"); got != stored.Origin {
					t.Fatalf("checkout origin = %q, want %q", got, stored.Origin)
				}
			} else if got := workspaceImportGit(t, checkout, "remote", "get-url", "origin"); !filepath.IsAbs(got) || got == p.SourceURL {
				t.Fatalf("empty explicit Origin acquired external push destination: %q", got)
			}
		})
	}
}

func TestWorkspaceImportRefusesCollaboratorBeforeCreation(t *testing.T) {
	e, _, _, _ := workspaceImportEnv(t, true)
	signer, _ := addMember(t, e, "collaborator", domain.RoleCollaborator, false)
	client := controlAs(t, e, signer)
	if err := client.Call(protocol.MethodWorkspaceImport, workspaceImportParams("denied"), nil); err == nil || wireErrOf(t, err).Code != protocol.CodeDenied {
		t.Fatalf("collaborator import = %v", err)
	}
	workspaces, err := e.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range workspaces {
		if workspace.Name == "denied" {
			t.Fatal("denied request created a workspace")
		}
	}
}

func TestWorkspaceImportFailureRetainsIdentityAndCanRefresh(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			e, engine, source, _ := workspaceImportEnv(t, !empty)
			client := controlClient(t, e)
			p := workspaceImportParams("repairable")
			p.Origin = "https://github.com/member/fork.git"
			if !empty {
				p.BaseBranch = "missing"
			}
			var imported protocol.WorkspaceImportResult
			if err := client.Call(protocol.MethodWorkspaceImport, p, &imported); err != nil {
				t.Fatal(err)
			}
			if !imported.Created || imported.Workspace.ID == "" || !imported.Mirror.Enabled || imported.Error == "" || !strings.Contains(imported.Error, p.BaseBranch) || imported.Mirror.LastError == "" || imported.Mirror.AcceptedCommit != "" {
				t.Fatalf("fetch failure lost partial state: %+v", imported)
			}
			id := domain.WorkspaceID(imported.Workspace.ID)
			stored, err := e.store.GetWorkspace(t.Context(), id)
			if err != nil || stored.Origin != p.Origin {
				t.Fatalf("failed import lost workspace: %+v, %v", stored, err)
			}
			if _, err := engine.WorkspaceBranchCommit(t.Context(), id, p.BaseBranch); err == nil {
				t.Fatal("missing source produced a base")
			}
			if empty {
				workspaceImportCommit(t, source)
			} else {
				workspaceImportGit(t, source, "branch", "missing", "HEAD")
			}
			commit := workspaceImportGit(t, source, "rev-parse", "HEAD")
			var repaired protocol.WorkspaceMirrorResult
			if err := client.Call(protocol.MethodWorkspaceMirrorRefresh, protocol.WorkspaceMirrorParams{WorkspaceID: string(id)}, &repaired); err != nil {
				t.Fatal(err)
			}
			if repaired.Generation != imported.Mirror.Generation || repaired.ObservedCommit != commit || repaired.AcceptedCommit != "" || repaired.LastError != "" {
				t.Fatalf("refresh did not repair same import: %+v", repaired)
			}
		})
	}
}

func TestWorkspaceImportConfigureFailureRetainsWorkspace(t *testing.T) {
	e, _, _, _ := workspaceImportEnv(t, true)
	p := workspaceImportParams("bad-source")
	p.SourceURL = "file:///etc"
	var result protocol.WorkspaceImportResult
	if err := controlClient(t, e).Call(protocol.MethodWorkspaceImport, p, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Workspace.ID == "" || result.Error == "" || result.Mirror.Enabled {
		t.Fatalf("configuration failure = %+v", result)
	}
	if _, err := e.store.GetWorkspace(t.Context(), domain.WorkspaceID(result.Workspace.ID)); err != nil {
		t.Fatalf("configuration failure discarded workspace: %v", err)
	}
}

func TestWorkspaceImportDeployKeyPendingThenAdopt(t *testing.T) {
	e, _, _, commit := workspaceImportEnv(t, true)
	client := controlClient(t, e)
	p := workspaceImportParams("private")
	p.Auth = "deploy-key"
	var imported protocol.WorkspaceImportResult
	if err := client.Call(protocol.MethodWorkspaceImport, p, &imported); err != nil {
		t.Fatal(err)
	}
	if !imported.Created || imported.Error != "" || imported.Mirror.Status != string(domain.MirrorStatusPending) || !strings.HasPrefix(imported.Mirror.PublicKey, "ssh-ed25519 ") || imported.Mirror.ObservedCommit != "" || imported.Mirror.AcceptedCommit != "" {
		t.Fatalf("deploy-key pending state = %+v", imported)
	}
	var refreshed, adopted protocol.WorkspaceMirrorResult
	if err := client.Call(protocol.MethodWorkspaceMirrorRefresh, protocol.WorkspaceMirrorParams{WorkspaceID: imported.Workspace.ID}, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.PublicKey != imported.Mirror.PublicKey || refreshed.Generation != imported.Mirror.Generation || refreshed.ObservedCommit != commit || refreshed.AcceptedCommit != "" {
		t.Fatalf("verification changed key or accepted candidate: %+v", refreshed)
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: imported.Workspace.ID, Generation: refreshed.Generation}, &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.AcceptedCommit != commit || adopted.PublicKey != imported.Mirror.PublicKey {
		t.Fatalf("adopted state = %+v", adopted)
	}
}

func TestWorkspaceImportAcceptedBaseRewriteNeedsAdoption(t *testing.T) {
	e, engine, source, accepted := workspaceImportEnv(t, true)
	client := controlClient(t, e)
	var imported protocol.WorkspaceImportResult
	if err := client.Call(protocol.MethodWorkspaceImport, workspaceImportParams("rewritten"), &imported); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: imported.Workspace.ID, Generation: imported.Mirror.Generation}, nil); err != nil {
		t.Fatal(err)
	}
	workspaceImportGit(t, source, "checkout", "--orphan", "rewrite")
	// Give the unrelated root a distinct message as well as no parent.
	workspaceImportGit(t, source, "-c", "user.name=Source", "-c", "user.email=source@example.test", "commit", "--allow-empty", "-m", "rewritten root")
	workspaceImportGit(t, source, "branch", "-M", "main")
	candidate := workspaceImportGit(t, source, "rev-parse", "HEAD")
	params := protocol.WorkspaceMirrorParams{WorkspaceID: imported.Workspace.ID}
	if err := client.Call(protocol.MethodWorkspaceMirrorRefresh, params, nil); err == nil {
		t.Fatal("rewrite refresh succeeded without explicit adoption")
	}
	var state protocol.WorkspaceMirrorResult
	if err := client.Call(protocol.MethodWorkspaceMirrorStatus, params, &state); err != nil {
		t.Fatal(err)
	}
	base, err := engine.WorkspaceBranchCommit(t.Context(), domain.WorkspaceID(imported.Workspace.ID), "main")
	if err != nil || base != accepted || state.AcceptedCommit != accepted || state.ObservedCommit != candidate || state.Status != string(domain.MirrorStatusRewritten) {
		t.Fatalf("rewritten source moved accepted base: base=%s state=%+v error=%v", base, state, err)
	}
}
