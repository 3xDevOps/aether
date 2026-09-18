package mission

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	scopeDiagIntended   = "intended_overlap"
	scopeDiagObserved   = "observed_overlap"
	scopeDiagOutside    = "out_of_scope"
	maxScopeDiagnostics = 256
)

// scopeDiagnostics compares the declared task scope with both the current
// overlap snapshots and the immutable retained evidence attached to a
// submission. A missing snapshot or expired/unavailable packet is represented
// explicitly as unavailable; it is never interpreted as an empty change set.
func (s *Service) scopeDiagnostics(ctx context.Context, tasks []*domain.Task, attempts []*domain.Attempt, submissions []*domain.Submission) ([]protocol.MissionScopeDiagnostic, error) {
	if len(tasks) == 0 {
		return nil, nil
	}
	workspaceByMission := make(map[domain.MissionID]domain.WorkspaceID)
	for _, task := range tasks {
		if task == nil || task.MissionID == "" {
			continue
		}
		if _, ok := workspaceByMission[task.MissionID]; ok {
			continue
		}
		mission, err := s.cfg.Missions.GetMission(ctx, task.MissionID)
		if err != nil {
			return nil, err
		}
		workspaceByMission[task.MissionID] = mission.WorkspaceID
	}
	byTask := make(map[domain.TaskID]*domain.Task, len(tasks))
	for _, task := range tasks {
		if task != nil {
			byTask[task.ID] = task
		}
	}
	attemptByTask := make(map[domain.TaskID][]*domain.Attempt)
	for _, attempt := range attempts {
		if attempt != nil && attempt.TaskID != "" {
			attemptByTask[attempt.TaskID] = append(attemptByTask[attempt.TaskID], attempt)
		}
	}
	// Intended overlap is a relationship between declared scopes, independent
	// of whether workers have started yet.
	out := make([]protocol.MissionScopeDiagnostic, 0, 16)
	for i := range len(tasks) {
		a := tasks[i]
		if a == nil || a.Revision == nil {
			continue
		}
		for j := i + 1; j < len(tasks); j++ {
			b := tasks[j]
			if b == nil || b.Revision == nil {
				continue
			}
			shared := intersectPaths(a.Revision.Scope.ExpectedPaths, b.Revision.Scope.ExpectedPaths)
			if len(shared) == 0 {
				continue
			}
			var runID, peerRunID domain.RunID
			if runs := taskAttempts(attemptByTask[a.ID]); len(runs) > 0 {
				runID = runs[0]
			}
			if runs := taskAttempts(attemptByTask[b.ID]); len(runs) > 0 {
				peerRunID = runs[0]
			}
			if len(out) >= maxScopeDiagnostics {
				return out, nil
			}
			out = append(out, protocol.MissionScopeDiagnostic{TaskID: string(a.ID), TaskRevision: a.CurrentRevision, RunID: string(runID), Kind: scopeDiagIntended, Paths: shared, PeerTaskID: string(b.ID), PeerRunID: string(peerRunID)})
		}
	}

	// Observed overlap is derived from the same run.diff snapshots consumed by
	// the overlap index. Snapshot failures are retained as an explicit unknown.
	type observed struct {
		attempt *domain.Attempt
		paths   []string
		known   bool
		reason  string
	}
	observedRuns := make([]observed, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt == nil || attempt.RunID == "" || byTask[attempt.TaskID] == nil {
			continue
		}
		var paths []string
		var known bool
		var err error
		if s.cfg.ScopeSnapshot != nil {
			paths, known, err = s.cfg.ScopeSnapshot(ctx, attempt.RunID)
		}
		reason := "snapshot unavailable"
		if err != nil {
			reason = fmt.Sprintf("snapshot lookup failed: %v", err)
		}
		if known {
			paths = cleanPaths(paths)
			reason = ""
		}
		observedRuns = append(observedRuns, observed{attempt: attempt, paths: paths, known: known, reason: reason})
		if !known && len(out) < maxScopeDiagnostics {
			out = append(out, protocol.MissionScopeDiagnostic{TaskID: string(attempt.TaskID), TaskRevision: attempt.TaskRevision, RunID: string(attempt.RunID), Kind: scopeDiagObserved, Unavailable: true, UnavailableWhy: reason})
		}
	}
	for i := range len(observedRuns) {
		for j := i + 1; j < len(observedRuns); j++ {
			a, b := observedRuns[i], observedRuns[j]
			if !a.known || !b.known {
				continue
			}
			shared := intersectPaths(a.paths, b.paths)
			if len(shared) == 0 || len(out) >= maxScopeDiagnostics {
				continue
			}
			out = append(out, protocol.MissionScopeDiagnostic{TaskID: string(a.attempt.TaskID), TaskRevision: a.attempt.TaskRevision, RunID: string(a.attempt.RunID), Kind: scopeDiagObserved, Paths: shared, PeerTaskID: string(b.attempt.TaskID), PeerRunID: string(b.attempt.RunID)})
		}
	}

	// Out-of-scope is evaluated against the immutable packet referenced by the
	// exact submission. Never use a worker's later live snapshot for this fact.
	for _, task := range tasks {
		if task == nil || task.Revision == nil || len(out) >= maxScopeDiagnostics {
			continue
		}
		sub := latestSubmission(submissions, task.ID, task.CurrentRevision)
		if sub == nil {
			continue
		}
		d := protocol.MissionScopeDiagnostic{TaskID: string(task.ID), TaskRevision: task.CurrentRevision, Kind: scopeDiagOutside, RunID: string(sub.Ref.RunID), Detail: fmt.Sprintf("evidence_ref=%s;submission_id=%s", sub.Ref.EvidenceRef, sub.ID)}
		if s.cfg.Evidence == nil || sub.Ref.EvidenceRef == "" {
			d.Unavailable, d.UnavailableWhy = true, "retained evidence unavailable"
			out = append(out, d)
			continue
		}
		packet, err := s.cfg.Evidence.Get(ctx, workspaceByMission[task.MissionID], sub.Ref.EvidenceRef)
		if err != nil || packet.ID == "" || packet.Availability != protocol.EvidenceAvailable || packet.RunID != string(sub.Ref.RunID) || packet.RetainedRevision != sub.Ref.RetainedRevision {
			d.Unavailable = true
			if err != nil {
				d.UnavailableWhy = fmt.Sprintf("retained evidence lookup failed: %v", err)
			} else {
				d.UnavailableWhy = "retained evidence unavailable or does not match submission"
			}
			out = append(out, d)
			continue
		}
		if len(packet.UnresolvedFacts) > 0 {
			d.Unavailable = true
			d.UnavailableWhy = "retained evidence has unresolved facts: " + strings.Join(packet.UnresolvedFacts, "; ")
			out = append(out, d)
			continue
		}
		var changed []string
		for _, fact := range packet.ChangedFiles {
			changed = append(changed, fact.Path)
		}
		d.Paths = scopeViolations(task.Revision.Scope, changed)
		if len(d.Paths) > 0 {
			out = append(out, d)
		}
	}
	return out, nil
}

