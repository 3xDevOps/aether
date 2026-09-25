package runrepo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func localExec(ctx context.Context, _ runtime.ID, argv []string, dir string) (int, string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) { return exit.ExitCode(), stdout.String(), stderr.String(), nil }
	return 0, stdout.String(), stderr.String(), err
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	code, stdout, stderr, err := localExec(context.Background(), "test", append([]string{"git"}, args...), dir)
	if err != nil || code != 0 { t.Fatalf("git %v: code=%d err=%v stderr=%s", args, code, err, stderr) }
	return strings.TrimSpace(stdout)
}

func writeFile(t *testing.T, dir, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(dir, path), []byte(value), 0600); err != nil { t.Fatal(err) }
}

func newRepo(t *testing.T) (*Service, Execution, Expected) {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "Run Author")
	gitCmd(t, dir, "config", "user.email", "run@example.test")
	writeFile(t, dir, "selected", "original\n")
	writeFile(t, dir, "unrelated", "original\n")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "initial")
	return New(localExec), Execution{ContainerID: "test", WorkDir: dir, Authorize: func(context.Context, bool) error { return nil }}, Expected{Branch: "main", Head: gitCmd(t, dir, "rev-parse", "HEAD")}
}

func TestCommitPreservesUnselectedIndexAndWorktree(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "unrelated", "staged unrelated\n")
	gitCmd(t, run.WorkDir, "add", "unrelated")
	writeFile(t, run.WorkDir, "unrelated", "unstaged unrelated\n")
	writeFile(t, run.WorkDir, "selected", "staged selected\n")
	gitCmd(t, run.WorkDir, "add", "selected")
	writeFile(t, run.WorkDir, "selected", "selected final\n")
	writeFile(t, run.WorkDir, ":(glob)*", "literal untracked\n")
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected", ":(glob)*"}, Message: "selected only"})
	if err != nil { t.Fatal(err) }
	if !result.Committed || !result.IndexUpdated || result.Head == expected.Head { t.Fatalf("result: %+v", result) }
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:selected"); got != "selected final" { t.Fatalf("selected content: %q", got) }
	if got := gitCmd(t, run.WorkDir, "show", "HEAD::(glob)*"); got != "literal untracked" { t.Fatalf("literal path: %q", got) }
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:unrelated"); got != "original" { t.Fatalf("committed unrelated content: %q", got) }
	if got := gitCmd(t, run.WorkDir, "show", ":unrelated"); got != "staged unrelated" { t.Fatalf("lost unrelated index: %q", got) }
	if got, err := os.ReadFile(filepath.Join(run.WorkDir, "unrelated")); err != nil || string(got) != "unstaged unrelated\n" { t.Fatalf("lost unrelated worktree: %q %v", got, err) }
	if got := gitCmd(t, run.WorkDir, "diff", "--cached", "--name-only"); got != "unrelated" { t.Fatalf("staged paths after commit: %q", got) }
}

func TestCommitRenameIncludesDeletionAndRejectsDirectories(t *testing.T) {
	s, run, expected := newRepo(t)
	gitCmd(t, run.WorkDir, "mv", "selected", "renamed with space")
	state, err := s.Status(context.Background(), run)
	if err != nil { t.Fatal(err) }
	if len(state.Changes) != 1 || state.Changes[0].Path != "renamed with space" || state.Changes[0].OriginalPath != "selected" { t.Fatalf("rename status: %+v", state) }
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"renamed with space"}, Message: "rename"})
	if err != nil { t.Fatal(err) }
	if got := gitCmd(t, run.WorkDir, "ls-tree", "--name-only", result.Head); got != "renamed with space\nunrelated" { t.Fatalf("committed tree: %q", got) }
	writeFile(t, run.WorkDir, "folder/one", "one")
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: Expected{Branch: "main", Head: result.Head}, Paths: []string{"folder"}, Message: "directory"})
	if err == nil { t.Fatal("directory selection silently included child files") }
}

func TestCommitCASRejectsConcurrentNativeCommit(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	var nativeHead string
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slicesContain(args, "update-ref") {
			gitCmd(t, dir, "commit", "--allow-empty", "-m", "native writer")
			nativeHead = gitCmd(t, dir, "rev-parse", "HEAD")
		}
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.Output.ExitCode == 0 || result.Committed { t.Fatalf("CAS result=%+v err=%v", result, err) }
	if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != nativeHead || got == expected.Head { t.Fatalf("overwrote native HEAD: %s vs %s", got, nativeHead) }
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:selected"); got != "original" { t.Fatalf("published rejected content: %q", got) }
}

