package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runrepo"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

// RunRepoService resolves only a stored live run. The transport supplies human
// authority, never a caller-selected container/account/workdir. This seam is
// deliberately absent from the run-agent development dispatcher.
type RunRepoService interface {
	Native() *runrepo.Service
	Execution(context.Context, domain.Run) (runrepo.Execution, error)
}

func init() {
	registerGuarded(protocol.MethodRunGitStatus, permissions.View, runTarget, (*Server).runGitStatus)
	registerGuarded(protocol.MethodRunGitDiff, permissions.View, runTarget, (*Server).runGitDiff)
	registerGuarded(protocol.MethodRunGitCommit, permissions.Push, runTarget, (*Server).runGitCommit)
	registerGuarded(protocol.MethodRunGitPush, permissions.Push, runTarget, (*Server).runGitPush)
	registerGuarded(protocol.MethodRunGitLog, permissions.View, runTarget, (*Server).runGitLog)
	registerGuarded(protocol.MethodRunPRStatus, permissions.View, runTarget, (*Server).runPRStatus)
	registerGuarded(protocol.MethodRunPRCreate, permissions.Push, runTarget, (*Server).runPRCreate)
	registerGuarded(protocol.MethodRunPRFeedback, permissions.View, runTarget, (*Server).runPRFeedback)
	registerGuarded(protocol.MethodRunPRMerge, permissions.Push, runTarget, (*Server).runPRMerge)
}

func decodeRunRepo(raw json.RawMessage, dst any) *protocol.Error {
	if len(raw) == 0 || len(raw) > protocol.MaxRunRepoParamsBytes || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalidParams("repository params must be a bounded JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return invalidParams("invalid repository params: " + err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalidParams("repository params must contain one JSON object")
	}
	return nil
}

type runRepoCall struct {
	run         domain.Run
	repo        domain.Run
	native      *runrepo.Service
	execution   runrepo.Execution
	account     domain.MemberID
	home        domain.MemberID
	accountName string
}

// repoOwner is the workspace of the extra repository the call acts on, empty
// for the run's own.
func (c runRepoCall) repoOwner() domain.WorkspaceID {
	if c.repo.WorkspaceID == c.run.WorkspaceID {
		return ""
	}
	return c.repo.WorkspaceID
}

func (s *Server) runRepoAuthority(ctx context.Context, member domain.MemberID, runID, repoID string, mutation bool) (runRepoCall, error) {
	var call runRepoCall
	if runID == "" {
		return call, invalidParams("run_id is required")
	}
	if s.cfg.Services.RunRepo == nil || s.cfg.Services.RunRepo.Native() == nil {
		return call, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run repository service unavailable"}
	}
	run, runErr := s.cfg.Store.GetRun(ctx, domain.RunID(runID))
	if runErr != nil {
		return call, runErr
	}
	repo, repoErr := repoView(run, repoID)
	if repoErr != nil {
		return call, repoErr
	}
	account := run.AccountMember()
	authorize := func(ctx context.Context, mutate bool) error {
		return s.lockAuthorization(func() error {
			if memberErr := s.checkMember(ctx, member); memberErr != nil {
				return memberErr
			}
			actor, actorErr := resolveActor(ctx, s.cfg.Store, member)
			if actorErr != nil {
				return actorErr
			}
			target, targetErr := resolveRunTarget(ctx, s.cfg.Store, run.ID)
			if targetErr != nil {
				return targetErr
			}
			capability := permissions.View
			if mutate {
				capability = permissions.Push
			}
			if capabilityErr := permissions.Check(capability, actor, target); capabilityErr != nil {
				return capabilityErr
			}
			// Editing a protected/other member's checkout is also steering it.
			if mutate {
				if steeringErr := permissions.Check(permissions.Steer, actor, target); steeringErr != nil {
					return steeringErr
				}
			}
			current, currentErr := s.cfg.Store.GetRun(ctx, run.ID)
			if currentErr != nil {
				return currentErr
			}
			if current.AccountMember() != account {
				return fmt.Errorf("%w: run account changed; refresh before acting", permissions.ErrDenied)
			}
			_, accountErr := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, member, string(account))
			return accountErr
		})
	}
	if authorityErr := authorize(ctx, mutation); authorityErr != nil {
		return call, authorityErr
	}
	execution, executionErr := s.cfg.Services.RunRepo.Execution(ctx, *run)
	if executionErr != nil {
		call.run, call.repo, call.account, call.home = *run, repo, account, run.HomeMember()
		return call, executionErr
	}
	if repo.WorkspaceID != run.WorkspaceID {
		extra, _ := run.ExtraRepo(repo.WorkspaceID)
		execution.WorkDir = extra.Path
	}
	liveAuthorize := execution.Authorize
	if liveAuthorize == nil {
		return call, errors.New("run repository execution has no live authorization fence")
	}
	execution.Authorize = func(ctx context.Context, mutate bool) error {
		if liveAuthorityErr := authorize(ctx, mutate); liveAuthorityErr != nil {
			return liveAuthorityErr
		}
		return liveAuthorize(ctx, mutate)
	}
	owner, ownerErr := s.cfg.Store.GetMember(ctx, account)
	if ownerErr != nil {
		return call, ownerErr
	}
	return runRepoCall{run: *run, repo: repo, native: s.cfg.Services.RunRepo.Native(), execution: execution, account: account, accountName: owner.DisplayName}, nil
}

