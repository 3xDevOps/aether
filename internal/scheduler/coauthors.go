package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
)

// coAuthorsPath is where the run's co-author list appears inside its
// container, under the read-only coordination mount.
var coAuthorsPath = path.Join(coordtransport.MountDir, coord.CoAuthorsName)

// coAuthorInstruction tells the agent to credit the people steering it.
// The list changes while the agent works, so re-reading it is part of the
// instruction.
var coAuthorInstruction = "Before each commit, read " + coAuthorsPath +
	". It holds one Co-authored-by trailer per person other than the run owner who has steered this run," +
	" and it changes while you work. End every commit message you write, and the description of any pull" +
	" request you open, with exactly those lines. If the file is missing or empty, add nothing." +
	" After those lines, end every pull request description with \"Opened from Aether run <run id>\", using the run_id" +
	" aether-internal status reports."

// runInstructions are the rules a run's agent follows: its role card, with
// the report rules only while lifecycle reporting is enabled, and the
// co-author rule. A run without a run transport has no mounted directory to
// read the files from and gets none.
func (s *Scheduler) runInstructions(g coordhooks.Guide) string {
	c := s.coordinationSeam()
	if c == nil || c.svc == nil {
		return ""
	}
	if !c.enabled {
		g.Role = ""
	}
	return coordhooks.RoleCard(g) + "\n\n" + coAuthorInstruction
}

// briefedInstructions are the run instructions followed by the team memory
// brief of the run's workspace and owner. Only a launch that carries
// instructions carries the brief.
func (s *Scheduler) briefedInstructions(ctx context.Context, g coordhooks.Guide, repos []domain.RunRepo, workspace domain.WorkspaceID, owner domain.MemberID) (string, error) {
	listed := make([]string, len(repos))
	for i, repo := range repos {
		listed[i] = repo.Name + " at " + repo.Path
	}
	g.Repos = strings.Join(listed, ", ")
	text := s.runInstructions(g)
	if text == "" || s.cfg.Memory == nil {
		return text, nil
	}
	brief, err := s.cfg.Memory.Brief(ctx, workspace, owner, domain.RunID(g.RunID))
	if err != nil {
		return "", fmt.Errorf("build the team memory brief: %w", err)
	}
	if brief == "" {
		return text, nil
	}
	return text + "\n\n" + brief, nil
}

// runGuide is what a run's role card depends on: background marks a
// headless run and wake a launch that Aether resumes when mail arrives while
// it is idle.
func (s *Scheduler) runGuide(ctx context.Context, run domain.RunID, background, wake bool) coordhooks.Guide {
	g := coordhooks.Guide{RunID: string(run), Background: background, Wake: wake && !background}
	var r *domain.Run
	if run != "" {
		r, _ = s.cfg.Store.GetRun(ctx, run)
	}
	if background {
		g.Role = coordhooks.RoleBackground
	} else {
		g.Role = coordhooks.RoleInteractive
	}
	if r != nil {
		g.Parent = string(r.ParentRunID)
	}
	return g
}

// launchInstructions are the instructions a non-ACP launch of run carries in
// its system prompt, or none for a profile that has no place for them. A
// member's own developer_instructions, which the launch's setting would
// replace, come first.
func (s *Scheduler) launchInstructions(ctx context.Context, run *domain.Run, profile harness.Profile, native harness.NativeLaunch) (string, error) {
	if !profile.CarriesInstructions() {
		return "", nil
	}
	text, err := s.briefedInstructions(ctx, s.runGuide(ctx, run.ID, run.Mode == domain.LaunchHeadless, native.IdleWake), run.Extras, run.WorkspaceID, run.MemberID)
	if err != nil {
		return "", err
	}
	if text == "" || profile.InstructionsConfig == "" || s.cfg.Homes == nil {
		return text, nil
	}
	config, err := s.cfg.Homes.HomeRead(ctx, run.HomeMember(), profile.InstructionsConfig)
	// Aether refuses to follow a linked config; Codex still reads it, and
	// the launch's setting replaces its value.
	if errors.Is(err, memberhome.ErrConfigNotFound) || errors.Is(err, memberhome.ErrConfigDenied) {
		return text, nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", profile.InstructionsConfig, err)
	}
	kept, err := harness.ConfiguredInstructions(config.Content)
	if err != nil {
		return "", fmt.Errorf("read developer_instructions from %s: %w", profile.InstructionsConfig, err)
	}
	if kept == "" {
		return text, nil
	}
	return kept + "\n\n" + text, nil
}

