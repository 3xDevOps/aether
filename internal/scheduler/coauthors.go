package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
)

// coAuthorsPath is where the run's co-author list appears inside its
// container, under the read-only coordination mount.
var coAuthorsPath = path.Join(mcpbridge.MountDir, coord.CoAuthorsName)

// coAuthorInstruction tells the agent to credit the people steering it.
// The list changes while the agent works, so the file is the contract and
// re-reading it is part of the instruction.
var coAuthorInstruction = "Before each commit, read " + coAuthorsPath +
	". It holds one Co-authored-by trailer per person other than the run owner who has steered this run," +
	" and it grows while you work. End every commit message you write, and the description of any pull" +
	" request you open, with exactly those lines. An empty file means add nothing."

// withCoAuthorInstruction appends that rule to a task prompt. A taskless
// launch stays taskless - its placeholder is dropped whole, so there is
// nowhere to say this - and a run without coordination has no mounted
// directory to read the file from.
func (s *Scheduler) withCoAuthorInstruction(task string) string {
	if task == "" || s.coordinationSeam() == nil {
		return task
	}
	return task + "\n\n" + coAuthorInstruction
}

// coAuthorTrailers renders one Co-authored-by line per member who steered
// the run and does not own it now. The owner is the commit's author, and a
// handoff can hand the run to someone already on the list. A member who set
// no git identity is credited by display name at their aether.local
// address, which is what domain.GitIdentity falls back to.
func (s *Scheduler) coAuthorTrailers(ctx context.Context, run *domain.Run) ([]string, error) {
	steerers, err := s.cfg.Store.ListRunSteerers(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list run steerers: %w", err)
	}
	trailers := make([]string, 0, len(steerers))
	for _, m := range steerers {
		if m.ID == run.MemberID {
			continue
		}
		trailers = append(trailers, m.GitIdentity().Trailer())
	}
	return trailers, nil
}

// commitAll commits the run's outstanding work: authored as the run's
// owner, committed as Aether, and crediting every member who steered it.
// A run or member the store cannot answer for still gets its work
// committed - losing the commit would be worse than losing the byline.
func (s *Scheduler) commitAll(ctx context.Context, run domain.RunID, message string) (string, error) {
	var author domain.GitIdentity
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve run for commit author", "run", run, "error", err)
	} else {
		if owner, oerr := s.cfg.Store.GetMember(ctx, r.MemberID); oerr != nil {
			slog.Warn("scheduler: resolve commit author", "run", run, "error", oerr)
		} else {
			author = owner.GitIdentity()
		}
		trailers, terr := s.coAuthorTrailers(ctx, r)
		if terr != nil {
			slog.Warn("scheduler: resolve commit co-authors", "run", run, "error", terr)
		}
		if len(trailers) > 0 {
			message += "\n\n" + strings.Join(trailers, "\n")
		}
	}
	return s.cfg.Git.CommitAll(ctx, run, message, author)
}

// RecordSteer notes that a member other than the run's owner steered it -
// injected a message, or typed into one of its terminals. The first time
// for a given member it stamps the workspace timeline and refreshes the
// list the container reads; later calls are no-ops.
func (s *Scheduler) RecordSteer(ctx context.Context, run domain.RunID, actor domain.MemberID) {
	if actor == "" {
		return
	}
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve run for steer record", "run", run, "error", err)
		return
	}
	if actor == r.MemberID {
		return
	}
	added, err := s.cfg.Store.AddRunSteerer(ctx, run, actor)
	if err != nil {
		slog.Warn("scheduler: record run steerer", "run", run, "member", actor, "error", err)
		return
	}
	if !added {
		return
	}
	s.publishTimeline(ctx, r.WorkspaceID, run, actor, events.TimelineCoAuthor, "")
	s.refreshCoAuthors(ctx, r)
}

// refreshCoAuthors rewrites the run container's co-author list. Only a run
// holding a provisioned coordination directory has one to rewrite; for any
// other run the trailers still reach the branch through commitAll.
func (s *Scheduler) refreshCoAuthors(ctx context.Context, run *domain.Run) {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run.ID]
	provisioned := entry != nil && entry.coordDir != ""
	s.mu.Unlock()
	if c == nil || !provisioned {
		return
	}
	trailers, err := s.coAuthorTrailers(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve co-authors", "run", run.ID, "error", err)
		return
	}
	if err := c.svc.WriteCoAuthors(run.ID, trailers); err != nil {
		slog.Warn("scheduler: write run co-authors", "run", run.ID, "error", err)
	}
}
