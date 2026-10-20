package runrepo

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func subjects(commits []protocol.RunGitCommit) []string {
	out := []string{}
	for _, commit := range commits {
		out = append(out, commit.Subject)
	}
	return out
}

func TestLogTracksBaseMovementAndPredictsConflicts(t *testing.T) {
	s, run, initial := newRepo(t)
	dir := run.WorkDir
	gitCmd(t, dir, "checkout", "-q", "-b", "upstream")
	writeFile(t, dir, "upstream-only", "upstream\n")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-q", "-m", "upstream clean")
	gitCmd(t, dir, "update-ref", "refs/remotes/origin/main", "upstream")
	gitCmd(t, dir, "checkout", "-q", "-b", "aether/run", initial.Head)
	writeFile(t, dir, "selected", "run change\n")
	gitCmd(t, dir, "commit", "-q", "-am", "run one")
	writeFile(t, dir, "unrelated", "run change\n")
	gitCmd(t, dir, "commit", "-q", "-am", "run two")
	request := LogRequest{BaseBranch: "main", ForkPoint: initial.Head}

	log, err := s.Log(t.Context(), run, request)
	if err != nil {
		t.Fatal(err)
	}
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	if log.Branch != "aether/run" || log.BaseRef != "origin/main" || log.Base != gitCmd(t, dir, "rev-parse", "upstream") || log.Head != head || log.MergeBase != initial.Head {
		t.Fatalf("log refs = %+v", log)
	}
	if log.Ahead != 2 || log.Behind != 1 || len(log.Conflicts) != 0 || log.Truncated {
		t.Fatalf("clean base: ahead=%d behind=%d conflicts=%v truncated=%t", log.Ahead, log.Behind, log.Conflicts, log.Truncated)
	}
	if got := subjects(log.Commits); !reflect.DeepEqual(got, []string{"run two", "run one"}) {
		t.Fatalf("commits = %v", got)
	}
	if got := subjects(log.Upstream); !reflect.DeepEqual(got, []string{"upstream clean"}) {
		t.Fatalf("upstream = %v", got)
	}
	top := log.Commits[0]
	if top.SHA != head || !strings.HasPrefix(head, top.Short) || top.Author != "Run Author" || !reflect.DeepEqual(top.Parents, []string{log.Commits[1].SHA}) || top.AuthoredAt == "" {
		t.Fatalf("commit = %+v", top)
	}

	gitCmd(t, dir, "checkout", "-q", "upstream")
	writeFile(t, dir, "selected", "upstream change\n")
	gitCmd(t, dir, "commit", "-q", "-am", "upstream conflicting")
	gitCmd(t, dir, "update-ref", "refs/remotes/origin/main", "upstream")
	gitCmd(t, dir, "checkout", "-q", "aether/run")
	log, err = s.Log(t.Context(), run, request)
	if err != nil {
		t.Fatal(err)
	}
	if log.Ahead != 2 || log.Behind != 2 || !reflect.DeepEqual(log.Conflicts, []protocol.RunGitConflict{{Path: "selected"}}) {
		t.Fatalf("moved base: ahead=%d behind=%d conflicts=%v", log.Ahead, log.Behind, log.Conflicts)
	}
	if got := subjects(log.Upstream); !reflect.DeepEqual(got, []string{"upstream conflicting", "upstream clean"}) {
		t.Fatalf("upstream = %v", got)
	}
	if !reflect.DeepEqual(log.Upstream[0].Paths, []string{"selected"}) || log.Upstream[1].Paths != nil {
		t.Fatalf("upstream conflict paths = %v / %v", log.Upstream[0].Paths, log.Upstream[1].Paths)
	}
	if status := gitCmd(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("conflict prediction touched the worktree: %q", status)
	}

	gitCmd(t, dir, "checkout", "-q", "-b", "side", "upstream~1")
	writeFile(t, dir, "unrelated", "side change\n")
	gitCmd(t, dir, "commit", "-q", "-am", "side conflicting")
	gitCmd(t, dir, "checkout", "-q", "upstream")
	gitCmd(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "update-ref", "refs/remotes/origin/main", "upstream")
	gitCmd(t, dir, "checkout", "-q", "aether/run")
	if log, err = s.Log(t.Context(), run, request); err != nil {
		t.Fatal(err)
	}
	mergePaths := []string{"missing"}
	for _, commit := range log.Upstream {
		if commit.SHA == merge {
			mergePaths = commit.Paths
		}
	}
	if !reflect.DeepEqual(mergePaths, []string{"unrelated"}) {
		t.Fatalf("merge commit paths = %v, want the conflicting path it brings in", mergePaths)
	}

	request.Limit = 1
	if log, err = s.Log(t.Context(), run, request); err != nil || !log.Truncated || len(log.Commits) != 1 || len(log.Upstream) != 1 {
		t.Fatalf("limited log = %+v, %v", log, err)
	}
}

