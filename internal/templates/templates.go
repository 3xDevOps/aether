// Package templates is task templates and their cron schedules.
//
// A template is a named, parameterized run definition stored on a session:
// agent, task prompt with {{placeholders}}, launch mode, and an advisory
// budget hint. Any collaborator may launch one; only an admin may create,
// change, or delete one. Launching a template is not a second launch path
// - it renders the prompt and calls the same scheduler entry point a
// hand-launched run takes, so the resulting run is indistinguishable.
//
// A schedule fires its template unattended on a cron rule. Two properties
// matter and are deliberate:
//
//   - Missed slots are skipped, never caught up. Next fire times are
//     computed forward from the moment the service first sees a schedule,
//     so a server that was down all night wakes up with nothing owed and
//     no storm of overdue runs.
//   - Every fire re-checks the launch capability against the creating
//     member's current row. The scheduler is permission-gated only at the
//     RPC boundary, so without this a demoted or removed member would keep
//     launching runs forever through their schedule.
//   - A fire supplies no parameters, so only a template that renders from
//     its defaults alone may be scheduled, and a scheduled template may not
//     be edited into a prompt it can no longer render. Otherwise a schedule
//     reports a next fire time it can never honour.
//
// Cron-fired runs start from whatever base the server last saw: it never
// fetches upstream on its own, it only learns about upstream from member
// pushes. The base branch's age is therefore reported at every launch, in
// the CLI's output and in the timeline entry each fire stamps.
package templates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// DefaultInterval is how often the cron loop looks for due schedules.
const DefaultInterval = 30 * time.Second

// ErrUnknownParam is returned when a launch supplies a parameter the
// template's prompt does not use.
var ErrUnknownParam = errors.New("templates: unknown parameter")

// ErrMissingParam is returned when a template's prompt has a placeholder
// with neither a default nor a supplied value.
var ErrMissingParam = errors.New("templates: missing parameter")

// ErrInvalidDefinition is returned when a template or schedule is
// malformed: a missing field, a bad name or mode, an unparseable cron
// rule.
var ErrInvalidDefinition = errors.New("templates: invalid definition")

// Launcher is the scheduler seam: the one entry point every run comes
// through, hand-launched or fired by cron. The server supplies the same
// guarded controller its RPC handlers use, so a template launch and a
// cron fire are admitted against the session budget like any other run.
type Launcher interface {
	Launch(ctx context.Context, session domain.SessionID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error)
}

// BaseResolver reports when a workspace branch was last committed as the
// server currently sees it. Satisfied by RepoBase.
type BaseResolver interface {
	BaseCommitTime(ctx context.Context, ws domain.WorkspaceID, branch string) (time.Time, error)
}

// Config wires the service. Store, Bus, Runs, and Base are required.
type Config struct {
	Store store.Store
	Bus   events.Bus
	Runs  Launcher
	Base  BaseResolver
	// Interval overrides DefaultInterval, the cron scan period.
	Interval time.Duration
	// Now overrides the clock; tests drive schedules through it.
	Now func() time.Time
}

// Service is template CRUD plus the cron loop. Its exported method set
// satisfies the sshd.TemplateService seam.
type Service struct {
	store    store.Store
	bus      events.Bus
	runs     Launcher
	base     BaseResolver
	interval time.Duration
	now      func() time.Time

	wg   sync.WaitGroup
	stop context.CancelFunc

	mu     sync.Mutex
	timers map[string]*timer
	closed bool
}

// timer is one schedule's parsed rule and the next instant it is due.
type timer struct {
	spec string
	rule cron.Schedule
	next time.Time
}

// BaseInfo is the age of the base branch a run starts from, as the server
// last saw it. Known is false when the branch has no commit the server has
// seen - an unpushed repo, a branch that only exists on a laptop.
type BaseInfo struct {
	Branch string
	Age    time.Duration
	Known  bool
}

// String renders the base age the way both the CLI and the timeline
// report it.
func (b BaseInfo) String() string {
	if !b.Known {
		return "base " + b.Branch + " has no commit the server has seen"
	}
	return "base " + b.Branch + " is " + FormatAge(b.Age) + " old"
}

// Launched is a template launch: the run plus the base it started from.
type Launched struct {
	Run  *domain.Run
	Base BaseInfo
}

// ScheduleInfo is a stored schedule plus the next instant it is due. Next
// is zero when the service has not started.
type ScheduleInfo struct {
	Schedule *store.Schedule
	Next     time.Time
}

// New builds the service; call Start to begin firing schedules.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil || cfg.Runs == nil || cfg.Base == nil {
		return nil, errors.New("templates: Store, Bus, Runs, and Base are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:    cfg.Store,
		bus:      cfg.Bus,
		runs:     cfg.Runs,
		base:     cfg.Base,
		interval: cfg.Interval,
		now:      cfg.Now,
		timers:   make(map[string]*timer),
	}, nil
}

// Start seeds every stored schedule's next fire time from now - which is
// what makes downtime skip rather than accumulate - and begins the cron
// loop. ctx bounds only the setup; the loop runs until Close.
func (s *Service) Start(ctx context.Context) error {
	if err := s.scan(ctx, false); err != nil {
		return err
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(loopCtx)
	}()
	return nil
}

