//go:build integration

package gitengine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// not in the patch, and not in the commit the server makes, whatever the
// checkout's own ignore rules say.
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
	// A negation in .gitignore outranks every rule the server could add.
	for name, content := range map[string]string{".gitignore": "!*/\n", "notes.md": "one\ntwo\nthree\n"} {
		if err = os.WriteFile(filepath.Join(checkout, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if got := statPaths(files); truncated || !slices.Equal(got, []string{".gitignore +1 -0", "notes.md +3 -0"}) {
		t.Fatalf("stat set = %v (truncated %v), want only .gitignore and notes.md", got, truncated)
	}
	watch := &diffWatch{e: e, run: "run1", checkout: checkout}
	if err = watch.loadIgnoreState(ctx); err != nil {
		t.Fatalf("loadIgnoreState: %v", err)
	}
	if !watch.ignored(filepath.Join(checkout, "relative", "tree")) || watch.ignored(filepath.Join(checkout, "notes.md")) {
		t.Fatal("the diff watch does not prune exactly the linked worktrees")
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil {
		t.Fatalf("RunPatch: %v", err)
	}
	if strings.Count(patch.Text, "diff --git ") != 2 || strings.Contains(patch.Text, "/tree") {
		t.Fatalf("patch covers more than .gitignore and notes.md:\n%s", patch.Text)
	}
	if _, err = e.CommitAll(ctx, "run1", "wip: worktrees", domain.GitIdentity{}, nil); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if got := gitc(t, checkout, "show", "--name-only", "--format=", "HEAD"); got != ".gitignore\nnotes.md" {
		t.Fatalf("server commit holds %q, want only .gitignore and notes.md", got)
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

// The stat set stops reading untracked content at the bound a snapshot
// stages under, without dropping a path: the overlap index reads them all.
func TestDiffStatsListEveryPathPastTheReadBudget(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "large files", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	// One byte short of the budget in whole files, then one more whole file.
	sizes := make([]int64, MaxSnapshotInputBytes/countBytesCap+1)
	for i := range sizes {
		sizes[i] = countBytesCap
	}
	sizes[len(sizes)-2]--
	for i, size := range sizes {
		name := filepath.Join(checkout, fmt.Sprintf("large-%02d.bin", i))
		if err = os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err = os.Truncate(name, size); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(checkout, "small.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if !truncated || len(files) != len(sizes)+1 {
		t.Fatalf("stat set past the read budget = %d files, truncated %v; want every path and truncated", len(files), truncated)
	}
	for i, file := range files {
		// A file of NUL bytes is one unterminated line.
		want := 1
		if i == len(sizes)-1 {
			want = 0
		}
		if file.Additions != want {
			t.Errorf("%s counts %d lines, want %d", file.Path, file.Additions, want)
		}
	}
}
