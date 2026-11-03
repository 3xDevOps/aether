package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// recentMail is how long mail from another session warns a close even
// after the run read it.
const recentMail = 30 * time.Minute

func init() {
	registerMethod(protocol.MethodRunCloseCheck, (*Server).runCloseCheck)
}

func (s *Server) runCloseCheck(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunCloseCheckParams](params)
	if perr != nil {
		return nil, perr
	}
	if len(p.RunIDs) == 0 || len(p.RunIDs) > protocol.MaxCloseCheckRuns {
		return nil, invalidParams(fmt.Sprintf("run_ids must name 1 to %d sessions", protocol.MaxCloseCheckRuns))
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.RunCloseCheckResult{Runs: make([]protocol.RunCloseCheck, len(p.RunIDs))}
	for i, id := range p.RunIDs {
		out.Runs[i] = protocol.RunCloseCheck{RunID: id, Warnings: []protocol.CloseWarning{}}
		warnings, perr := s.closeWarnings(ctx, actor, domain.RunID(id))
		if perr != nil {
			out.Runs[i].Error = perr
			continue
		}
		out.Runs[i].Warnings = append(out.Runs[i].Warnings, warnings...)
	}
	return out, nil
}

func (s *Server) closeWarnings(ctx context.Context, actor permissions.Actor, id domain.RunID) ([]protocol.CloseWarning, *protocol.Error) {
	if id == "" {
		return nil, invalidParams("run_id is required")
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.View, actor, target); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunCloseCheck + ": " + cerr.Error()}
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	prs, err := s.cfg.Store.RunPullRequests(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	deps, err := s.cfg.Store.CloseDependents(ctx, id, time.Now().Add(-recentMail))
	if err != nil {
		return nil, rpcError(err)
	}
	snap := s.runSnapshot(run)
	var out []protocol.CloseWarning
	if run.Protected {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningProtected,
			Detail: "Protected: only its owner or an admin can steer, message or close it"})
	}
	// Recorded pull request states are as last observed; nothing here asks
	// GitHub, so a 50-run check stays fast.
	open, merged := false, false
	for _, pr := range prs {
		primary := pr.RepoWorkspace == ""
		switch pr.State {
		case "merged":
			merged = merged || primary && (pr.MergedAt == nil || !pr.MergedAt.Before(run.LastCommitAt))
		case "closed":
		default:
			open = open || primary
			out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningPullRequest, URL: pr.URL,
				Detail: fmt.Sprintf("Pull request %s#%d was %s, not merged, when Aether last checked %s", pr.Repository, pr.Number, pr.State,
					pr.ObservedAt.UTC().Format(time.RFC3339))})
		}
	}
	if run.LastCommit != "" && run.LastCommit != run.BaseCommit && run.Status != domain.RunMerged && !open && !merged {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningUnmergedWork,
			Detail: "Commits on " + run.Branch + " are in no merged pull request"})
	}
	if deps.Messages > 0 {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningMessages, Count: deps.Messages,
			Detail: fmt.Sprintf("%d %s from other sessions unread or sent in the last 30 minutes", deps.Messages, plural(deps.Messages, "message", "messages"))})
	}
	if len(deps.Awaiting) > 0 {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningAwaited, RunIDs: runIDStrings(deps.Awaiting),
			Detail: "Waiting for it to finish: " + s.runLabels(ctx, deps.Awaiting)})
	}
	if len(deps.Children) > 0 {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningSubsessions, Count: len(deps.Children), RunIDs: runIDStrings(deps.Children),
			Detail: fmt.Sprintf("%d %s close with it: %s", len(deps.Children), plural(len(deps.Children), "subsession", "subsessions"), s.runLabels(ctx, deps.Children))})
	}
	if snap.CleanupPending || snap.CleanupError != "" {
		detail := "Cleanup of its container and checkout is still pending"
		if snap.CleanupError != "" {
			detail = "Cleanup failed: " + snap.CleanupError
		}
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningCleanupPending, Detail: detail})
	}
	return out, nil
}

// runLabels names each run by id and, when it has one, its title.
func (s *Server) runLabels(ctx context.Context, ids []domain.RunID) string {
	labels := make([]string, len(ids))
	for i, id := range ids {
		labels[i] = string(id)
		if r, err := s.cfg.Store.GetRun(ctx, id); err == nil && r.Title != "" {
			labels[i] += " (" + r.Title + ")"
		}
	}
	return strings.Join(labels, ", ")
}

func runIDStrings(ids []domain.RunID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