func TestLogFallsBackToForkPointAndReportsMissingCheckout(t *testing.T) {
	s, run, initial := newRepo(t)
	writeFile(t, run.WorkDir, "selected", "change\n")
	gitCmd(t, run.WorkDir, "commit", "-q", "-am", "change")
	log, err := s.Log(t.Context(), run, LogRequest{BaseBranch: "develop", ForkPoint: initial.Head})
	if err != nil {
		t.Fatal(err)
	}
	if log.BaseRef != "" || log.Base != initial.Head || log.Ahead != 1 || log.Behind != 0 {
		t.Fatalf("fork-point log = %+v", log)
	}
	gitCmd(t, run.WorkDir, "update-ref", "refs/remotes/origin/develop", gitCmd(t, run.WorkDir, "commit-tree", "-m", "unrelated", gitCmd(t, run.WorkDir, "mktree")))
	log, err = s.Log(t.Context(), run, LogRequest{BaseBranch: "develop"})
	if err != nil || log.MergeBase != "" || log.Ahead != 2 || log.Behind != 1 || len(log.Conflicts) != 0 || !strings.Contains(log.ConflictsError, "unrelated histories") {
		t.Fatalf("unrelated base log = %+v, %v", log, err)
	}
	gitCmd(t, run.WorkDir, "update-ref", "-d", "refs/remotes/origin/develop")
	if _, err = s.Log(t.Context(), run, LogRequest{BaseBranch: "develop"}); err == nil || !strings.Contains(err.Error(), "origin/develop is missing") {
		t.Fatalf("no base error = %v", err)
	}
	if _, err = s.Log(t.Context(), run, LogRequest{Limit: protocol.MaxRunGitLogLimit + 1}); err == nil {
		t.Fatal("oversized limit accepted")
	}
	if err = os.RemoveAll(run.WorkDir); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Log(t.Context(), run, LogRequest{BaseBranch: "main", ForkPoint: initial.Head}); err == nil {
		t.Fatal("log of a removed checkout succeeded")
	}
}

func fakeGH(merge func() (int, string), pull string, calls *[][]string) ExecFunc {
	return func(_ context.Context, _ runtime.ID, argv []string, _ string) (int, string, string, error) {
		native := slices.Clone(argv[slices.Index(argv, "gh"):])
		*calls = append(*calls, native)
		if native[1] == "pr" {
			code, stderr := merge()
			return code, "", stderr, nil
		}
		if slices.Contains(native, "user") {
			return 0, "octo\n", "", nil
		}
		return 0, pull, "", nil
	}
}

const openPull = `{"number":7,"html_url":"https://github.com/acme/app/pull/7","state":"open","title":"Run","draft":false,"merged":false,"merged_at":null,"base":{"ref":"main","repo":{"full_name":"acme/app"}},"head":{"ref":"aether/run","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"full_name":"acme/app"}}}`

