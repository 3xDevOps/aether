package runrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// Command-local overrides (for example, env GIT_AUTHOR_EMAIL=...) still
	// apply, but the developer's ambient identity must not replace the fixture.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GIT_AUTHOR_") || strings.HasPrefix(key, "GIT_COMMITTER_") || key == "EMAIL" {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.String(), stderr.String(), nil
	}
	return 0, stdout.String(), stderr.String(), err
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	code, stdout, stderr, err := localExec(context.Background(), "test", append([]string{"git"}, args...), dir)
	if err != nil || code != 0 {
		t.Fatalf("git %v: code=%d err=%v stderr=%s", args, code, err, stderr)
	}
	return strings.TrimSpace(stdout)
}

func writeFile(t *testing.T, dir, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || !result.IndexUpdated || result.Head == expected.Head {
		t.Fatalf("result: %+v", result)
	}
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:selected"); got != "selected final" {
		t.Fatalf("selected content: %q", got)
	}
	if got := gitCmd(t, run.WorkDir, "show", "HEAD::(glob)*"); got != "literal untracked" {
		t.Fatalf("literal path: %q", got)
	}
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:unrelated"); got != "original" {
		t.Fatalf("committed unrelated content: %q", got)
	}
	if got := gitCmd(t, run.WorkDir, "show", ":unrelated"); got != "staged unrelated" {
		t.Fatalf("lost unrelated index: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(run.WorkDir, "unrelated")); err != nil || string(got) != "unstaged unrelated\n" {
		t.Fatalf("lost unrelated worktree: %q %v", got, err)
	}
	if got := gitCmd(t, run.WorkDir, "diff", "--cached", "--name-only"); got != "unrelated" {
		t.Fatalf("staged paths after commit: %q", got)
	}
}

func TestCommitRenameIncludesDeletionAndRejectsDirectories(t *testing.T) {
	s, run, expected := newRepo(t)
	gitCmd(t, run.WorkDir, "mv", "selected", "renamed with space")
	state, err := s.Status(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Changes) != 1 || state.Changes[0].Path != "renamed with space" || state.Changes[0].OriginalPath != "selected" {
		t.Fatalf("rename status: %+v", state)
	}
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"renamed with space"}, Message: "rename"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, run.WorkDir, "ls-tree", "--name-only", result.Head); got != "renamed with space\nunrelated" {
		t.Fatalf("committed tree: %q", got)
	}
	writeFile(t, run.WorkDir, "folder/one", "one")
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: Expected{Branch: "main", Head: result.Head}, Paths: []string{"folder"}, Message: "directory"})
	if err == nil {
		t.Fatal("directory selection silently included child files")
	}
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
	if !errors.As(err, &commandErr) || commandErr.Output.ExitCode == 0 || result.Committed {
		t.Fatalf("CAS result=%+v err=%v", result, err)
	}
	if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != nativeHead || got == expected.Head {
		t.Fatalf("overwrote native HEAD: %s vs %s", got, nativeHead)
	}
	if got := gitCmd(t, run.WorkDir, "show", "HEAD:selected"); got != "original" {
		t.Fatalf("published rejected content: %q", got)
	}
}

func TestCommitReportsPublishedCommitWhenIndexIsLocked(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slicesContain(args, "reset") {
			writeFile(t, dir, ".git/index.lock", "another native writer")
		}
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if err == nil || !result.Committed || result.IndexUpdated || result.Head != gitCmd(t, run.WorkDir, "rev-parse", "HEAD") {
		t.Fatalf("partial commit result=%+v err=%v", result, err)
	}
	if result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "index.lock") {
		t.Fatalf("missing native lock failure: %+v", result.Output)
	}
}

