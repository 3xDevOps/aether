package runrepo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type LogRequest struct {
	// BaseBranch names the checkout's origin/<BaseBranch>; ForkPoint is the
	// base used when that ref is missing.
	BaseBranch string
	ForkPoint  string
	Limit      int
}

type LogResult = protocol.RunGitLogResult

const logFormat = "--format=%H%x1f%h%x1f%an%x1f%aI%x1f%P%x1f%s"

// Log reads the run branch against its upstream base: the commits on each
// side of their merge base and the paths a merge of the two would conflict
// on. merge-tree writes only unreferenced objects; refs, index and worktree
// are untouched.
func (s *Service) Log(ctx context.Context, run Execution, req LogRequest) (LogResult, error) {
	result := LogResult{Commits: []protocol.RunGitCommit{}, Upstream: []protocol.RunGitCommit{}, Conflicts: []protocol.RunGitConflict{}}
	if req.Limit < 0 || req.Limit > protocol.MaxRunGitLogLimit {
		return result, fmt.Errorf("runrepo: log limit must be between 1 and %d", protocol.MaxRunGitLogLimit)
	}
	if req.Limit == 0 {
		req.Limit = protocol.DefaultRunGitLogLimit
	}
	out, err := s.git(ctx, run, false, "rev-parse", "--verify", "HEAD^{commit}")
	head, err := complete(out, err)
	if err != nil {
		return result, err
	}
	result.Head = strings.TrimSpace(head)
	out, err = s.git(ctx, run, false, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		result.Branch = strings.TrimSpace(out.Stdout)
	} else if !exitedOne(out, err) {
		return result, err
	}
	if req.BaseBranch != "" {
		if err = s.branch(ctx, run, req.BaseBranch); err != nil {
			return result, err
		}
		out, err = s.git(ctx, run, false, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+req.BaseBranch+"^{commit}")
		if err == nil {
			result.BaseRef, result.Base = "origin/"+req.BaseBranch, strings.TrimSpace(out.Stdout)
		} else if !exitedOne(out, err) {
			return result, err
		}
	}
	if result.Base == "" {
		if !validOID(req.ForkPoint) {
			return result, errors.New("runrepo: run has no upstream base: origin/" + req.BaseBranch + " is missing and no fork point is recorded")
		}
		result.Base = req.ForkPoint
	}
	if !validOID(result.Head) || !validOID(result.Base) {
		return result, errors.New("runrepo: invalid native commit ID")
	}
	out, err = s.git(ctx, run, false, "merge-base", result.Head, result.Base)
	if err == nil {
		result.MergeBase = strings.TrimSpace(out.Stdout)
	} else if !exitedOne(out, err) {
		return result, err
	}
	out, err = s.git(ctx, run, false, "rev-list", "--left-right", "--count", result.Head+"..."+result.Base)
	counts, err := complete(out, err)
	if err != nil {
		return result, err
	}
	if _, scanErr := fmt.Sscanf(counts, "%d\t%d", &result.Ahead, &result.Behind); scanErr != nil {
		return result, fmt.Errorf("runrepo: invalid native rev-list count %q: %w", strings.TrimSpace(counts), scanErr)
	}
	result.Truncated = result.Ahead > req.Limit || result.Behind > req.Limit
	if result.Commits, err = s.commits(ctx, run, req.Limit, result.Base+".."+result.Head); err != nil {
		return result, err
	}
	if result.Upstream, err = s.commits(ctx, run, req.Limit, result.Head+".."+result.Base); err != nil {
		return result, err
	}
	out, err = s.git(ctx, run, false, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", result.Head, result.Base)
	if err != nil && !exitedOne(out, err) {
		return result, err
	}
	if out.Truncated {
		return result, &CommandError{Operation: "merge-tree", Output: out, Cause: ErrTruncated}
	}
	if err != nil {
		paths := strings.Split(strings.TrimSuffix(out.Stdout, "\x00"), "\x00")
		for _, path := range paths[1:] {
			result.Conflicts = append(result.Conflicts, protocol.RunGitConflict{Path: path})
		}
	}
	return result, nil
}

func (s *Service) commits(ctx context.Context, run Execution, limit int, revisions string) ([]protocol.RunGitCommit, error) {
	out, err := s.git(ctx, run, false, "log", "-z", "--no-show-signature", "--max-count="+strconv.Itoa(limit), logFormat, revisions, "--")
	text, err := complete(out, err)
	if err != nil {
		return nil, err
	}
	commits := []protocol.RunGitCommit{}
	for _, record := range strings.Split(text, "\x00") {
		if record == "" {
			continue
		}
		fields := strings.SplitN(strings.TrimPrefix(record, "\n"), "\x1f", 6)
		if len(fields) != 6 || !validOID(fields[0]) {
			return nil, errors.New("runrepo: invalid native git log response")
		}
		commits = append(commits, protocol.RunGitCommit{SHA: fields[0], Short: fields[1], Author: fields[2], AuthoredAt: fields[3], Parents: strings.Fields(fields[4]), Subject: fields[5]})
	}
	return commits, nil
}

// exitedOne reports a native command that ran and exited 1, which several
// read commands use for "not found" or "conflicts".
func exitedOne(out CommandOutput, err error) bool {
	var commandErr *CommandError
	return errors.As(err, &commandErr) && commandErr.Cause == nil && out.ExitCode == 1
}