func TestCommitReportsPublishedCommitWhenIndexIsLocked(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slicesContain(args, "reset") { writeFile(t, dir, ".git/index.lock", "another native writer") }
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if err == nil || !result.Committed || result.IndexUpdated || result.Head != gitCmd(t, run.WorkDir, "rev-parse", "HEAD") { t.Fatalf("partial commit result=%+v err=%v", result, err) }
	if result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "index.lock") { t.Fatalf("missing native lock failure: %+v", result.Output) }
}

func TestMutationsRejectStaleDetachedAndRevokedAuthority(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	gitCmd(t, run.WorkDir, "checkout", "--detach")
	_, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, ErrStale) { t.Fatalf("detached error: %v", err) }
	gitCmd(t, run.WorkDir, "checkout", "main")
	gitCmd(t, run.WorkDir, "commit", "--allow-empty", "-m", "advance")
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, ErrStale) { t.Fatalf("stale error: %v", err) }
	expected.Head = gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	revoked := errors.New("account access revoked")
	run.Authorize = func(_ context.Context, mutation bool) error { if mutation { return revoked }; return nil }
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, revoked) || gitCmd(t, run.WorkDir, "rev-parse", "HEAD") != expected.Head { t.Fatalf("revoked mutation: %v", err) }
}

func TestPushExactCommitAndRejectsNonFastForwardOrRetarget(t *testing.T) {
	s, run, expected := newRepo(t)
	bare := t.TempDir()
	gitCmd(t, bare, "init", "--bare")
	gitCmd(t, run.WorkDir, "remote", "add", "publish", bare)
	target := PushTarget{Remote: "publish", Repository: bare, HeadBranch: "review"}
	// Even a native commit in the final scheduling gap must not change the
	// object being pushed; the remote receives the explicitly reviewed OID.
	advanced := false
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slicesContain(args, "push") && !advanced { advanced = true; gitCmd(t, dir, "commit", "--allow-empty", "-m", "native advance") }
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err != nil || !result.Pushed { t.Fatalf("push=%+v err=%v", result, err) }
	if got := gitCmd(t, bare, "rev-parse", "refs/heads/review"); got != expected.Head { t.Fatalf("pushed wrong object %s", got) }
	s.exec = localExec
	newHead := gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	gitCmd(t, run.WorkDir, "push", "publish", "HEAD:refs/heads/review")
	gitCmd(t, run.WorkDir, "reset", "--hard", expected.Head)
	writeFile(t, run.WorkDir, "selected", "divergent\n")
	gitCmd(t, run.WorkDir, "commit", "-am", "divergent")
	expected.Head = gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	result, err = s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err == nil || result.Pushed || result.Output.ExitCode == 0 { t.Fatalf("forced divergent push: %+v %v", result, err) }
	if got := gitCmd(t, bare, "rev-parse", "refs/heads/review"); got != newHead { t.Fatalf("remote changed on failed push: %s", got) }
	gitCmd(t, run.WorkDir, "remote", "set-url", "--push", "publish", t.TempDir())
	_, err = s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err == nil || !strings.Contains(err.Error(), "destination changed") { t.Fatalf("retarget error: %v", err) }
}

func TestDiffStatusAndBoundedOutput(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", strings.Repeat("large line\n", MaxOutput/5))
	writeFile(t, run.WorkDir, "new file", "untracked")
	state, err := s.Status(context.Background(), run)
	if err != nil { t.Fatal(err) }
	if state.Branch != expected.Branch || state.Head != expected.Head || state.Detached || state.Unborn { t.Fatalf("state: %+v", state) }
	want := []Change{{Path: "selected", Index: ".", Worktree: "M"}, {Path: "new file", Index: "?", Worktree: "?", Untracked: true}}
	if !reflect.DeepEqual(state.Changes, want) { t.Fatalf("changes: %+v", state.Changes) }
	diff, err := s.Diff(context.Background(), run, DiffRequest{Paths: []string{"selected"}})
	if err != nil || !diff.Output.Truncated || len(diff.Output.Stdout) != MaxOutput || !strings.Contains(diff.Output.Stdout, "-original") { t.Fatalf("diff err=%v truncated=%t bytes=%d", err, diff.Output.Truncated, len(diff.Output.Stdout)) }
	for _, path := range []string{"../escape", "/absolute", ".git/config", "a/../b", "folder/", "a\x00b"} {
		if _, err := s.Diff(context.Background(), run, DiffRequest{Paths: []string{path}}); err == nil { t.Fatalf("accepted path %q", path) }
	}
}

func slicesContain(values []string, value string) bool { for _, item := range values { if item == value { return true } }; return false }