func TestMutationsRejectStaleDetachedAndRevokedAuthority(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	gitCmd(t, run.WorkDir, "checkout", "--detach")
	_, err := s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("detached error: %v", err)
	}
	gitCmd(t, run.WorkDir, "checkout", "main")
	gitCmd(t, run.WorkDir, "commit", "--allow-empty", "-m", "advance")
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale error: %v", err)
	}
	expected.Head = gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	revoked := errors.New("account access revoked")
	run.Authorize = func(_ context.Context, mutation bool) error {
		if mutation {
			return revoked
		}
		return nil
	}
	_, err = s.Commit(context.Background(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, revoked) || gitCmd(t, run.WorkDir, "rev-parse", "HEAD") != expected.Head {
		t.Fatalf("revoked mutation: %v", err)
	}
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
		if slicesContain(args, "push") && !advanced {
			advanced = true
			gitCmd(t, dir, "commit", "--allow-empty", "-m", "native advance")
		}
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err != nil || !result.Pushed {
		t.Fatalf("push=%+v err=%v", result, err)
	}
	if got := gitCmd(t, bare, "rev-parse", "refs/heads/review"); got != expected.Head {
		t.Fatalf("pushed wrong object %s", got)
	}
	s.exec = localExec
	newHead := gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	gitCmd(t, run.WorkDir, "push", "publish", "HEAD:refs/heads/review")
	gitCmd(t, run.WorkDir, "reset", "--hard", expected.Head)
	writeFile(t, run.WorkDir, "selected", "divergent\n")
	gitCmd(t, run.WorkDir, "commit", "-am", "divergent")
	expected.Head = gitCmd(t, run.WorkDir, "rev-parse", "HEAD")
	result, err = s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err == nil || result.Pushed || result.Output.ExitCode == 0 {
		t.Fatalf("forced divergent push: %+v %v", result, err)
	}
	if got := gitCmd(t, bare, "rev-parse", "refs/heads/review"); got != newHead {
		t.Fatalf("remote changed on failed push: %s", got)
	}
	gitCmd(t, run.WorkDir, "remote", "set-url", "--push", "publish", t.TempDir())
	_, err = s.Push(context.Background(), run, PushRequest{Expected: expected, Target: target})
	if err == nil || !strings.Contains(err.Error(), "destination changed") {
		t.Fatalf("retarget error: %v", err)
	}
}

func TestDiffStatusAndBoundedOutput(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", strings.Repeat("large line\n", MaxOutput/5))
	writeFile(t, run.WorkDir, "new file", "untracked")
	state, err := s.Status(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if state.Branch != expected.Branch || state.Head != expected.Head || state.Detached || state.Unborn {
		t.Fatalf("state: %+v", state)
	}
	want := []Change{{Path: "selected", Index: ".", Worktree: "M"}, {Path: "new file", Index: "?", Worktree: "?", Untracked: true}}
	if !reflect.DeepEqual(state.Changes, want) {
		t.Fatalf("changes: %+v", state.Changes)
	}
	diff, err := s.Diff(context.Background(), run, DiffRequest{Paths: []string{"selected"}})
	if err != nil || !diff.Output.Truncated || len(diff.Output.Stdout) != MaxOutput || !strings.Contains(diff.Output.Stdout, "-original") {
		t.Fatalf("diff err=%v truncated=%t bytes=%d", err, diff.Output.Truncated, len(diff.Output.Stdout))
	}
	for _, path := range []string{"../escape", "/absolute", ".git/config", "a/../b", "folder/", "a\x00b"} {
		if _, err := s.Diff(context.Background(), run, DiffRequest{Paths: []string{path}}); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
}

func slicesContain(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func TestCommitRejectsConcurrentBranchSwitchWithIdenticalHead(t *testing.T) {
	for _, detach := range []bool{false, true} {
		t.Run(fmt.Sprintf("detached=%t", detach), func(t *testing.T) {
			s, run, expected := newRepo(t)
			gitCmd(t, run.WorkDir, "branch", "other", expected.Head)
			writeFile(t, run.WorkDir, "selected", "ours\n")
			s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
				if slicesContain(args, "update-ref") {
					if detach {
						gitCmd(t, dir, "checkout", "--detach")
					} else {
						gitCmd(t, dir, "checkout", "other")
					}
				}
				return localExec(ctx, id, args, dir)
			}
			result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
			var stale *StaleError
			if !errors.As(err, &stale) || result.Committed || stale.Actual.Head != expected.Head || stale.Detached != detach {
				t.Fatalf("branch switch: result=%+v stale=%+v err=%v", result, stale, err)
			}
			if !detach && stale.Actual.Branch != "other" {
				t.Fatalf("actual branch=%q", stale.Actual.Branch)
			}
			for _, branch := range []string{"main", "other"} {
				if got := gitCmd(t, run.WorkDir, "rev-parse", branch); got != expected.Head {
					t.Fatalf("changed unrelated/non-current branch %s to %s", branch, got)
				}
			}
		})
	}
}

func TestCommitSigningFailureDoesNotPublishOrDiscardIndex(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	writeFile(t, run.WorkDir, "unrelated", "staged elsewhere\n")
	gitCmd(t, run.WorkDir, "add", "unrelated")
	gitCmd(t, run.WorkDir, "config", "gpg.format", "ssh")
	gitCmd(t, run.WorkDir, "config", "user.signingkey", filepath.Join(run.WorkDir, "missing-signing-key"))
	gitCmd(t, run.WorkDir, "config", "commit.gpgsign", "true")
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "signed"})
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || result.Committed || result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "missing-signing-key") {
		t.Fatalf("signing failure: %+v err=%v", result, err)
	}
	if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != expected.Head {
		t.Fatalf("published unsigned commit %s", got)
	}
	if got := gitCmd(t, run.WorkDir, "show", ":unrelated"); got != "staged elsewhere" {
		t.Fatalf("lost index: %q", got)
	}
}

