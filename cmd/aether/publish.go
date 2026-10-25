package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "publish",
		short: "push a run's branch and open a pull request into its base branch, as the dashboard's Publish does",
		run:   runPublish,
	})
}

func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	repo := fs.String("repo", "", "publish this extra repository of a multi-repo run, by workspace ID or name (default: the primary)")
	runID, err := parseLeadingArg(fs, args)
	if err != nil || runID == "" {
		return fmt.Errorf("usage: aether publish <run-id> [--repo <workspace>]")
	}
	return withControl(func(c *protocol.Client) error {
		return publish(c, os.Stdout, os.Stderr, runID, *repo)
	})
}

func repoFailure(runID, message string, out protocol.RunRepoCommandOutput, actual *protocol.RunGitExpected) error {
	lines := []string{message}
	for _, s := range []string{out.Stderr, out.Stdout} {
		if s = strings.TrimSpace(s); s != "" {
			lines = append(lines, s)
		}
	}
	if actual != nil {
		lines = append(lines, fmt.Sprintf("the checkout is now on %s at %s; review it, then run aether publish %s again", actual.Branch, actual.Head, runID))
	}
	return errors.New(strings.Join(lines, "\n"))
}

// publishGap words each gap as web/src/routes/diff/publish-push.tsx does.
func publishGap(runID string, st protocol.RunGitStatusResult, push protocol.RunGitPushTarget) error {
	terminal := fmt.Sprintf("in the run's terminal (aether attach %s), then run aether publish %s again", runID, runID)
	switch {
	case len(st.Remotes) == 0:
		return fmt.Errorf("this checkout has no remote. Add one with git %s", terminal)
	case push.Remote == "":
		return fmt.Errorf("this workspace sets no checkout Origin, so no destination is chosen for you. Set one with aether workspace origin <url>, or set the branch's upstream with git %s", terminal)
	case push.Repository == "":
		return fmt.Errorf("remote %s does not have exactly one push URL. Leave one with git remote set-url %s", push.Remote, terminal)
	}
	return nil
}

