//go:build integration

package gitengine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listTree lists every entry under dir, one relative path per line.
func listTree(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, strings.TrimPrefix(path, dir))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return strings.Join(paths, "\n")
}

// TestRunPatchRendersWorkingDiff is the diff timeline's server half: what a
// member sees in the dashboard has to be everything the agent changed since
// the fork point - committed, uncommitted, and brand new files alike - and
// rendering it must not disturb the checkout the agent is working in.
func TestRunPatchRendersWorkingDiff(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "render diffs")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(checkout, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A committed change, so the patch has to reach past HEAD to the base.
	write("keep.txt", "one\ntwo\n")
	if _, err := e.CommitAll(ctx, "run1", "wip: edit"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	// Then uncommitted work: a further tracked edit, a brand new file, the
	// deletion of a file the base carried, and something git is told to
	// ignore.
	write("keep.txt", "one\ntwo\nthree\n")
	write("pkg/new.go", "package pkg\n")
	write("café.txt", "unicode\n")
	write(".gitignore", "*.log\n")
	write("build.log", "noise\n")
	if err := os.Remove(filepath.Join(checkout, "file.txt")); err != nil {
		t.Fatal(err)
	}

	before, err := e.git(ctx, checkout, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status before: %v", err)
	}
	objectsBefore := listTree(t, filepath.Join(checkout, ".git", "objects"))

	patch, err := e.RunPatch(ctx, "run1", 0)
	if err != nil {
		t.Fatalf("RunPatch: %v", err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	if patch.Base != base {
		t.Errorf("patch base = %q, want the recorded fork point %q", patch.Base, base)
	}
	if patch.Truncated {
		t.Error("a handful of lines was reported as truncated")
	}
	for _, want := range []string{
		"diff --git a/keep.txt b/keep.txt",
		"+three",
		"diff --git a/pkg/new.go b/pkg/new.go",
		"+package pkg",
		"diff --git a/file.txt b/file.txt",
		"deleted file mode",
		// Raw, not octal-escaped: the client matches these paths against the
		// ones run.diff reports, which are never escaped.
		"b/café.txt",
	} {
		if !strings.Contains(patch.Text, want) {
			t.Errorf("patch is missing %q:\n%s", want, patch.Text)
		}
	}
	if strings.Contains(patch.Text, "build.log") {
		t.Errorf("ignored file reached the patch:\n%s", patch.Text)
	}

	after, err := e.git(ctx, checkout, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status after: %v", err)
	}
	if after != before {
		t.Errorf("rendering the patch changed the agent's index:\nbefore %q\nafter  %q", before, after)
	}
	// The checkout's object database is chowned to the run's user at
	// provisioning; anything the server writes there would leave the agent
	// unable to commit, so rendering must add no objects or fan-out dirs.
	if objectsAfter := listTree(t, filepath.Join(checkout, ".git", "objects")); objectsAfter != objectsBefore {
		t.Errorf("rendering the patch wrote into the checkout's object database:\nbefore:\n%s\nafter:\n%s",
			objectsBefore, objectsAfter)
	}

	// The byte ceiling truncates at a line boundary rather than mid-hunk.
	capped, err := e.RunPatch(ctx, "run1", 40)
	if err != nil {
		t.Fatalf("capped RunPatch: %v", err)
	}
	if !capped.Truncated || len(capped.Text) > 40 {
		t.Fatalf("capped patch = %d bytes, truncated=%v", len(capped.Text), capped.Truncated)
	}
	if capped.Text != "" && !strings.HasSuffix(capped.Text, "\n") {
		t.Errorf("truncated patch ends mid-line: %q", capped.Text)
	}

	if _, err := e.RunPatch(ctx, "run-unknown", 0); err == nil {
		t.Error("RunPatch on a run with no checkout should fail")
	}
}

// TestRunPatchColonInDataDir pins that a ':' in the data-dir path - legal
// on Linux - survives patch rendering. Handing the checkout's objects to
// git via colon-split GIT_ALTERNATE_OBJECT_DIRECTORIES broke exactly here.
func TestRunPatchColonInDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aether:prod")
	e, err := New(Config{
		ReposDir:     filepath.Join(dir, "repos"),
		CheckoutsDir: filepath.Join(dir, "checkouts"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "colon path")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := e.RunPatch(ctx, "run1", 0)
	if err != nil {
		t.Fatalf("RunPatch under a colon data dir: %v", err)
	}
	if !strings.Contains(patch.Text, "+three") {
		t.Errorf("patch is missing the edit:\n%s", patch.Text)
	}
}