func TestCommitDeletionAndCoauthorsPreserveNativeIdentity(t *testing.T) {
	s, run, expected := newRepo(t)
	if err := os.Remove(filepath.Join(run.WorkDir, "selected")); err != nil {
		t.Fatal(err)
	}
	run.CoAuthors = func(_ context.Context, author string) ([]string, error) {
		if author != "run@example.test" {
			return nil, fmt.Errorf("wrong actual author %q", author)
		}
		return []string{"Co-authored-by: Steering Person <steerer@example.test>"}, nil
	}
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "delete selected\n\nCo-authored-by: Steering Person <steerer@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, run.WorkDir, "ls-tree", "--name-only", result.Head); got != "unrelated" {
		t.Fatalf("deletion tree=%q", got)
	}
	message := gitCmd(t, run.WorkDir, "log", "-1", "--format=%B")
	if strings.Count(message, "Co-authored-by: Steering Person") != 1 {
		t.Fatalf("trailers=%q", message)
	}
	if got := gitCmd(t, run.WorkDir, "log", "-1", "--format=%an <%ae>"); got != "Run Author <run@example.test>" {
		t.Fatalf("author=%q", got)
	}
}

func TestCommitRechecksAuthorityAtPublication(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	revoked := errors.New("shared account revoked")
	revoke := false
	run.Authorize = func(_ context.Context, mutate bool) error {
		if revoke && mutate {
			return revoked
		}
		return nil
	}
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		code, stdout, stderr, err := localExec(ctx, id, args, dir)
		if slicesContain(args, "mktemp") && code == 0 && err == nil {
			private := strings.TrimSpace(stdout)
			t.Cleanup(func() { _ = os.RemoveAll(private) })
		}
		if slicesContain(args, "commit-tree") {
			revoke = true
		}
		return code, stdout, stderr, err
	}
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, revoked) || result.Committed {
		t.Fatalf("revoked result=%+v err=%v", result, err)
	}
	if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != expected.Head {
		t.Fatalf("published after revoke: %s", got)
	}
}

func TestCommitRejectsSelectedFileBecomingDirectory(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slicesContain(args, "add") {
			if err := os.Remove(filepath.Join(dir, "selected")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "selected/unreviewed", "must not publish\n")
		}
		return localExec(ctx, id, args, dir)
	}
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if err == nil || result.Committed || !strings.Contains(err.Error(), "unexpected path") {
		t.Fatalf("directory race=%+v err=%v", result, err)
	}
	if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != expected.Head {
		t.Fatalf("published unreviewed descendants: %s", got)
	}
}

func TestPushRejectsMultipleConfiguredDestinations(t *testing.T) {
	s, run, expected := newRepo(t)
	first, second := t.TempDir(), t.TempDir()
	gitCmd(t, first, "init", "--bare")
	gitCmd(t, second, "init", "--bare")
	gitCmd(t, run.WorkDir, "remote", "add", "publish", first)
	gitCmd(t, run.WorkDir, "remote", "set-url", "--add", "--push", "publish", first)
	gitCmd(t, run.WorkDir, "remote", "set-url", "--add", "--push", "publish", second)
	result, err := s.Push(t.Context(), run, PushRequest{Expected: expected, Target: PushTarget{Remote: "publish", Repository: first, HeadBranch: "review"}})
	if err == nil || result.Pushed || !strings.Contains(err.Error(), "multiple destinations") {
		t.Fatalf("ambiguous push=%+v err=%v", result, err)
	}
	if got := gitCmd(t, first, "for-each-ref", "--format=%(refname)"); got != "" {
		t.Fatalf("pushed first ambiguous destination: %s", got)
	}
	if got := gitCmd(t, second, "for-each-ref", "--format=%(refname)"); got != "" {
		t.Fatalf("pushed second ambiguous destination: %s", got)
	}
}

