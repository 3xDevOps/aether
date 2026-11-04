package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
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
	outcome := domain.RunStatus(p.Outcome)
	if outcome != "" && outcome != domain.RunMerged && outcome != domain.RunAbandoned && outcome != domain.RunCompleted {
		return nil, invalidParams(`outcome must be "merged", "abandoned" or "completed"`)
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.RunCloseCheckResult{Runs: []protocol.RunCloseCheck{}}
	seen := map[string]bool{}
	for _, id := range p.RunIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		check := protocol.RunCloseCheck{RunID: id, Warnings: []protocol.CloseWarning{}}
		warnings, perr := s.closeWarnings(ctx, actor, domain.RunID(id), outcome)
		if perr != nil {
			check.Error = perr
		} else {
			check.Warnings = append(check.Warnings, warnings...)
		}
		out.Runs = append(out.Runs, check)
	}
	return out, nil
}

func (s *Server) closeWarnings(ctx context.Context, actor permissions.Actor, id domain.RunID, outcome domain.RunStatus) ([]protocol.CloseWarning, *protocol.Error) {
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
	deps, err := s.cfg.Store.CloseDependents(ctx, id, time.Now().Add(-recentMail))
	if err != nil {
		return nil, rpcError(err)
	}
	known := map[domain.RunID]*domain.Run{id: run}
	var out []protocol.CloseWarning
	if run.Protected {
		out = append(out, protectedWarning)
	}
	work, err := s.workWarnings(ctx, run, outcome)
	if err != nil {
		return nil, rpcError(err)
	}
	out = append(out, work...)
	if deps.Messages > 0 {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningMessages, Count: deps.Messages, RunIDs: runIDStrings(deps.Senders),
			Detail: fmt.Sprintf("%d %s from %s %s unread or arrived in the last 30 minutes", deps.Messages, plural(deps.Messages, "message", "messages"),
				s.runLabels(ctx, known, deps.Senders), plural(deps.Messages, "is", "are"))})
	}
	tree := func(w domain.RunID) bool { return w == id || slices.Contains(deps.Children, w) }
	out = append(out, s.awaitedWarnings(ctx, known, slices.DeleteFunc(deps.Awaiting, tree))...)
	if len(deps.Children) > 0 {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningSubsessions, Count: len(deps.Children), RunIDs: runIDStrings(deps.Children),
			Detail: fmt.Sprintf("%d %s close with it: %s", len(deps.Children), plural(len(deps.Children), "subsession", "subsessions"), s.runLabels(ctx, known, deps.Children))})
	}
	// A subsession closes as its parent does, so what closing it would
	// affect is part of this close.
	for _, childID := range deps.Children {
		child, read := known[childID]
		if !read {
			var err error
			if child, err = s.cfg.Store.GetRun(ctx, childID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, rpcError(err)
			}
		}
		if child == nil {
			continue
		}
		waiters, err := s.cfg.Store.RunWaiters(ctx, childID)
		if err != nil {
			return nil, rpcError(err)
		}
		childWork, err := s.workWarnings(ctx, child, outcome)
		if err != nil {
			return nil, rpcError(err)
		}
		if child.Protected {
			childWork = append(childWork, protectedWarning)
		}
		for _, w := range append(childWork, s.awaitedWarnings(ctx, known, slices.DeleteFunc(waiters, tree))...) {
			w.Subsession = string(childID)
			out = append(out, w)
		}
	}
	snap := s.runSnapshot(run)
	if snap.CleanupPending || snap.CleanupError != "" {
		detail := "Cleanup of its container and checkout is still pending"
		if snap.CleanupError != "" {
			detail = "Cleanup failed: " + snap.CleanupError
		}
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningCleanupPending, Detail: detail})
	}
	return out, nil
}