func publish(c *protocol.Client, w, notes io.Writer, runID, repoName string) error {
	r, err := getRun(c, runID)
	if err != nil {
		return err
	}
	var list protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
		return err
	}
	repo, wsID, base := "", r.WorkspaceID, r.BaseBranch
	if repoName != "" {
		id, err := workspaceIDIn(list.Workspaces, repoName)
		if err != nil {
			return fmt.Errorf("--repo %s: %w", repoName, err)
		}
		if id != r.WorkspaceID {
			if !slices.ContainsFunc(r.Repos, func(rr protocol.RunRepo) bool { return rr.WorkspaceID == id }) {
				return fmt.Errorf("--repo %s is not one of run %s's repos", repoName, r.ID)
			}
			repo, wsID, base = id, id, ""
		}
	}
	i := slices.IndexFunc(list.Workspaces, func(ws protocol.Workspace) bool { return ws.ID == wsID })
	if i < 0 {
		return fmt.Errorf("workspace %s of run %s not found", wsID, r.ID)
	}
	ws := list.Workspaces[i]
	base = cmp.Or(base, ws.BaseBranch)

	var st protocol.RunGitStatusResult
	if err := c.Call(protocol.MethodRunGitStatus, protocol.RunGitStatusParams{Repo: repo, RunID: r.ID}, &st); err != nil {
		return err
	}
	if st.Error != "" {
		return repoFailure(r.ID, st.Error, st.Output, nil)
	}
	if st.Detached || st.Branch == "" {
		return fmt.Errorf("the checkout is on a detached HEAD at %s. Check out a branch in the run's terminal (aether attach %s), then run aether publish %s again", st.Head, r.ID, r.ID)
	}
	if repo == "" && st.Head == r.BaseCommit {
		return fmt.Errorf("run %s's branch %s has no commit beyond its base %s; nothing to publish", r.ID, st.Branch, r.BaseCommit)
	}
	push := protocol.DefaultPushTarget(st.Remotes, st.Upstream, st.Branch, ws.Origin != "")
	if err := publishGap(r.ID, st, push); err != nil {
		return err
	}
	if len(st.Changes) > 0 {
		_, _ = fmt.Fprintf(notes, "%s not pushed; aether message %s asks the agent to commit them\n", plural(len(st.Changes), "uncommitted change"), r.ID)
	}
	expected := protocol.RunGitExpected{Branch: st.Branch, Head: st.Head}
	var mirror protocol.WorkspaceMirrorResult
	if err := c.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: wsID}, &mirror); err != nil {
		return err
	}
	target := protocol.DefaultPRTarget(push, base, mirror.SourceURL)

	var found protocol.RunPRStatusResult
	if target != (protocol.RunPRTarget{}) {
		if err := c.Call(protocol.MethodRunPRStatus, protocol.RunPRStatusParams{Repo: repo, RunID: r.ID, Expected: expected, Target: target}, &found); err != nil {
			return err
		}
		if found.Error != "" || found.Actual != nil {
			return repoFailure(r.ID, cmp.Or(found.Error, "the checkout moved"), found.Output, found.Actual)
		}
		if found.PullRequest == nil && strings.TrimSpace(st.HeadSubject) == "" {
			return fmt.Errorf("a pull request needs a title, and the head commit %s has no subject. Amend it in the run's terminal (aether attach %s), then run aether publish %s again", st.Head, r.ID, r.ID)
		}
		if found.Identity == "" {
			detail := ""
			if st.IdentityError != "" {
				detail = " (" + st.IdentityError + ")"
			}
			return fmt.Errorf("a pull request is opened with the run account's GitHub login, and it has none%s. Connect GitHub (gh auth login in aether terminal, then aether github connect), then run aether publish %s again", detail, r.ID)
		}
	}

	var pushed protocol.RunGitPushResult
	if err := c.Call(protocol.MethodRunGitPush, protocol.RunGitPushParams{Repo: repo, RunID: r.ID, Expected: expected, Target: push}, &pushed); err != nil {
		return err
	}
	if !pushed.Pushed {
		return repoFailure(r.ID, cmp.Or(pushed.Error, "the push was refused"), pushed.Output, pushed.Actual)
	}
	if _, err := fmt.Fprintf(w, "pushed commit %.8s to %s as branch %s (never a force push)\n", pushed.Head, pushed.Target.Repository, pushed.Target.HeadBranch); err != nil {
		return err
	}
	switch {
	case target == (protocol.RunPRTarget{}):
		_, err := fmt.Fprintf(w, "no pull request opened: %s is not a github.com repository\n", push.Repository)
		return err
	case found.PullRequest != nil:
		pr := found.PullRequest
		_, err := fmt.Fprintf(w, "pull request #%d is %s: %s\n", pr.Number, strings.ToLower(pr.State), pr.URL)
		return err
	}

	var created protocol.RunPRCreateResult
	if err := c.Call(protocol.MethodRunPRCreate, protocol.RunPRCreateParams{Repo: repo, RunID: r.ID, Expected: expected, Target: target,
		Title: st.HeadSubject, ExpectedLogin: found.Identity}, &created); err != nil {
		return err
	}
	switch {
	case created.PullRequest != nil:
		verb := "opened"
		if !created.Created {
			verb = "found"
		}
		_, err := fmt.Fprintf(w, "%s pull request #%d into %s:%s: %s\n", verb, created.PullRequest.Number, target.Repository, target.BaseBranch, created.PullRequest.URL)
		return err
	case created.CreationUncertain:
		return repoFailure(r.ID, fmt.Sprintf("creation outcome is uncertain: %s\nthe push stands. Run aether publish %s again: it looks for the pull request before it opens one", created.Error, r.ID), created.Output, created.Actual)
	}
	return repoFailure(r.ID, cmp.Or(created.Error, "the pull request was not opened")+"\nthe push stands", created.Output, created.Actual)
}