// withInstructions appends the run instructions to a task prompt, for a
// launch that carries them in no system prompt. A taskless launch remains
// taskless.
func withInstructions(task, instructions string) string {
	if task == "" || instructions == "" {
		return task
	}
	return task + "\n\n" + instructions
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
	var signingKey []byte
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: resolve run for commit author", "run", run, "error", err)
	} else {
		// The run's owner is both who the commit is authored as and whose
		// key signs it. A key the server cannot read costs the signature,
		// not the commit.
		if s.cfg.Signing != nil {
			key, kerr := s.cfg.Signing.Key(r.MemberID)
			if kerr != nil {
				slog.Warn("scheduler: read commit signing key", "run", run, "error", kerr)
			} else {
				signingKey = key
			}
		}
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
	return s.cfg.Git.CommitAll(ctx, run, message, author, signingKey)
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
		s.refreshCoAuthors(ctx, r.ID)
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
	s.refreshCoAuthors(ctx, r.ID)
}

// RecordHandoffOperation applies the co-author phase of one durable handoff.
// Both the steerer row and its deterministic timeline entry are idempotent,
// so a retry after either write boundary converges without duplicate credit.
func (s *Scheduler) RecordHandoffOperation(ctx context.Context, run domain.RunID, from domain.MemberID, operationID string, at time.Time) error {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("scheduler: resolve run after handoff: %w", err)
	}
	if operationID == "" {
		return fmt.Errorf("scheduler: handoff operation ID is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if from != "" && from != r.MemberID {
		if _, err := s.cfg.Store.AddRunSteerer(ctx, run, from); err != nil {
			return fmt.Errorf("scheduler: record outgoing handoff owner: %w", err)
		}
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{
			ID: "handoff:" + operationID + ":coauthor", Time: at,
			WorkspaceID: r.WorkspaceID, RunID: run, ActorID: from,
			Payload: events.TimelinePayload{Kind: events.TimelineCoAuthor},
		}); err != nil {
			return fmt.Errorf("scheduler: publish handoff co-author: %w", err)
		}
	}
	if err := s.refreshCoAuthorsErr(ctx, run); err != nil {
		return err
	}
	return nil
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
			s.refreshCoAuthors(ctx, r.ID)
			continue
		}
		steerers, lerr := s.cfg.Store.ListRunSteerers(ctx, r.ID)
		if lerr != nil {
			slog.Warn("scheduler: list run steerers", "run", r.ID, "error", lerr)
			continue
		}
		if slices.ContainsFunc(steerers, func(m *domain.Member) bool { return m.ID == member }) {
			s.refreshCoAuthors(ctx, r.ID)
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
func (s *Scheduler) refreshCoAuthors(ctx context.Context, run domain.RunID) {
	if err := s.refreshCoAuthorsErr(ctx, run); err != nil {
		slog.Warn("scheduler: refresh co-authors", "run", run, "error", err)
	}
}

func (s *Scheduler) refreshCoAuthorsErr(ctx context.Context, run domain.RunID) error {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run]
	var dir, author string
	if entry != nil {
		dir, author = entry.coordDir, entry.gitAuthorEmail
	}
	s.mu.Unlock()
	if c == nil || dir == "" {
		return nil
	}
	// The run is read under the lock, so a handoff committed after the read
	// refreshes after this write and the list ends on the current owner.
	entry.coAuthorMu.Lock()
	defer entry.coAuthorMu.Unlock()
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("scheduler: resolve run for co-authors: %w", err)
	}
	trailers, err := s.containerCoAuthors(ctx, r, author)
	if err != nil {
		return fmt.Errorf("scheduler: resolve co-authors: %w", err)
	}
	if err := c.svc.WriteCoAuthors(run, trailers); err != nil {
		return fmt.Errorf("scheduler: write run co-authors: %w", err)
	}
	return nil
}

// taskLine reduces a task to its commit-message form: first line only,
// truncated to 72 characters.
func taskLine(task string) string {
	if i := strings.IndexAny(task, "\r\n"); i >= 0 {
		task = task[:i]
	}
	if r := []rune(task); len(r) > 72 {
		task = string(r[:72])
	}
	return task
}
