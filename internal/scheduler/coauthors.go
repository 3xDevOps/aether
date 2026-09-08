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

// coAuthorTrailers renders one Co-authored-by line per member in members
// who is not already credited by the commit itself. A member who set no
// git identity is credited by the fallback domain.GitIdentity gives them.
//
// credited holds the addresses the commit already carries - the address it
// is authored as. That is the whole rule: everyone involved, less whoever
// the commit is already signed by. An owner filter on top of it would be
// wrong for the container's own commits, which keep authoring as whoever
// launched the run however often it changes hands.
func coAuthorTrailers(members []*domain.Member, credited ...string) []string {
	trailers := make([]string, 0, len(members))
	seen := make(map[string]bool, len(members)+len(credited))
	for _, address := range credited {
		if address != "" {
			seen[strings.ToLower(address)] = true
		}
	}
	for _, m := range members {
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

// runCoAuthors is that list for one run, read from the store, less the
// addresses the commit it will ride on already carries.
func (s *Scheduler) runCoAuthors(ctx context.Context, run *domain.Run, credited ...string) ([]string, error) {
	steerers, err := s.cfg.Store.ListRunSteerers(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list run steerers: %w", err)
	}
	return coAuthorTrailers(steerers, credited...), nil
}

// containerCoAuthors is the list the run's own agent is told to append:
// everyone the run involves - its owner and everyone who has steered it -
// less the address the container already authors as.
//
// The owner belongs on it because the container's author is frozen when it
// is created. After a handoff the agent still commits as whoever launched
// the run, so the member now directing it is credited on none of those
// commits unless a trailer says so. Before a handoff the owner is that
// frozen address and drops right back out.
func (s *Scheduler) containerCoAuthors(ctx context.Context, run *domain.Run, author string) ([]string, error) {
	steerers, err := s.cfg.Store.ListRunSteerers(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list run steerers: %w", err)
	}
	members := steerers
	if owner, oerr := s.cfg.Store.GetMember(ctx, run.MemberID); oerr != nil {
		slog.Warn("scheduler: resolve run owner for co-authors", "run", run.ID, "error", oerr)
	} else {
		members = append([]*domain.Member{owner}, steerers...)
	}
	return coAuthorTrailers(members, author), nil
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
		trailers, terr := s.runCoAuthors(ctx, r, author.Email)
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
//
// A run credits its current owner as well as its steerers, because the
// container goes on authoring as whoever launched it. The owner of a run
// they took over in a handoff is on its list without being one of its
// steerers, so matching on the steerers alone would leave their old
// address in the container.
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
		if r.MemberID == member {
			s.refreshCoAuthors(ctx, r)
			continue
		}
		steerers, lerr := s.cfg.Store.ListRunSteerers(ctx, r.ID)
		if lerr != nil {
			slog.Warn("scheduler: list run steerers", "run", r.ID, "error", lerr)
			continue
		}
		if slices.ContainsFunc(steerers, func(m *domain.Member) bool { return m.ID == member }) {
			s.refreshCoAuthors(ctx, r)
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
// store. Only a run holding a provisioned coordination directory has one to
// rewrite; for any other run the trailers still reach the branch through
// commitAll.
//
// The list is everyone the run involves less the address the container's
// own commits are authored as. That address is frozen when the container
// is created, so telling the agent to append a trailer for itself would
// credit the author twice, while the member who took the run over after a
// handoff is credited nowhere else on those commits.
func (s *Scheduler) refreshCoAuthors(ctx context.Context, run *domain.Run) {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run.ID]
	var dir, author string
	if entry != nil {
		dir, author = entry.coordDir, entry.gitAuthorEmail
	}
	s.mu.Unlock()
	if c == nil || dir == "" {
		return
	}
	entry.coAuthorMu.Lock()
	defer entry.coAuthorMu.Unlock()
	trailers, err := s.containerCoAuthors(ctx, run, author)
	if err != nil {
		slog.Warn("scheduler: resolve co-authors", "run", run.ID, "error", err)
		return
	}
	if err := c.svc.WriteCoAuthors(run.ID, trailers); err != nil {
		slog.Warn("scheduler: write run co-authors", "run", run.ID, "error", err)
	}
}