// Close stops the cron loop and waits for an in-flight fire to finish.
// Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
	return nil
}

// List returns a session's templates by name.
func (s *Service) List(ctx context.Context, session domain.SessionID) ([]*store.Template, error) {
	return s.store.ListTemplates(ctx, session)
}

// Save creates or replaces a template after validating its prompt,
// parameters, and mode. An empty mode becomes headless: templates exist
// for unattended work.
func (s *Service) Save(ctx context.Context, t *store.Template) error {
	if t.Name == "" || t.Task == "" || t.Harness == "" {
		return fmt.Errorf("%w: name, task, and agent are required", ErrInvalidDefinition)
	}
	if err := validName(t.Name); err != nil {
		return err
	}
	if t.Mode == "" {
		t.Mode = domain.LaunchHeadless
	}
	if !t.Mode.Valid() {
		return fmt.Errorf("%w: invalid mode %q", ErrInvalidDefinition, t.Mode)
	}
	if err := checkParams(t.Task, t.Params); err != nil {
		return err
	}
	if _, err := Render(t.Task, t.Params, nil); err != nil {
		scheduled, serr := s.scheduled(ctx, t.SessionID, t.Name)
		if serr != nil {
			return fmt.Errorf("templates: save %s: %w", t.Name, serr)
		}
		if scheduled {
			return fmt.Errorf("%w: template %q has a schedule and could no longer fire unattended: %s",
				ErrInvalidDefinition, t.Name, err)
		}
	}
	if _, err := s.store.GetSession(ctx, t.SessionID); err != nil {
		return fmt.Errorf("templates: save %s: %w", t.Name, err)
	}
	if err := s.store.SaveTemplate(ctx, t); err != nil {
		return fmt.Errorf("templates: save %s: %w", t.Name, err)
	}
	return nil
}

// scheduled reports whether a template name already has a cron rule.
func (s *Service) scheduled(ctx context.Context, session domain.SessionID, name string) (bool, error) {
	list, err := s.store.ListSchedules(ctx, session)
	if err != nil {
		return false, fmt.Errorf("list schedules: %w", err)
	}
	for _, sc := range list {
		if sc.Template == name {
			return true, nil
		}
	}
	return false, nil
}

// Delete removes a template and, with it, its schedule.
func (s *Service) Delete(ctx context.Context, session domain.SessionID, name string) error {
	if err := s.store.DeleteTemplate(ctx, session, name); err != nil {
		return fmt.Errorf("templates: delete %s: %w", name, err)
	}
	return nil
}

// Launch renders a template and launches it as member. Permission is the
// caller's business: this is the manually invoked path, guarded at the RPC
// boundary like any other launch.
func (s *Service) Launch(ctx context.Context, session domain.SessionID, name string, member domain.MemberID, params map[string]string) (*Launched, error) {
	t, err := s.store.GetTemplate(ctx, session, name)
	if err != nil {
		return nil, fmt.Errorf("templates: launch %s: %w", name, err)
	}
	return s.launch(ctx, t, member, params)
}

func (s *Service) launch(ctx context.Context, t *store.Template, member domain.MemberID, params map[string]string) (*Launched, error) {
	task, err := Render(t.Task, t.Params, params)
	if err != nil {
		return nil, fmt.Errorf("templates: launch %s: %w", t.Name, err)
	}
	base := s.baseInfo(ctx, t.SessionID)
	run, err := s.runs.Launch(ctx, t.SessionID, member, task, t.Harness, t.Mode)
	if err != nil {
		return nil, fmt.Errorf("templates: launch %s: %w", t.Name, err)
	}
	return &Launched{Run: run, Base: base}, nil
}

// baseInfo reports the age of the session's base branch as the server
// currently sees it. A lookup failure is reported as unknown age, never as
// a failed launch: the run is still correct, only its freshness is
// uncertain.
func (s *Service) baseInfo(ctx context.Context, session domain.SessionID) BaseInfo {
	sess, err := s.store.GetSession(ctx, session)
	if err != nil {
		return BaseInfo{}
	}
	info := BaseInfo{Branch: sess.BaseBranch}
	committed, err := s.base.BaseCommitTime(ctx, sess.WorkspaceID, sess.BaseBranch)
	if err != nil {
		slog.Debug("templates: base branch age unavailable",
			"session", session, "branch", sess.BaseBranch, "error", err)
		return info
	}
	info.Age = s.now().Sub(committed)
	if info.Age < 0 {
		info.Age = 0
	}
	info.Known = true
	return info
}

func (s *Service) publish(ctx context.Context, session domain.SessionID, run domain.RunID, actor domain.MemberID, message string) {
	if _, err := s.bus.Publish(ctx, events.Event{
		SessionID: session,
		RunID:     run,
		ActorID:   actor,
		Payload:   events.TimelinePayload{Kind: events.TimelineNote, Message: message},
	}); err != nil {
		slog.Warn("templates: publish timeline entry", "session", session, "error", err)
	}
}

// FormatAge renders a duration the way run and base ages are reported:
// the coarsest unit that is not zero.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