func TestMergePRKeepsGHStderrAndRereadsState(t *testing.T) {
	const refusal = "X Pull request acme/app#7 is not mergeable: the base branch policy prohibits the merge.\n"
	var calls [][]string
	run := Execution{ContainerID: "test", WorkDir: "/work", Authorize: func(context.Context, bool) error { return nil }}
	s := New(fakeGH(func() (int, string) { return 1, refusal }, openPull, &calls))
	result, err := s.MergePR(t.Context(), run, PRMergeRequest{Repository: "acme/app", Number: 7, Method: "squash", ExpectedHead: "0123456789abcdef0123456789abcdef01234567"})
	if err == nil || !strings.Contains(err.Error(), refusal) || result.Output.Stderr != refusal || result.Output.ExitCode != 1 {
		t.Fatalf("merge failure = %+v, %v", result.Output, err)
	}
	if result.PullRequest == nil || result.PullRequest.State != "open" || result.PullRequest.Target.HeadBranch != "aether/run" {
		t.Fatalf("pull request after refusal = %+v", result.PullRequest)
	}
	if result.Identity != "octo" {
		t.Fatalf("identity = %q", result.Identity)
	}
	if !reflect.DeepEqual(calls[1], []string{"gh", "pr", "merge", "7", "--repo", "github.com/acme/app", "--squash", "--match-head-commit", "0123456789abcdef0123456789abcdef01234567"}) {
		t.Fatalf("merge argv = %v", calls[0])
	}

	calls = nil
	s = New(fakeGH(func() (int, string) { t.Fatal("merged a retargeted pull request"); return 0, "" }, openPull, &calls))
	result, err = s.MergePR(t.Context(), run, PRMergeRequest{Repository: "acme/app", Number: 7, Method: "squash", ExpectedHead: "0123456789abcdef0123456789abcdef01234567", ExpectedBase: "release"})
	if err == nil || !strings.Contains(err.Error(), "pull request #7 now targets main, not release") || result.PullRequest == nil {
		t.Fatalf("retargeted merge = %+v, %v", result, err)
	}

	merged := strings.NewReplacer(`"state":"open"`, `"state":"closed"`, `"merged":false`, `"merged":true`, `"merged_at":null`, `"merged_at":"2026-10-20T01:02:03Z"`).Replace(openPull)
	s = New(fakeGH(func() (int, string) { return 0, "" }, merged, &calls))
	result, err = s.MergePR(t.Context(), run, PRMergeRequest{Repository: "acme/app", Number: 7, Method: "rebase", ExpectedHead: "0123456789abcdef0123456789abcdef01234567"})
	if err != nil || result.PullRequest == nil || result.PullRequest.State != "merged" || result.PullRequest.MergedAt == nil {
		t.Fatalf("merge = %+v, %v", result.PullRequest, err)
	}

	calls = nil
	if _, err = s.MergePR(t.Context(), run, PRMergeRequest{Repository: "acme/app", Number: 7, Method: "merge", ExpectedHead: "0123456789abcdef0123456789abcdef01234567", ExpectedLogin: "someone-else"}); err == nil || !strings.Contains(err.Error(), "identity changed") || slices.ContainsFunc(calls, func(c []string) bool { return c[1] == "pr" }) {
		t.Fatalf("merge under a switched login = %v, calls %v", err, calls)
	}

	calls = nil
	for _, req := range []PRMergeRequest{{Repository: "acme/app", Number: 7, Method: "auto"}, {Repository: "acme", Number: 7, Method: "merge"}, {Repository: "acme/app", Method: "merge"}, {Repository: "acme/app", Number: 7, Method: "merge", ExpectedHead: "HEAD"}} {
		if _, err := s.MergePR(t.Context(), run, req); err == nil {
			t.Fatalf("invalid merge request %+v accepted", req)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("invalid requests ran %v", calls)
	}
}
