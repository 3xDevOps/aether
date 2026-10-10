//go:build integration

package gitengine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// addContainerWorktree adds a linked worktree at dir and rewrites both of its
// link files the way a run container leaves them. By default they name the
// checkout by the container's mount point, a path the server does not have;
// relative is what Git writes under worktree.useRelativePaths.
func addContainerWorktree(t *testing.T, checkout, dir string, relative bool) {
	t.Helper()
	gitc(t, checkout, "worktree", "add", "-q", "--detach", dir)
	gitfile := filepath.Join(checkout, dir, ".git")
	link, err := os.ReadFile(gitfile)
	if err != nil {
		t.Fatal(err)
	}
	record := strings.TrimSpace(strings.TrimPrefix(string(link), "gitdir: "))
	inContainer := func(from, to string) string {
		if !relative {
			return strings.Replace(to, checkout, "/workspace", 1)
		}
		rel, relErr := filepath.Rel(from, to)
		if relErr != nil {
			t.Fatal(relErr)
		}
		return rel
	}
	for name, content := range map[string]string{
		gitfile:                         "gitdir: " + inContainer(filepath.Dir(gitfile), record),
		filepath.Join(record, "gitdir"): inContainer(record, gitfile),
	} {
		if writeErr := os.WriteFile(name, []byte(content+"\n"), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
}

func statPaths(files []events.FileDiffStat) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, fmt.Sprintf("%s +%d -%d", file.Path, file.Additions, file.Deletions))
	}
	return paths
}

// A worktree an agent adds inside its checkout is another checkout of the
// same repository. Its files are not the run's changes: not in the counts,
// not in the patch, and not in the commit the server makes.
func TestLinkedWorktreesStayOutOfTheRunsChanges(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "worktrees", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")

	addContainerWorktree(t, checkout, ".claude/worktrees/agent-1/tree", false)
	// Characters an ignore pattern would otherwise read as a glob.
	addContainerWorktree(t, checkout, "scratch/odd [1]*/tree", false)
	addContainerWorktree(t, checkout, "relative/tree", true)
	if err = os.WriteFile(filepath.Join(checkout, "notes.md"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if got := statPaths(files); truncated || len(got) != 1 || got[0] != "notes.md +3 -0" {
		t.Fatalf("stat set = %v (truncated %v), want only notes.md +3 -0", got, truncated)
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil {
		t.Fatalf("RunPatch: %v", err)
	}
	if strings.Count(patch.Text, "diff --git ") != 1 || !strings.Contains(patch.Text, "diff --git a/notes.md b/notes.md") {
		t.Fatalf("patch covers more than notes.md:\n%s", patch.Text)
	}
	if _, err = e.CommitAll(ctx, "run1", "wip: worktrees", domain.GitIdentity{}, nil); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if got := gitc(t, checkout, "show", "--name-only", "--format=", "HEAD"); got != "notes.md" {
		t.Fatalf("server commit holds %q, want only notes.md", got)
	}
	if commit, commitErr := e.CommitAll(ctx, "run1", "wip: again", domain.GitIdentity{}, nil); commitErr != nil || commit != "" {
		t.Fatalf("CommitAll with only linked worktrees left = (%q, %v), want nothing to commit", commit, commitErr)
	}
}

// The checkout is materialized under the fork point's attributes, so a file
// declared eol=crlf sits in the worktree with CRLF over an LF blob. Read
// back without those attributes it looks rewritten from top to bottom.
func TestForkPointLineEndingsAreNotChanges(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	ctx := t.Context()
	if _, err := e.InitWorkspaceRepo(ctx, "ws1"); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	gitc(t, src, "init")
	for name, content := range map[string]string{
		".gitattributes": "*.bat text eol=crlf\n",
		"run.bat":        "@echo off\necho one\necho two\n",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitc(t, src, "add", "-A")
	gitc(t, src, "commit", "-m", "initial")
	gitc(t, src, "push", url("ws1"), "main")

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "line endings", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	script := filepath.Join(checkout, "run.bat")
	if data, readErr := os.ReadFile(script); readErr != nil || string(data) != "@echo off\r\necho one\r\necho two\r\n" {
		t.Fatalf("checkout did not materialize CRLF: %q, %v", data, readErr)
	}

	files, _, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("untouched checkout reports changes: %v", statPaths(files))
	}
	if commit, commitErr := e.CommitAll(ctx, "run1", "wip: untouched", domain.GitIdentity{}, nil); commitErr != nil || commit != "" {
		t.Fatalf("CommitAll on an untouched checkout = (%q, %v), want nothing to commit", commit, commitErr)
	}

	if err = os.WriteFile(script, []byte("@echo off\r\necho one\r\necho 2\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, _, err = e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if got := statPaths(files); len(got) != 1 || got[0] != "run.bat +1 -1" {
		t.Fatalf("one edited line reports %v, want run.bat +1 -1", got)
	}
	if _, err = e.CommitAll(ctx, "run1", "wip: one line", domain.GitIdentity{}, nil); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if got := gitc(t, checkout, "show", "--numstat", "--format=", "HEAD"); got != "1\t1\trun.bat" {
		t.Fatalf("server commit changed %q, want one line of run.bat", got)
	}
	blob, err := e.gitBytes(ctx, checkout, "cat-file", "blob", "HEAD:run.bat")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("\r")) {
		t.Fatalf("server commit stored CRLF: %q", blob)
	}
}

func TestDiffStatsStopAtTheFileBound(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "many files", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	for i := range maxDiffStatFiles {
		name := filepath.Join(checkout, fmt.Sprintf("generated-%04d.txt", i))
		if err = os.WriteFile(name, []byte("line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if truncated || len(files) != maxDiffStatFiles {
		t.Fatalf("stat set at the bound = %d files, truncated %v", len(files), truncated)
	}
	if err = os.WriteFile(filepath.Join(checkout, "one-more.txt"), []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, truncated, err = e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if !truncated || len(files) != maxDiffStatFiles {
		t.Fatalf("stat set past the bound = %d files, truncated %v", len(files), truncated)
	}
}