func repoView(run *domain.Run, repoID string) (domain.Run, error) {
	if repoID == "" || domain.WorkspaceID(repoID) == run.WorkspaceID {
		return *run, nil
	}
	extra, ok := run.ExtraRepo(domain.WorkspaceID(repoID))
	if !ok {
		return domain.Run{}, invalidParams("repo " + repoID + " is not one of the run's repos")
	}
	view := *run
	view.WorkspaceID, view.Branch, view.Worktree = extra.WorkspaceID, extra.Branch, extra.Worktree
	view.BaseBranch, view.BaseCommit = extra.BaseBranch, extra.BaseCommit
	return view, nil
}

func repoError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func repoActual(err error) *protocol.RunGitExpected {
	var stale *runrepo.StaleError
	if errors.As(err, &stale) {
		return &stale.Actual
	}
	return nil
}

func publicPR(pr *runrepo.PullRequest) *protocol.RunPullRequest {
	if pr == nil {
		return nil
	}
	return &protocol.RunPullRequest{Number: pr.Number, URL: pr.URL, State: pr.State, Title: pr.Title, Draft: pr.Draft, Repository: pr.Target.Repository, BaseBranch: pr.Target.BaseBranch, HeadRepository: pr.Target.HeadRepository, HeadBranch: pr.Target.HeadBranch, HeadOID: pr.Head, MergedAt: pr.MergedAt}
}

func (s *Server) runGitStatus(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitStatusParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, false)
	if err != nil {
		return nil, rpcError(err)
	}
	state, err := call.native.Status(ctx, call.execution)
	result := protocol.RunGitStatusResult{Repo: p.Repo, Branch: state.Branch, Head: state.Head, Detached: state.Detached, Unborn: state.Unborn, Changes: state.Changes, AccountMemberID: string(call.account), AccountName: call.accountName, Remotes: []protocol.RunGitRemote{}}
	if err == nil && !state.Unborn {
		result.HeadSubject, err = call.native.Subject(ctx, call.execution, state.Head)
	}
	if err == nil {
		result.Remotes, result.Upstream, err = call.native.Remotes(ctx, call.execution, state.Branch)
	}
	if err != nil {
		result.Output = runrepo.FailureOutput(result.Output, err)
		result.Truncated = errors.Is(err, runrepo.ErrTruncated) || result.Output.Truncated
		result.Error = repoError(err)
		return result, nil
	}
	result.Identity, err = call.native.Identity(ctx, call.execution)
	if err != nil {
		// A missing GitHub login does not hide otherwise useful local Git
		// state. Permission/lifecycle revocation still fails the operation.
		var commandErr *runrepo.CommandError
		if errors.As(err, &commandErr) {
			result.IdentityError = repoError(err)
		} else {
			result.Error = repoError(err)
		}
		result.Output = runrepo.FailureOutput(result.Output, err)
	}
	return result, nil
}