// workWarnings reads recorded pull request states as last observed; it
// never asks GitHub, so a 50-run check stays fast. Recorded pull requests
// carry no commit, so whether one holds the branch tip is judged by time.
func (s *Server) workWarnings(ctx context.Context, run *domain.Run, outcome domain.RunStatus) ([]protocol.CloseWarning, error) {
	prs, err := s.cfg.Store.RunPullRequests(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	var out []protocol.CloseWarning
	open, merged := false, false
	for _, pr := range prs {
		primary := pr.RepoWorkspace == ""
		switch pr.State {
		case "merged":
			merged = merged || primary && (pr.MergedAt == nil || !pr.MergedAt.Before(run.LastCommitAt))
		case "closed":
		default:
			open = open || primary
			observed := pr.ObservedAt.UTC().Format(time.RFC3339)
			out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningPullRequest, URL: pr.URL, Number: pr.Number, ObservedAt: observed,
				Detail: fmt.Sprintf("Pull request %s#%d was %s, not merged, when Aether last checked %s", pr.Repository, pr.Number, pr.State, observed)})
		}
	}
	if outcome != domain.RunMerged && run.Status != domain.RunMerged && run.LastCommit != "" && run.LastCommit != run.BaseCommit && !open && !merged {
		out = append(out, protocol.CloseWarning{Kind: protocol.CloseWarningUnmergedWork,
			Detail: "Commits on " + run.Branch + " may not be merged: no merged pull request includes its last commit"})
	}
	return out, nil
}

var protectedWarning = protocol.CloseWarning{Kind: protocol.CloseWarningProtected,
	Detail: "Protected: only its owner or an admin can steer, message or close it"}

func (s *Server) awaitedWarnings(ctx context.Context, known map[domain.RunID]*domain.Run, waiters []domain.RunID) []protocol.CloseWarning {
	if len(waiters) == 0 {
		return nil
	}
	return []protocol.CloseWarning{{Kind: protocol.CloseWarningAwaited, RunIDs: runIDStrings(waiters),
		Detail: "Waiting for it to finish: " + s.runLabels(ctx, known, waiters)}}
}

// runLabels reads each run once per check: known holds what the check
// already read, and a nil entry a run with no record yet.
func (s *Server) runLabels(ctx context.Context, known map[domain.RunID]*domain.Run, ids []domain.RunID) string {
	labels := make([]string, len(ids))
	for i, id := range ids {
		r, read := known[id]
		if !read {
			var err error
			r, err = s.cfg.Store.GetRun(ctx, id)
			// A failed read only costs the label its title; the caller
			// that needs the run reads it again and returns the error.
			if err == nil || errors.Is(err, store.ErrNotFound) {
				known[id] = r
			}
		}
		_, cached := known[id]
		labels[i] = string(id)
		switch {
		case cached && r == nil:
			labels[i] += " (queued)"
		case r != nil && r.Title != "":
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

// archiveHolds refuses to archive a completed session while it still holds
// work a close would warn about: live subsessions, a pull request not merged,
// sessions waiting on it, or pending cleanup. A Final session was closed past
// those warnings already.
func (s *Server) archiveHolds(ctx context.Context, member domain.MemberID, id domain.RunID) *protocol.Error {
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return rpcError(err)
	}
	if run.Status != domain.RunCompleted {
		return nil
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return rpcError(err)
	}
	warnings, perr := s.closeWarnings(ctx, actor, id, domain.RunCompleted)
	if perr != nil {
		return perr
	}
	var held []string
	for _, w := range warnings {
		if archiveHolding(w.Kind) {
			held = append(held, w.Detail)
		}
	}
	if len(held) == 0 {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeInvalidState, Message: "completed session " + string(id) + " still holds work: " + strings.Join(held, "; ")}
}

func archiveHolding(kind string) bool {
	switch kind {
	case protocol.CloseWarningSubsessions, protocol.CloseWarningPullRequest, protocol.CloseWarningAwaited, protocol.CloseWarningCleanupPending:
		return true
	}
	return false
}