func latestSubmission(submissions []*domain.Submission, taskID domain.TaskID, revision int) *domain.Submission {
	var selected *domain.Submission
	for _, sub := range submissions {
		if sub == nil || sub.TaskID != taskID || sub.TaskRevision != revision {
			continue
		}
		if sub.State == domain.SubmissionAccepted {
			return sub
		}
		if selected == nil || sub.CreatedAt.After(selected.CreatedAt) {
			selected = sub
		}
	}
	return selected
}

func taskAttempts(in []*domain.Attempt) []domain.RunID {
	out := make([]domain.RunID, 0, len(in))
	for _, a := range in {
		if a != nil && a.RunID != "" {
			out = append(out, a.RunID)
		}
	}
	return out
}

func intersectPaths(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	out := make([]string, 0)
	for _, left := range cleanPaths(a) {
		for _, right := range cleanPaths(b) {
			if pathsOverlap(left, right) {
				out = append(out, left)
				break
			}
		}
	}
	return cleanPaths(out)
}

func pathsOverlap(a, b string) bool {
	return scopeContains([]string{a}, b) || scopeContains([]string{b}, a)
}

// scopeViolations returns changed paths outside an accepted task scope.
// It is shared by report reconciliation and dashboard diagnostics so both
// paths apply identical directory and exclusion semantics.
func scopeViolations(scope domain.TaskScope, paths []string) []string {
	out := make([]string, 0)
	for _, changed := range paths {
		if !scopeContains(scope.ExpectedPaths, changed) || scopeContains(scope.Exclusions, changed) {
			out = append(out, changed)
		}
	}
	return cleanPaths(out)
}

func scopeContains(declared []string, observed string) bool {
	observed = path.Clean(strings.TrimSpace(observed))
	if observed == "." || observed == "" {
		return false
	}
	for _, raw := range declared {
		base := path.Clean(strings.TrimSpace(raw))
		if base == "." || base == "" {
			continue
		}
		if base == observed || strings.HasPrefix(observed, base+"/") {
			return true
		}
	}
	return false
}

func cleanPaths(in []string) []string {
	set := make(map[string]struct{}, len(in))
	for _, raw := range in {
		p := path.Clean(strings.TrimSpace(raw))
		if p == "." || p == "" {
			continue
		}
		set[p] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