func (s *Server) runGitDiff(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitDiffParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, false)
	if err != nil {
		return nil, rpcError(err)
	}
	diff, err := call.native.Diff(ctx, call.execution, runrepo.DiffRequest{Paths: p.Paths, Staged: p.Staged})
	return protocol.RunGitDiffResult{Repo: p.Repo, State: diff.State, Output: runrepo.FailureOutput(diff.Output, err), Error: repoError(err)}, nil
}

func (s *Server) runGitCommit(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitCommitParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, true)
	if err != nil {
		return nil, rpcError(err)
	}
	commit, err := call.native.Commit(ctx, call.execution, runrepo.CommitRequest{Expected: p.Expected, Paths: p.Paths, Message: p.Message})
	return protocol.RunGitCommitResult{Repo: p.Repo, Committed: commit.Committed, Head: commit.Head, IndexUpdated: commit.IndexUpdated, HooksRun: false, Output: runrepo.FailureOutput(commit.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runGitPush(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitPushParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, true)
	if err != nil {
		return nil, rpcError(err)
	}
	push, err := call.native.Push(ctx, call.execution, runrepo.PushRequest{Expected: p.Expected, Target: runrepo.PushTarget(p.Target)})
	return protocol.RunGitPushResult{Repo: p.Repo, Pushed: push.Pushed, Head: push.Head, Target: protocol.RunGitPushTarget(push.Target), Output: runrepo.FailureOutput(push.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runPRStatus(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRStatusParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, false)
	if errors.Is(err, scheduler.ErrNoLiveEnvironment) {
		run, runErr := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
		if runErr != nil {
			return nil, rpcError(runErr)
		}
		if run.Status.Terminal() {
			result, perr := s.recordedPRStatus(ctx, call, p, err)
			if perr != nil {
				return nil, perr
			}
			return result, nil
		}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	var status runrepo.PRResult
	perr, err := s.prDefaults(ctx, call, &p.Expected, &p.Target)
	if perr != nil {
		return nil, perr
	}
	if err == nil {
		err = call.native.CheckExpected(ctx, call.execution, p.Expected)
	}
	if err == nil {
		status, err = call.native.LookupPR(ctx, call.execution, runrepo.PRTarget(p.Target))
	}
	if status.PullRequest != nil {
		s.recordPR(ctx, call, status.PullRequest, false)
	}
	return protocol.RunPRStatusResult{Repo: p.Repo, Identity: status.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(status.PullRequest), Output: runrepo.FailureOutput(status.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runPRCreate(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRCreateParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, true)
	if err != nil {
		return nil, rpcError(err)
	}
	created, err := call.native.CreatePR(ctx, call.execution, runrepo.PRCreateRequest{Expected: p.Expected, Target: runrepo.PRTarget(p.Target), Title: p.Title, Body: p.Body, Draft: p.Draft, ExpectedLogin: p.ExpectedLogin})
	if created.PullRequest != nil {
		s.recordPR(ctx, call, created.PullRequest, created.Created || created.Reconciled)
	}
	return protocol.RunPRCreateResult{Repo: p.Repo, Identity: created.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(created.PullRequest), Created: created.Created, Reconciled: created.Reconciled, CreationUncertain: created.CreationUncertain, Output: runrepo.FailureOutput(created.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

// recordPR keeps the PR a run opened, or the state of one it opened before,
// for insights, and publishes run.pr when that is new or changed state. It
// outlives a cancelled request, since CreatePR can confirm a PR after one. A
// failure is only logged: the GitHub side already happened and must not be
// reported as failed.
func (s *Server) recordPR(ctx context.Context, call runRepoCall, pr *runrepo.PullRequest, opened bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	run := call.run
	repository := strings.ToLower(pr.Target.Repository)
	recorded, err := s.cfg.Store.RunPullRequests(ctx, run.ID)
	if err != nil {
		slog.Warn("sshd: read run pull requests", "run", run.ID, "error", err)
		return
	}
	var prior *store.RunPullRequest
	for _, candidate := range recorded {
		if candidate.Repository == repository && candidate.Number == pr.Number {
			prior = candidate
		}
	}
	record := s.cfg.Store.ObserveRunPullRequest
	if opened {
		record = s.cfg.Store.RecordRunPullRequest
	}
	err = record(ctx, &store.RunPullRequest{
		RunID: run.ID, RepoWorkspace: call.repoOwner(), Repository: repository, Number: pr.Number,
		URL: pr.URL, State: pr.State, MergedAt: pr.MergedAt, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		slog.Warn("sshd: record run pull request", "run", run.ID, "error", err)
		return
	}
	if opened && prior == nil || prior != nil && prChanged(prior, pr.State, pr.MergedAt) {
		s.publishRunPR(ctx, run, pr.Number, pr.URL, pr.State, pr.MergedAt)
	}
}

// prChanged reports whether an observation changes a recorded pull request,
// whose merged state is final.
func prChanged(recorded *store.RunPullRequest, state string, mergedAt *time.Time) bool {
	return recorded.State != "merged" && recorded.State != state || recorded.MergedAt == nil && mergedAt != nil
}

// publishRunPR tells clients to re-read a run's pull request. A failure is
// only logged, like recordPR's: the GitHub side already happened.
func (s *Server) publishRunPR(ctx context.Context, run domain.Run, number int, url, state string, mergedAt *time.Time) {
	if s.cfg.Bus == nil {
		return
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{WorkspaceID: run.WorkspaceID, RunID: run.ID, Payload: events.RunPRPayload{
		RunID: run.ID, Number: number, URL: url, State: state, MergedAt: mergedAt,
	}}); err != nil {
		slog.Warn("sshd: publish run pull request", "run", run.ID, "error", err)
	}
}

// recordedPRStatus answers run.pr.status for a finished run whose container
// is gone from the pull requests it opened through run.pr.create. Each one
// not yet seen merged is looked up again with the gh login in the home the run's container
// mounted, the one gh used there, so a merge after the run finished is still
// recorded. A run with none recorded keeps the error that it has no live
// environment.
func (s *Server) recordedPRStatus(ctx context.Context, call runRepoCall, p protocol.RunPRStatusParams, liveErr error) (protocol.RunPRStatusResult, *protocol.Error) {
	prs, err := s.cfg.Store.RunPullRequests(ctx, domain.RunID(p.RunID))
	if err != nil {
		return protocol.RunPRStatusResult{}, rpcError(err)
	}
	repository := strings.ToLower(p.Target.Repository)
	prs = slices.DeleteFunc(prs, func(pr *store.RunPullRequest) bool {
		return pr.RepoWorkspace != call.repoOwner() || repository != "" && pr.Repository != repository
	})
	if len(prs) == 0 {
		return protocol.RunPRStatusResult{}, rpcError(liveErr)
	}
	result := protocol.RunPRStatusResult{Repo: p.Repo, AccountMemberID: string(call.account)}
	for _, pr := range prs {
		if pr.State == "merged" || s.cfg.Services.GitHub == nil {
			continue
		}
		identity, observed, lookupErr := s.cfg.Services.GitHub.PullRequest(ctx, call.home, pr.Repository, pr.Number)
		if lookupErr != nil {
			result.Error = repoError(lookupErr)
			continue
		}
		result.Identity = identity.Login
		changed := prChanged(pr, observed.State, observed.MergedAt)
		pr.URL, pr.State, pr.MergedAt, pr.ObservedAt = observed.URL, observed.State, observed.MergedAt, time.Now().UTC()
		if err := s.cfg.Store.ObserveRunPullRequest(ctx, pr); err != nil {
			return protocol.RunPRStatusResult{}, rpcError(err)
		}
		if changed {
			s.publishRunPR(ctx, call.run, pr.Number, pr.URL, pr.State, pr.MergedAt)
		}
	}
	latest := prs[0]
	result.PullRequest = &protocol.RunPullRequest{Number: latest.Number, URL: latest.URL, State: latest.State, Repository: latest.Repository, MergedAt: latest.MergedAt}
	return result, nil
}

func (s *Server) runPRFeedback(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRFeedbackParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, false)
	feedback := runrepo.PRFeedback{Checks: []protocol.RunPRCheck{}, Comments: []protocol.RunPRComment{}, Reviews: []protocol.RunPRReview{}, ReviewComments: []protocol.RunPRReviewComment{}}
	if errors.Is(err, scheduler.ErrNoLiveEnvironment) && call.run.Status.Terminal() {
		status, perr := s.recordedPRStatus(ctx, call, protocol.RunPRStatusParams{RunID: p.RunID, Repo: p.Repo, Target: p.Target}, err)
		if perr != nil {
			return nil, perr
		}
		message := "checks and review feedback need the run's environment, which is gone: " + err.Error()
		if status.Error != "" {
			message += "; " + status.Error
		}
		return protocol.RunPRFeedbackResult{Repo: p.Repo, Identity: status.Identity, AccountMemberID: status.AccountMemberID, PullRequest: status.PullRequest, Checks: feedback.Checks, Comments: feedback.Comments, Reviews: feedback.Reviews, ReviewComments: feedback.ReviewComments, Error: message}, nil
	}
	if err != nil {
		return nil, rpcError(err)
	}
	perr, err := s.prDefaults(ctx, call, &p.Expected, &p.Target)
	if perr != nil {
		return nil, perr
	}
	if err == nil {
		err = call.native.CheckExpected(ctx, call.execution, p.Expected)
	}
	if err == nil {
		feedback, err = call.native.PRFeedback(ctx, call.execution, runrepo.PRTarget(p.Target), p.Limit)
	}
	return protocol.RunPRFeedbackResult{Repo: p.Repo, Identity: feedback.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(feedback.PullRequest), Checks: feedback.Checks, Comments: feedback.Comments, Reviews: feedback.Reviews, ReviewComments: feedback.ReviewComments, Mergeable: feedback.Mergeable, MergeStateStatus: feedback.MergeStateStatus, Truncated: feedback.Truncated, Output: runrepo.FailureOutput(feedback.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runGitLog(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitLogParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, false)
	if err != nil {
		return nil, rpcError(err)
	}
	log, err := call.native.Log(ctx, call.execution, runrepo.LogRequest{BaseBranch: call.repo.BaseBranch, ForkPoint: call.repo.BaseCommit, Limit: p.Limit})
	log.Repo = p.Repo
	log.Output = runrepo.FailureOutput(log.Output, err)
	log.Error = repoError(err)
	return log, nil
}

func (s *Server) runPRMerge(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRMergeParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	if (p.Repository == "") != (p.Number == 0) {
		return nil, invalidParams("repository and number must be given together")
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, p.Repo, true)
	if err != nil {
		return nil, rpcError(err)
	}
	if p.Repository == "" {
		prs, listErr := s.cfg.Store.RunPullRequests(ctx, call.run.ID)
		if listErr != nil {
			return nil, rpcError(listErr)
		}
		prs = slices.DeleteFunc(prs, func(pr *store.RunPullRequest) bool { return pr.RepoWorkspace != call.repoOwner() })
		if len(prs) == 0 {
			return nil, invalidParams("run has no recorded pull request; pass repository and number")
		}
		p.Repository, p.Number = prs[0].Repository, prs[0].Number
	}
	merged, err := call.native.MergePR(ctx, call.execution, runrepo.PRMergeRequest{Repository: p.Repository, Number: p.Number, Method: p.Method, ExpectedHead: p.ExpectedHead, ExpectedLogin: p.ExpectedLogin, ExpectedBase: p.ExpectedBase})
	if merged.PullRequest != nil {
		s.recordPR(ctx, call, merged.PullRequest, false)
	}
	return protocol.RunPRMergeResult{Repo: p.Repo, Identity: merged.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(merged.PullRequest), Merged: merged.PullRequest != nil && merged.PullRequest.State == "merged", Output: runrepo.FailureOutput(merged.Output, err), Error: repoError(err)}, nil
}
