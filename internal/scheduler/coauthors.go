package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"slices"
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
	" and it changes while you work. End every commit message you write, and the description of any pull" +
	" request you open, with exactly those lines. If the file is missing or empty, add nothing."

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

// coAuthorTrailers renders one Co-authored-by line per member in steerers
// who does not own the run now. The owner is the commit's author, and a
// handoff can hand the run to someone already on the list. A member who
// set no git identity is credited by the fallback domain.GitIdentity
// gives them.
func coAuthorTrailers(steerers []*domain.Member, owner domain.MemberID) []string {
	trailers := make([]string, 0, len(steerers))
	seen := make(map[string]bool, len(steerers))
	for _, m := range steerers {
		if m.ID == owner {
			continue
		}
		id := m.GitIdentity()
		// Two members can stand behind one address - a shared account, or
		// the same person joined from two machines. Git and GitHub credit
		// the address, so a second identical trailer says nothing.
		key := strings.ToLower(id.Email)
		if seen[key] {
			continue
		}
		seen[key] = true
		trailers = append(trailers, id.Trailer())
	}
	return trailers
}

// runCoAuthors is that list for one run, read from the store.
func (s *Scheduler) runCoAuthors(ctx context.Context, run *domain.Run) ([]string, error) {
	steerers, err := s.cfg.Store.ListRunSteerers(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list run steerers: %w", err)
	}
	return coAuthorTrailers(steerers, run.MemberID), nil
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
		trailers, terr := s.runCoAuthors(ctx, r)
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
// injected a message, or typed into its terminal. The first time for a
// given member it stamps the workspace timeline and refreshes the list the
// container reads; later calls are no-ops.
func (s *Scheduler) RecordSteer(ctx context.Context, run domain.RunID, actor domain.MemberID) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve run for steer record", "run", run, "error", err)
		return
	}
	if s.addSteerer(ctx, r, actor) {
		s.refreshCoAuthors(ctx, r)
	}
}

// RecordHandoff credits the outgoing owner of a run and rewrites what the
// container reads. Ownership is what separates the author from the
// co-authors, so a transfer swaps two lines at once: the member who just
// gave the run away joins the steerers, and the one who took it stops
// being credited as their own co-author.
func (s *Scheduler) RecordHandoff(ctx context.Context, run domain.RunID, from domain.MemberID) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve run after handoff", "run", run, "error", err)
		return
	}
	s.addSteerer(ctx, r, from)
	s.refreshCoAuthors(ctx, r)
}

// RefreshMemberCoAuthors rewrites the co-author list of every live run
// that credits member, after their git identity changed. It reaches the
// trailers only: a container's own GIT_AUTHOR_* is fixed when it is
// created, so the agent's author line keeps the identity the run started
// with (docs/teams.md).
func (s *Scheduler) RefreshMemberCoAuthors(ctx context.Context, member domain.MemberID) {
	if s.coordinationSeam() == nil {
		return
	}
	runs, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: list runs for co-author refresh", "member", member, "error", err)
		return
	}
	for _, r := range runs {
		steerers, lerr := s.cfg.Store.ListRunSteerers(ctx, r.ID)
		if lerr != nil {
			slog.Warn("scheduler: list run steerers", "run", r.ID, "error", lerr)
			continue
		}
		if slices.ContainsFunc(steerers, func(m *domain.Member) bool { return m.ID == member }) {
			s.writeCoAuthors(r, coAuthorTrailers(steerers, r.MemberID))
		}
	}
}

// addSteerer records member as a steerer of run and stamps the timeline
// the first time. It reports whether this call was the one that added
// them. The owner is the author, so recording them would be noise.
func (s *Scheduler) addSteerer(ctx context.Context, run *domain.Run, member domain.MemberID) bool {
	if member == "" || member == run.MemberID {
		return false
	}
	added, err := s.cfg.Store.AddRunSteerer(ctx, run.ID, member)
	if err != nil {
		slog.Warn("scheduler: record run steerer", "run", run.ID, "member", member, "error", err)
		return false
	}
	if added {
		s.publishTimeline(ctx, run.WorkspaceID, run.ID, member, events.TimelineCoAuthor, "")
	}
	return added
}

// refreshCoAuthors rewrites the run container's co-author list from the
// store.
func (s *Scheduler) refreshCoAuthors(ctx context.Context, run *domain.Run) {
	trailers, err := s.runCoAuthors(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve co-authors", "run", run.ID, "error", err)
		return
	}
	s.writeCoAuthors(run, trailers)
}

// writeCoAuthors hands the list to the coordination service. Only a run
// holding a provisioned coordination directory has one to rewrite; for any
// other run the trailers still reach the branch through commitAll.
func (s *Scheduler) writeCoAuthors(run *domain.Run, trailers []string) {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run.ID]
	provisioned := entry != nil && entry.coordDir != ""
	s.mu.Unlock()
	if c == nil || !provisioned {
		return
	}
	if err := c.svc.WriteCoAuthors(run.ID, trailers); err != nil {
		slog.Warn("scheduler: write run co-authors", "run", run.ID, "error", err)
	}
}
