package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func publishControl(t *testing.T, found *protocol.RunPullRequest) (*protocol.Client, map[string]*fakeReply) {
	t.Helper()
	replies := map[string]*fakeReply{
		protocol.MethodRunGet: {result: protocol.RunResult{Run: protocol.Run{ID: "r1", Status: "running", WorkspaceID: "ws1", BaseBranch: "main", BaseCommit: "base000"}}},
		protocol.MethodWorkspaceList: {result: protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{
			{ID: "ws1", Name: "api", BaseBranch: "main", Origin: "https://github.com/me/api.git"}}}},
		protocol.MethodRunGitStatus: {result: protocol.RunGitStatusResult{Branch: "aether/run-x", Head: "1a2b3c4d5e6f", HeadSubject: "add a health check",
			Remotes: []protocol.RunGitRemote{{Name: "origin", PushURLs: []string{"https://github.com/me/api.git"}}},
			Changes: []protocol.RunGitChange{{Path: "notes.txt", Untracked: true}}}},
		protocol.MethodWorkspaceMirrorStatus: {result: protocol.WorkspaceMirrorResult{SourceURL: "https://github.com/acme/api.git"}},
		protocol.MethodRunPRStatus:           {result: protocol.RunPRStatusResult{Identity: "alice", PullRequest: found}},
		protocol.MethodRunGitPush: {result: protocol.RunGitPushResult{Pushed: true, Head: "1a2b3c4d5e6f",
			Target: protocol.RunGitPushTarget{Remote: "origin", Repository: "https://github.com/me/api.git", HeadBranch: "aether/run-x"}}},
	}
	if found == nil {
		replies[protocol.MethodRunPRCreate] = &fakeReply{result: protocol.RunPRCreateResult{Created: true,
			PullRequest: &protocol.RunPullRequest{Number: 12, URL: "https://github.com/acme/api/pull/12"}}}
	}
	return fakeControlMethods(t, replies), replies
}

// aether publish pushes where the dashboard's Publish does, then opens the
// pull request into the workspace source, a fork's upstream here.
func TestPublishPushesThenOpensAPullRequest(t *testing.T) {
	c, replies := publishControl(t, nil)
	var out, notes bytes.Buffer
	if err := publish(c, &out, &notes, "r1", ""); err != nil {
		t.Fatal(err)
	}
	want := "pushed commit 1a2b3c4d to https://github.com/me/api.git as branch aether/run-x (never a force push)\n" +
		"opened pull request #12 into acme/api:main: https://github.com/acme/api/pull/12\n"
	if got := out.String(); got != want {
		t.Fatalf("publish printed %q\nwant %q", got, want)
	}
	if got := notes.String(); got != "1 uncommitted change is not pushed; aether message r1 asks the agent to commit them\n" {
		t.Fatalf("publish notes = %q", got)
	}
	expected := `"expected":{"branch":"aether/run-x","head":"1a2b3c4d5e6f"}`
	for method, want := range map[string]string{
		protocol.MethodRunGitPush: `{"run_id":"r1",` + expected + `,"target":{"remote":"origin","repository":"https://github.com/me/api.git","head_branch":"aether/run-x"}}`,
		protocol.MethodRunPRCreate: `{"run_id":"r1",` + expected + `,"target":{"repository":"acme/api","base_branch":"main","head_repository":"me/api","head_branch":"aether/run-x"},` +
			`"title":"add a health check","body":"","expected_login":"alice"}`,
	} {
		if got := string(replies[method].params()); got != want {
			t.Errorf("%s params = %s\nwant %s", method, got, want)
		}
	}
}

func TestPublishShowsAnOpenPullRequestInsteadOfOpeningAnother(t *testing.T) {
	c, _ := publishControl(t, &protocol.RunPullRequest{Number: 3, State: "OPEN", URL: "https://github.com/acme/api/pull/3"})
	var out bytes.Buffer
	if err := publish(c, &out, &bytes.Buffer{}, "r1", ""); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasSuffix(got, "pull request #3 is open: https://github.com/acme/api/pull/3\n") {
		t.Fatalf("publish printed %q", got)
	}
}

func TestPublishOpensANewPullRequestWhenTheFoundOneWasClosed(t *testing.T) {
	_, replies := publishControl(t, &protocol.RunPullRequest{Number: 3, State: "closed", URL: "https://github.com/acme/api/pull/3"})
	replies[protocol.MethodRunPRCreate] = &fakeReply{result: protocol.RunPRCreateResult{Created: true,
		PullRequest: &protocol.RunPullRequest{Number: 4, URL: "https://github.com/acme/api/pull/4"}}}
	c := fakeControlMethods(t, replies)
	var out bytes.Buffer
	if err := publish(c, &out, &bytes.Buffer{}, "r1", ""); err != nil {
		t.Fatal(err)
	}
	if replies[protocol.MethodRunPRCreate].params() == nil {
		t.Fatal("run.pr.create was not called")
	}
	if got := out.String(); !strings.HasSuffix(got, "opened pull request #4 into acme/api:main: https://github.com/acme/api/pull/4\n") {
		t.Fatalf("publish printed %q", got)
	}
}

func TestPublishRefusesAFinishedRunAndNamesReopen(t *testing.T) {
	c, replies := publishControl(t, nil)
	replies[protocol.MethodRunGet].answer(protocol.RunResult{Run: protocol.Run{ID: "r1", Status: "completed", WorkspaceID: "ws1"}})
	err := publish(c, &bytes.Buffer{}, &bytes.Buffer{}, "r1", "")
	if err == nil || !strings.Contains(err.Error(), "aether reopen r1, then aether publish r1") || !strings.Contains(err.Error(), "aether pull r1") {
		t.Fatalf("publish error = %v", err)
	}
	for _, m := range []string{protocol.MethodRunGitStatus, protocol.MethodRunGitPush, protocol.MethodRunPRCreate} {
		if replies[m].params() != nil {
			t.Errorf("%s was called for a finished run", m)
		}
	}
}

// An extra repository opens its pull request into the base branch the run
// captured for it, not its workspace's current base.
func TestPublishExtraRepoTargetsItsCapturedBase(t *testing.T) {
	c, replies := publishControl(t, nil)
	replies[protocol.MethodRunGet].answer(protocol.RunResult{Run: protocol.Run{ID: "r1", Status: "running", WorkspaceID: "ws0", BaseBranch: "main", BaseCommit: "base000",
		Repos: []protocol.RunRepo{{WorkspaceID: "ws0", Primary: true, BaseBranch: "main"}, {WorkspaceID: "ws1", Name: "api", BaseBranch: "release"}}}})
	var out, notes bytes.Buffer
	if err := publish(c, &out, &notes, "r1", "api"); err != nil {
		t.Fatal(err)
	}
	if got := string(replies[protocol.MethodRunPRCreate].params()); !strings.Contains(got, `"base_branch":"release"`) || !strings.Contains(got, `"repo":"ws1"`) {
		t.Fatalf("run.pr.create params = %s, want base release on repo ws1", got)
	}
}