func TestCommitPreservesNonzeroCleanupAndEarlierFailure(t *testing.T) {
	for _, indexFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("index-failure=%t", indexFailure), func(t *testing.T) {
			s, run, expected := newRepo(t)
			writeFile(t, run.WorkDir, "selected", "ours\n")
			s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
				if slicesContain(args, "reset") && indexFailure {
					writeFile(t, dir, ".git/index.lock", "native writer")
				}
				if slicesContain(args, "rm") {
					private := args[len(args)-1]
					t.Cleanup(func() { _ = os.RemoveAll(private) })
					// Real rm exits nonzero with nil Exec transport error.
					return localExec(ctx, id, []string{"rm", "--", filepath.Join(private, "missing-cleanup-entry")}, dir)
				}
				return localExec(ctx, id, args, dir)
			}
			result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
			if err == nil || !result.Committed || result.IndexUpdated == indexFailure || !strings.Contains(err.Error(), "missing-cleanup-entry") {
				t.Fatalf("cleanup error lost partial state: %+v err=%v", result, err)
			}
			if indexFailure && !strings.Contains(err.Error(), "index.lock") {
				t.Fatalf("cleanup replaced earlier failure: %v", err)
			}
			if !indexFailure && (result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "missing-cleanup-entry")) {
				t.Fatalf("cleanup lost actual command diagnostics: %+v", result.Output)
			}
			if got := gitCmd(t, run.WorkDir, "rev-parse", "HEAD"); got != result.Head {
				t.Fatalf("partial result lost published HEAD %s", got)
			}
		})
	}
}

func TestRemotesExposeIndependentFetchAndPushConfiguration(t *testing.T) {
	s, run, _ := newRepo(t)
	fetch, push := t.TempDir(), t.TempDir()
	gitCmd(t, run.WorkDir, "remote", "add", "origin", fetch)
	gitCmd(t, run.WorkDir, "remote", "set-url", "--push", "origin", push)
	remotes, _, err := s.Remotes(t.Context(), run, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 1 || remotes[0].Name != "origin" || !reflect.DeepEqual(remotes[0].FetchURLs, []string{fetch}) || !reflect.DeepEqual(remotes[0].PushURLs, []string{push}) {
		t.Fatalf("conflated fetch and push targets: %+v", remotes)
	}
}

func TestCommitReconcilesLostPublicationResponseWithoutRepeatingMutation(t *testing.T) {
	s, run, expected := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "ours\n")
	lost := errors.New("exec transport response lost")
	publications := 0
	s.exec = func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		code, stdout, stderr, err := localExec(ctx, id, args, dir)
		if slicesContain(args, "update-ref") && code == 0 && err == nil {
			publications++
			return code, stdout, stderr, lost
		}
		return code, stdout, stderr, err
	}
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "ours"})
	if !errors.Is(err, lost) || !result.Committed || result.IndexUpdated || result.Head != gitCmd(t, run.WorkDir, "rev-parse", "HEAD") || publications != 1 {
		t.Fatalf("lost publication response: %+v attempts=%d err=%v", result, publications, err)
	}
}

