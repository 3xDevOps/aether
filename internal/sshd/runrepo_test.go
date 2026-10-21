package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runrepo"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type nativeRepoFixture struct {
	native *runrepo.Service
	dir    string
}

func (f *nativeRepoFixture) Native() *runrepo.Service { return f.native }
func (f *nativeRepoFixture) Execution(_ context.Context, _ domain.Run) (runrepo.Execution, error) {
	return runrepo.Execution{ContainerID: "native-test", WorkDir: f.dir, Authorize: func(context.Context, bool) error { return nil }}, nil
}

func repoLocalExec(ctx context.Context, _ runtime.ID, argv []string, dir string) (int, string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
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

func repoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	code, out, stderr, err := repoLocalExec(t.Context(), "native-test", append([]string{"git"}, args...), dir)
	if err != nil || code != 0 {
		t.Fatalf("git %v: code=%d stderr=%s err=%v", args, code, stderr, err)
	}
	return strings.TrimSpace(out)
}

func newNativeRepoFixture(t *testing.T) (*nativeRepoFixture, protocol.RunGitExpected) {
	t.Helper()
	f := &nativeRepoFixture{dir: t.TempDir(), native: runrepo.New(repoLocalExec)}
	repoGit(t, f.dir, "init", "-b", "main")
	repoGit(t, f.dir, "config", "user.name", "Native Author")
	repoGit(t, f.dir, "config", "user.email", "native@example.test")
	for _, path := range []string{"selected", "unrelated"} {
		if err := os.WriteFile(filepath.Join(f.dir, path), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repoGit(t, f.dir, "add", ".")
	repoGit(t, f.dir, "commit", "-m", "initial")
	return f, protocol.RunGitExpected{Branch: "main", Head: repoGit(t, f.dir, "rev-parse", "HEAD")}
}

func repoParams(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRunRepoHandlersRequireAccountUseEvenForAdminReads(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	_, other := addMember(t, e, "Other admin", domain.RoleAdmin, false)
	params := repoParams(t, protocol.RunGitDiffParams{RunID: string(e.run.ID)})
	_, perr := e.srv.dispatch(t.Context(), other.ID, protocol.MethodRunGitDiff, params)
	if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "has not shared") {
		t.Fatalf("admin credential read without grant: %+v", perr)
	}
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, "selected"), []byte("shared content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, perr := e.srv.dispatch(t.Context(), other.ID, protocol.MethodRunGitDiff, params)
	if perr != nil {
		t.Fatal(perr)
	}
	var result protocol.RunGitDiffResult
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || !strings.Contains(result.Output.Stdout, "+shared content") {
		t.Fatalf("authorized native diff: %+v", result)
	}
}

func TestRunRepoReadRechecksAccountGrantBetweenNativeCommands(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	_, other := addMember(t, e, "Shared reader", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	fixture.native = runrepo.New(func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		code, out, stderr, err := repoLocalExec(ctx, id, args, dir)
		if slices.Contains(args, "status") {
			if revokeErr := e.store.RevokeAccountShare(ctx, e.member.ID, other.ID); revokeErr != nil {
				t.Fatal(revokeErr)
			}
		}
		return code, out, stderr, err
	})
	value, perr := e.srv.dispatch(t.Context(), other.ID, protocol.MethodRunGitDiff, repoParams(t, protocol.RunGitDiffParams{RunID: string(e.run.ID)}))
	if perr != nil {
		t.Fatal(perr)
	}
	var result protocol.RunGitDiffResult
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "has not shared") || result.Output.Stdout != "" {
		t.Fatalf("revoked account read exposed content: %+v", result)
	}
}

func TestRunRepoCommitReturnsPartialFailureAndActualStaleHead(t *testing.T) {
	fixture, expected := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	if err := os.WriteFile(filepath.Join(fixture.dir, "selected"), []byte("selected change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repoGit(t, fixture.dir, "commit", "--allow-empty", "-m", "native advance")
	actual := repoGit(t, fixture.dir, "rev-parse", "HEAD")
	params := protocol.RunGitCommitParams{RunID: string(e.run.ID), Expected: expected, Paths: []string{"selected"}, Message: "Selected commit"}
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunGitCommit, repoParams(t, params))
	if perr != nil {
		t.Fatal(perr)
	}
	var stale protocol.RunGitCommitResult
	if err := json.Unmarshal(value, &stale); err != nil {
		t.Fatal(err)
	}
	if stale.Committed || stale.Actual == nil || stale.Actual.Head != actual || stale.Actual.Branch != "main" {
		t.Fatalf("stale response=%+v", stale)
	}
	params.Expected.Head = actual
	fixture.native = runrepo.New(func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		if slices.Contains(args, "reset") {
			if err := os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte("native writer"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return repoLocalExec(ctx, id, args, dir)
	})
	value, perr = e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunGitCommit, repoParams(t, params))
	if perr != nil {
		t.Fatal(perr)
	}
	var partial protocol.RunGitCommitResult
	if err := json.Unmarshal(value, &partial); err != nil {
		t.Fatal(err)
	}
	if !partial.Committed || partial.IndexUpdated || partial.HooksRun || partial.Head != repoGit(t, fixture.dir, "rev-parse", "HEAD") || !strings.Contains(partial.Output.Stderr, "index.lock") {
		t.Fatalf("partial publication lost: %+v", partial)
	}
}

func TestRunRepoCommitRechecksPushAfterRoleRevocation(t *testing.T) {
	fixture, expected := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	if err := os.WriteFile(filepath.Join(fixture.dir, "selected"), []byte("reviewed change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture.native = runrepo.New(func(ctx context.Context, id runtime.ID, args []string, dir string) (int, string, string, error) {
		code, out, stderr, repoExecErr := repoLocalExec(ctx, id, args, dir)
		if slices.Contains(args, "mktemp") && code == 0 && repoExecErr == nil {
			private := strings.TrimSpace(out)
			t.Cleanup(func() { _ = os.RemoveAll(private) })
		}
		if slices.Contains(args, "commit-tree") {
			member, err := e.store.GetMember(ctx, e.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			member.Role = domain.RoleViewer
			if err := e.store.UpdateMember(ctx, member); err != nil {
				t.Fatal(err)
			}
		}
		return code, out, stderr, repoExecErr
	})
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunGitCommit, repoParams(t, protocol.RunGitCommitParams{RunID: string(e.run.ID), Expected: expected, Paths: []string{"selected"}, Message: "Must not publish"}))
	if perr != nil {
		t.Fatal(perr)
	}
	var result protocol.RunGitCommitResult
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	if result.Committed || !strings.Contains(result.Error, "permission denied") || repoGit(t, fixture.dir, "rev-parse", "HEAD") != expected.Head {
		t.Fatalf("published after role revoke: %+v", result)
	}
}

func TestRunRepoHandlersRejectCallerExecutionFields(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	_, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunGitDiff, repoParams(t, map[string]any{"run_id": string(e.run.ID), "argv": []string{"gh", "api", "user"}, "workdir": "/root"}))
	if perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("caller execution accepted: %+v", perr)
	}
}

