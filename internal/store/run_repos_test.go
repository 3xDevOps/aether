package store

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunPullRequestsKeepTheirOwningRepoWorkspace(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	run := mustCreateRun(t, db, mustCreateWorkspace(t, db).ID, mustCreateMember(t, db).ID, domain.RunCompleted)
	extra := mustCreateWorkspace(t, db)
	if err := db.SetRunRepos(ctx, run.ID, []domain.RunRepo{{WorkspaceID: extra.ID, Name: "lib", Path: "/repos/lib", BaseBranch: "main", BaseCommit: "abc"}}); err != nil {
		t.Fatal(err)
	}
	for _, pr := range []*RunPullRequest{
		{RunID: run.ID, Repository: "acme/app", Number: 1, URL: "u1", State: "open", ObservedAt: time.Now()},
		{RunID: run.ID, RepoWorkspace: extra.ID, Repository: "acme/lib", Number: 2, URL: "u2", State: "open", ObservedAt: time.Now()},
	} {
		if err := db.RecordRunPullRequest(ctx, pr); err != nil {
			t.Fatal(err)
		}
	}
	prs, err := db.RunPullRequests(ctx, run.ID)
	if err != nil || len(prs) != 2 || prs[0].RepoWorkspace != extra.ID || prs[1].RepoWorkspace != "" {
		t.Fatalf("pull requests = %+v, %v", prs, err)
	}
	ids, err := db.RunsWithExtraRepo(ctx, extra.ID)
	if err != nil || len(ids) != 1 || ids[0] != run.ID {
		t.Fatalf("RunsWithExtraRepo = %v, %v", ids, err)
	}
}