func TestCommitPreservesNativeTransactionHooksAndTheirVeto(t *testing.T) {
	for _, veto := range []bool{false, true} {
		t.Run(fmt.Sprintf("veto=%t", veto), func(t *testing.T) {
			s, run, expected := newRepo(t)
			hooks := filepath.Join(t.TempDir(), "native hooks")
			script := `#!/bin/sh
set -eu
cat >"hook-input-$1"
printf '%s\n' "$1" >>hook-events
if [ "$1" = prepared ]; then
	test -f "$(git rev-parse --git-path HEAD.lock)"
	test -f "$(git rev-parse --git-path refs/heads/main.lock)"
	printf 'prepare: ok\ncommit: ok\n'
`
			if veto {
				script += "	printf '%s\\n' 'native publication veto' >&2\n	exit 1\n"
			}
			script += "fi\n"
			writeFile(t, hooks, "reference-transaction", script)
			if err := os.Chmod(filepath.Join(hooks, "reference-transaction"), 0700); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, run.WorkDir, "config", "core.hooksPath", hooks)
			writeFile(t, run.WorkDir, "selected", "reviewed\n")
			result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "native policy"})
			if veto {
				if err == nil || result.Committed || !strings.Contains(result.Output.Stderr, "native publication veto") || gitCmd(t, run.WorkDir, "rev-parse", "HEAD") != expected.Head {
					t.Fatalf("transaction hook veto lost: result=%+v err=%v", result, err)
				}
			} else if err != nil || !result.Committed || !result.IndexUpdated {
				t.Fatalf("native hook publication: result=%+v err=%v", result, err)
			}
			events, readErr := os.ReadFile(filepath.Join(run.WorkDir, "hook-events"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(events), "prepared\n") != 1 || strings.Contains(string(events), "committed\n") == veto || strings.Contains(string(events), "aborted\n") != veto {
				t.Fatalf("native transaction lifecycle=%q", events)
			}
			input, readErr := os.ReadFile(filepath.Join(run.WorkDir, "hook-input-prepared"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(input), expected.Head+" ") || !strings.Contains(string(input), " refs/heads/main\n") {
				t.Fatalf("native hook lost transaction stdin=%q", input)
			}
			for _, ref := range []string{"HEAD", "refs/heads/main"} {
				lock := gitCmd(t, run.WorkDir, "rev-parse", "--git-path", ref+".lock")
				if !filepath.IsAbs(lock) {
					lock = filepath.Join(run.WorkDir, lock)
				}
				if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native lock retained: %s: %v", lock, err)
				}
			}
		})
	}
}

func TestCommitSignsSelectedFilesInLinkedWorktree(t *testing.T) {
	s, run, initial := newRepo(t)
	mainDir := run.WorkDir
	run.WorkDir = filepath.Join(t.TempDir(), "linked")
	gitCmd(t, mainDir, "worktree", "add", "-b", "linked", run.WorkDir)
	expected := Expected{Branch: "linked", Head: initial.Head}
	key := filepath.Join(t.TempDir(), "signing-key")
	code, _, stderr, err := localExec(t.Context(), "test", []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key}, run.WorkDir)
	if err != nil || code != 0 {
		t.Fatalf("native signing key: code=%d stderr=%s err=%v", code, stderr, err)
	}
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(t.TempDir(), "allowed-signers")
	if err = os.WriteFile(allowed, []byte("run@example.test "+strings.TrimSpace(string(publicKey))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, run.WorkDir, "config", "gpg.format", "ssh")
	gitCmd(t, run.WorkDir, "config", "user.signingkey", key)
	gitCmd(t, run.WorkDir, "config", "gpg.ssh.allowedSignersFile", allowed)
	gitCmd(t, run.WorkDir, "config", "commit.gpgsign", "true")
	writeFile(t, run.WorkDir, "unrelated", "unrelated staged\n")
	gitCmd(t, run.WorkDir, "add", "unrelated")
	writeFile(t, run.WorkDir, "unrelated", "unrelated worktree\n")
	writeFile(t, run.WorkDir, "selected", "signed selection\n")
	result, err := s.Commit(t.Context(), run, CommitRequest{Expected: expected, Paths: []string{"selected"}, Message: "signed linked selection"})
	if err != nil || !result.Committed || !result.IndexUpdated {
		t.Fatalf("signed linked commit: %+v err=%v", result, err)
	}
	gitCmd(t, run.WorkDir, "verify-commit", result.Head)
	if gitCmd(t, run.WorkDir, "symbolic-ref", "--short", "HEAD") != "linked" || gitCmd(t, mainDir, "rev-parse", "HEAD") != initial.Head {
		t.Fatal("linked publication changed the wrong HEAD")
	}
	if gitCmd(t, run.WorkDir, "show", result.Head+":selected") != "signed selection" || gitCmd(t, run.WorkDir, "show", result.Head+":unrelated") != "original" || gitCmd(t, run.WorkDir, "show", ":unrelated") != "unrelated staged" {
		t.Fatal("signed publication lost selected-tree or unrelated-index isolation")
	}
	worktree, err := os.ReadFile(filepath.Join(run.WorkDir, "unrelated"))
	if err != nil || string(worktree) != "unrelated worktree\n" {
		t.Fatalf("unrelated worktree changed: %q err=%v", worktree, err)
	}
}
