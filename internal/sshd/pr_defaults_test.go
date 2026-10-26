package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runrepo"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestRunPRStatusDefaultsExpectedAndTarget(t *testing.T) {
	fixture, expected := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	if err := e.store.SetWorkspaceOrigin(t.Context(), e.ws.ID, "https://github.com/acme/app.git"); err != nil {
		t.Fatal(err)
	}
	repoGit(t, fixture.dir, "remote", "add", "origin", "https://github.com/acme/app.git")
	repoGit(t, fixture.dir, "checkout", "-q", "-b", "aether/run-x")
	var lookups []string
	fixture.native = runrepo.New(func(ctx context.Context, id runtime.ID, argv []string, dir string) (int, string, string, error) {
		gh := slices.Index(argv, "gh")
		if gh < 0 {
			return repoLocalExec(ctx, id, argv, dir)
		}
		if slices.Contains(argv, "user") {
			return 0, "octo\n", "", nil
		}
		lookups = append(lookups, strings.Join(argv[gh:], " "))
		return 0, `[{"number":7,"html_url":"https://github.com/acme/app/pull/7","state":"open","title":"Fix","draft":false,"merged_at":null,"base":{"ref":"main","repo":{"full_name":"acme/app"}},"head":{"ref":"aether/run-x","sha":"` + expected.Head + `","repo":{"full_name":"acme/app"}}}]`, "", nil
	})
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunPRStatus, repoParams(t, map[string]string{"run_id": string(e.run.ID)}))
	if perr != nil {
		t.Fatal(perr)
	}
	var status protocol.RunPRStatusResult
	if err := json.Unmarshal(value, &status); err != nil {
		t.Fatal(err)
	}
	if status.Error != "" || status.PullRequest == nil || status.PullRequest.Number != 7 || status.PullRequest.HeadBranch != "aether/run-x" {
		t.Fatalf("run.pr.status = %+v", status)
	}
	if len(lookups) != 1 || !strings.Contains(lookups[0], "repos/acme/app/pulls?") || !strings.Contains(lookups[0], "base=main") || !strings.Contains(lookups[0], "head=acme%3Aaether%2Frun-x") {
		t.Fatalf("lookups = %v, want acme/app pulls for acme:aether/run-x into main", lookups)
	}
}

type goneRepoFixture struct{ *nativeRepoFixture }

func (goneRepoFixture) Execution(context.Context, domain.Run) (runrepo.Execution, error) {
	return runrepo.Execution{}, scheduler.ErrNoLiveEnvironment
}

func TestRunPRFeedbackReportsTheRecordedPRWhenTheEnvironmentIsGone(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = goneRepoFixture{fixture} })
	if _, err := e.store.UpdateRunStatus(t.Context(), e.run.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.store.RecordRunPullRequest(t.Context(), &store.RunPullRequest{RunID: e.run.ID, Repository: "acme/app", Number: 7, URL: "https://github.com/acme/app/pull/7", State: "open", ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunPRFeedback, repoParams(t, map[string]string{"run_id": string(e.run.ID)}))
	if perr != nil {
		t.Fatal(perr)
	}
	var feedback protocol.RunPRFeedbackResult
	if err := json.Unmarshal(value, &feedback); err != nil {
		t.Fatal(err)
	}
	pr := feedback.PullRequest
	if pr == nil || pr.Number != 7 || pr.URL != "https://github.com/acme/app/pull/7" || pr.State != "open" || !strings.Contains(feedback.Error, "environment") {
		t.Fatalf("run.pr.feedback = %+v", feedback)
	}
}

type mirrorReadFails struct{ store.Store }

func (mirrorReadFails) GetWorkspaceMirror(context.Context, domain.WorkspaceID) (*domain.WorkspaceMirror, error) {
	return nil, errors.New("database is locked")
}

func TestRunPRStatusDefaultsExplainWhatTheyNeed(t *testing.T) {
	fixture, _ := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	repoGit(t, fixture.dir, "remote", "add", "origin", "https://github.com/acme/app.git")
	status := func() (protocol.RunPRStatusResult, *protocol.Error) {
		t.Helper()
		value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunPRStatus, repoParams(t, map[string]string{"run_id": string(e.run.ID)}))
		var result protocol.RunPRStatusResult
		if perr == nil {
			if err := json.Unmarshal(value, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result, perr
	}

	e.srv.cfg.Store = mirrorReadFails{e.store}
	if _, perr := status(); perr == nil || !strings.Contains(perr.Message, "database is locked") {
		t.Fatalf("run.pr.status with a failing store = %v, want the store error as an RPC error", perr)
	}
	e.srv.cfg.Store = e.store

	repoGit(t, fixture.dir, "checkout", "-q", "--detach")
	result, perr := status()
	if perr != nil || !strings.Contains(result.Error, "detached HEAD") || !strings.Contains(result.Error, "pass both") {
		t.Fatalf("run.pr.status on a detached HEAD = %+v, %v", result, perr)
	}
}

func TestRunPRCreateDefaultsTheTarget(t *testing.T) {
	fixture, expected := newNativeRepoFixture(t)
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.RunRepo = fixture })
	if err := e.store.SetWorkspaceOrigin(t.Context(), e.ws.ID, "https://github.com/acme/app.git"); err != nil {
		t.Fatal(err)
	}
	repoGit(t, fixture.dir, "remote", "add", "origin", "https://github.com/acme/app.git")
	repoGit(t, fixture.dir, "checkout", "-q", "-b", "aether/run-x")
	expected.Branch = "aether/run-x"
	var posted string
	fixture.native = runrepo.New(func(ctx context.Context, id runtime.ID, argv []string, dir string) (int, string, string, error) {
		gh := slices.Index(argv, "gh")
		switch {
		case gh < 0:
			return repoLocalExec(ctx, id, argv, dir)
		case slices.Contains(argv, "user"):
			return 0, "octo\n", "", nil
		case slices.Contains(argv, "POST"):
			posted = strings.Join(argv[gh:], " ")
			return 0, `{"number":8,"html_url":"https://github.com/acme/app/pull/8","state":"open","title":"Fix","draft":false,"merged_at":null,"base":{"ref":"main","repo":{"full_name":"acme/app"}},"head":{"ref":"aether/run-x","sha":"` + expected.Head + `","repo":{"full_name":"acme/app"}}}`, "", nil
		case slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, "/git/ref/heads/") }):
			return 0, expected.Head + "\n", "", nil
		}
		return 0, "[]", "", nil
	})
	value, perr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodRunPRCreate, repoParams(t, protocol.RunPRCreateParams{RunID: string(e.run.ID), Expected: expected, Title: "Fix", Body: "b"}))
	if perr != nil {
		t.Fatal(perr)
	}
	var created protocol.RunPRCreateResult
	if err := json.Unmarshal(value, &created); err != nil {
		t.Fatal(err)
	}
	if created.Error != "" || !created.Created || created.PullRequest == nil || created.PullRequest.Number != 8 {
		t.Fatalf("run.pr.create = %+v", created)
	}
	if !strings.Contains(posted, "repos/acme/app/pulls") || !strings.Contains(posted, "base=main") || !strings.Contains(posted, "head=acme:aether/run-x") {
		t.Fatalf("POST = %q, want acme/app pulls for acme:aether/run-x into main", posted)
	}
}