func TestRunGitLogUsesTheRunsRecordedBase(t *testing.T) {
	fixture, expected := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	e.run.BaseBranch, e.run.BaseCommit = "main", expected.Head
	if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
		t.Fatal(err)
	}
	repoGit(t, fixture.dir, "update-ref", "refs/remotes/origin/main", expected.Head)
	repoGit(t, fixture.dir, "checkout", "-q", "-b", "aether/run")
	repoGit(t, fixture.dir, "commit", "-q", "--allow-empty", "-m", "run work")
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunGitLog, repoParams(t, protocol.RunGitLogParams{RunID: string(e.run.ID)}))
	if perr != nil {
		t.Fatal(perr)
	}
	var log protocol.RunGitLogResult
	if err := json.Unmarshal(value, &log); err != nil {
		t.Fatal(err)
	}
	if log.Error != "" || log.BaseRef != "origin/main" || log.Base != expected.Head || log.Ahead != 1 || len(log.Commits) != 1 || log.Commits[0].Subject != "run work" {
		t.Fatalf("run.git.log = %+v", log)
	}
}

func TestRunPRMergePassesGHStderrAndPublishesState(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	if err := e.store.RecordRunPullRequest(t.Context(), &store.RunPullRequest{RunID: e.run.ID, Repository: "acme/app", Number: 7, URL: "https://github.com/acme/app/pull/7", State: "open", ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunPR}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	const refusal = "X Pull request acme/app#7 is not mergeable: 1 of 2 required status checks are failing.\n"
	pull := `{"number":7,"html_url":"https://github.com/acme/app/pull/7","state":"open","merged":false,"merged_at":null,"base":{"ref":"main","repo":{"full_name":"acme/app"}},"head":{"ref":"aether/run","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"full_name":"acme/app"}}}`
	mergeCode, mergeStderr := 1, refusal
	var merges [][]string
	fixture.native = runrepo.New(func(_ context.Context, _ runtime.ID, args []string, _ string) (int, string, string, error) {
		native := args[slices.Index(args, "gh"):]
		if native[1] == "pr" {
			merges = append(merges, slices.Clone(native))
			return mergeCode, "", mergeStderr, nil
		}
		if slices.Contains(native, "user") {
			return 0, "octo\n", "", nil
		}
		path := strings.Split(native[slices.IndexFunc(native, func(arg string) bool { return strings.HasPrefix(arg, "repos/") })], "/")
		return 0, strings.NewReplacer("acme/app", path[1]+"/"+path[2], `"number":7`, `"number":`+path[4]).Replace(pull), "", nil
	})
	merge := func(params protocol.RunPRMergeParams) protocol.RunPRMergeResult {
		t.Helper()
		params.RunID, params.Method, params.ExpectedHead = string(e.run.ID), "squash", "0123456789abcdef0123456789abcdef01234567"
		value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunPRMerge, repoParams(t, params))
		if perr != nil {
			t.Fatal(perr)
		}
		var result protocol.RunPRMergeResult
		if decodeErr := json.Unmarshal(value, &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return result
	}
	event := func() events.RunPRPayload {
		t.Helper()
		select {
		case ev := <-sub.Events():
			if ev.RunID != e.run.ID || ev.WorkspaceID != e.ws.ID {
				t.Fatalf("run.pr scope = %s/%s", ev.WorkspaceID, ev.RunID)
			}
			return ev.Payload.(events.RunPRPayload)
		case <-time.After(5 * time.Second):
			t.Fatal("run.pr was not published")
			return events.RunPRPayload{}
		}
	}

	refused := merge(protocol.RunPRMergeParams{})
	if refused.Merged || refused.Identity != "octo" || refused.Output.Stderr != refusal || !strings.Contains(refused.Error, refusal) || refused.PullRequest == nil || refused.PullRequest.State != "open" {
		t.Fatalf("refused merge = %+v", refused)
	}
	if !reflect.DeepEqual(merges[0], []string{"gh", "pr", "merge", "7", "--repo", "github.com/acme/app", "--squash", "--match-head-commit", "0123456789abcdef0123456789abcdef01234567"}) {
		t.Fatalf("merge argv = %v", merges[0])
	}

	mergeCode, mergeStderr = 0, ""
	pull = strings.NewReplacer(`"merged":false`, `"merged":true`, `"merged_at":null`, `"merged_at":"2026-10-20T01:02:03Z"`).Replace(pull)
	if unrelated := merge(protocol.RunPRMergeParams{Repository: "acme/other", Number: 9}); !unrelated.Merged {
		t.Fatalf("explicit merge = %+v", unrelated)
	}
	done := merge(protocol.RunPRMergeParams{})
	if !done.Merged || done.Error != "" || done.PullRequest.MergedAt == nil {
		t.Fatalf("merge = %+v", done)
	}
	if got := event(); got.Number != 7 || got.State != "merged" || got.MergedAt == nil {
		t.Fatalf("run.pr after merge = %+v; an unchanged or unrecorded pull request must publish nothing", got)
	}
	prs, err := e.store.RunPullRequests(t.Context(), e.run.ID)
	if err != nil || len(prs) != 1 || prs[0].State != "merged" || prs[0].MergedAt == nil {
		t.Fatalf("recorded pull request = %+v, %v", prs, err)
	}
}
