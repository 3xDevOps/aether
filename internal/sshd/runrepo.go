package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runrepo"
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
	registerGuarded(protocol.MethodRunPRStatus, permissions.View, runTarget, (*Server).runPRStatus)
	registerGuarded(protocol.MethodRunPRCreate, permissions.Push, runTarget, (*Server).runPRCreate)
	registerGuarded(protocol.MethodRunPRFeedback, permissions.View, runTarget, (*Server).runPRFeedback)
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
	native      *runrepo.Service
	execution   runrepo.Execution
	account     domain.MemberID
	accountName string
}

func (s *Server) runRepoAuthority(ctx context.Context, member domain.MemberID, runID string, mutation bool) (runRepoCall, error) {
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
			_, accountErr := ResolveLaunchAccount(ctx, s.cfg.Store, member, string(account))
			return accountErr
		})
	}
	if authorityErr := authorize(ctx, mutation); authorityErr != nil {
		return call, authorityErr
	}
	execution, executionErr := s.cfg.Services.RunRepo.Execution(ctx, *run)
	if executionErr != nil {
		return call, executionErr
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
	return runRepoCall{native: s.cfg.Services.RunRepo.Native(), execution: execution, account: account, accountName: owner.DisplayName}, nil
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
	return &protocol.RunPullRequest{Number: pr.Number, URL: pr.URL, State: pr.State, Title: pr.Title, Draft: pr.Draft, Repository: pr.Target.Repository, BaseBranch: pr.Target.BaseBranch, HeadRepository: pr.Target.HeadRepository, HeadBranch: pr.Target.HeadBranch, HeadOID: pr.Head}
}

func (s *Server) runGitStatus(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitStatusParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, false)
	if err != nil {
		return nil, rpcError(err)
	}
	state, err := call.native.Status(ctx, call.execution)
	result := protocol.RunGitStatusResult{Branch: state.Branch, Head: state.Head, Detached: state.Detached, Unborn: state.Unborn, Changes: state.Changes, AccountMemberID: string(call.account), AccountName: call.accountName, Remotes: []protocol.RunGitRemote{}}
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
	call, err := s.runRepoAuthority(ctx, member, p.RunID, false)
	if err != nil {
		return nil, rpcError(err)
	}
	diff, err := call.native.Diff(ctx, call.execution, runrepo.DiffRequest{Paths: p.Paths, Staged: p.Staged})
	return protocol.RunGitDiffResult{State: diff.State, Output: runrepo.FailureOutput(diff.Output, err), Error: repoError(err)}, nil
}

func (s *Server) runGitCommit(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitCommitParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, true)
	if err != nil {
		return nil, rpcError(err)
	}
	commit, err := call.native.Commit(ctx, call.execution, runrepo.CommitRequest{Expected: p.Expected, Paths: p.Paths, Message: p.Message})
	return protocol.RunGitCommitResult{Committed: commit.Committed, Head: commit.Head, IndexUpdated: commit.IndexUpdated, HooksRun: false, Output: runrepo.FailureOutput(commit.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runGitPush(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunGitPushParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, true)
	if err != nil {
		return nil, rpcError(err)
	}
	push, err := call.native.Push(ctx, call.execution, runrepo.PushRequest{Expected: p.Expected, Target: runrepo.PushTarget(p.Target)})
	return protocol.RunGitPushResult{Pushed: push.Pushed, Head: push.Head, Target: protocol.RunGitPushTarget(push.Target), Output: runrepo.FailureOutput(push.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runPRStatus(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRStatusParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, false)
	if err != nil {
		return nil, rpcError(err)
	}
	var status runrepo.PRResult
	err = call.native.CheckExpected(ctx, call.execution, p.Expected)
	if err == nil {
		status, err = call.native.LookupPR(ctx, call.execution, runrepo.PRTarget(p.Target))
	}
	return protocol.RunPRStatusResult{Identity: status.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(status.PullRequest), Output: runrepo.FailureOutput(status.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runPRCreate(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRCreateParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, true)
	if err != nil {
		return nil, rpcError(err)
	}
	created, err := call.native.CreatePR(ctx, call.execution, runrepo.PRCreateRequest{Expected: p.Expected, Target: runrepo.PRTarget(p.Target), Title: p.Title, Body: p.Body, Draft: p.Draft, ExpectedLogin: p.ExpectedLogin})
	return protocol.RunPRCreateResult{Identity: created.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(created.PullRequest), Created: created.Created, Reconciled: created.Reconciled, CreationUncertain: created.CreationUncertain, Output: runrepo.FailureOutput(created.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}

func (s *Server) runPRFeedback(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	var p protocol.RunPRFeedbackParams
	if err := decodeRunRepo(raw, &p); err != nil {
		return nil, err
	}
	call, err := s.runRepoAuthority(ctx, member, p.RunID, false)
	if err != nil {
		return nil, rpcError(err)
	}
	feedback := runrepo.PRFeedback{Checks: []protocol.RunPRCheck{}, Comments: []protocol.RunPRComment{}, Reviews: []protocol.RunPRReview{}, ReviewComments: []protocol.RunPRReviewComment{}}
	err = call.native.CheckExpected(ctx, call.execution, p.Expected)
	if err == nil {
		feedback, err = call.native.PRFeedback(ctx, call.execution, runrepo.PRTarget(p.Target), p.Limit)
	}
	return protocol.RunPRFeedbackResult{Identity: feedback.Identity, AccountMemberID: string(call.account), PullRequest: publicPR(feedback.PullRequest), Checks: feedback.Checks, Comments: feedback.Comments, Reviews: feedback.Reviews, ReviewComments: feedback.ReviewComments, Truncated: feedback.Truncated, Output: runrepo.FailureOutput(feedback.Output, err), Error: repoError(err), Actual: repoActual(err)}, nil
}
