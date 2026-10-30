package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// PRPollBusy is how often a pull request is read while its checks run,
	// a review is requested, or it just changed; PRPollIdle once nothing
	// changes. GitHub sends Aether no webhooks.
	PRPollBusy = time.Minute
	PRPollIdle = 5 * time.Minute
	// prReuse is how long one read serves every watch on the same pull
	// request, so the watches an event or a due pass evaluates together
	// send one request.
	prReuse = 15 * time.Second
	// prTokenTTL bounds how long a member's verified token is reused; a 401
	// verifies it again at once.
	prTokenTTL = 10 * time.Minute
	// DefaultPRConcurrency caps GitHub requests in flight across members.
	DefaultPRConcurrency = 4
)

// GitHub is what pull request watches read. Satisfied by *github.Service.
type GitHub interface {
	Credentials(ctx context.Context, member domain.MemberID) (string, github.Account, error)
	PullRequestSnapshot(ctx context.Context, token, repo string, number int, prev *github.Snapshot) (github.Snapshot, error)
	RequiredChecks(ctx context.Context, token string, prev, snap *github.Snapshot) error
}

// PRStore is the persistence pull request watches use.
type PRStore interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	RunPullRequests(ctx context.Context, run domain.RunID) ([]*store.RunPullRequest, error)
	ObserveRunPullRequest(ctx context.Context, pr *store.RunPullRequest) error
	WatchPRSnapshot(ctx context.Context, member domain.MemberID, repo string, number int) (*store.WatchPRSnapshot, error)
	SaveWatchPRSnapshot(ctx context.Context, s *store.WatchPRSnapshot) error
}

// PRConfig wires a PRs source. GitHub and Store are required. Bus, when
// set, carries the run.pr event a linked run's changed pull request
// publishes.
type PRConfig struct {
	GitHub      GitHub
	Store       PRStore
	Bus         events.Bus
	Concurrency int
	Now         func() time.Time
}

// PRs evaluates pull request conditions against GitHub with each watch
// owner's own credentials: at most one request in flight per member and
// Concurrency across members, and none before a rate limit resets.
type PRs struct {
	github GitHub
	store  PRStore
	bus    events.Bus
	now    func() time.Time
	global chan struct{}

	mu      sync.Mutex
	members map[domain.MemberID]*prMember
}

type prMember struct {
	turn     chan struct{}
	token    string
	tokenAt  time.Time
	limitErr *prError
	// refused is when each pull request, by repo#number, began answering
	// 403 or 404; one alone may be GitHub's own glitch, and every read
	// within prReuse of it counts as that one.
	refused map[string]time.Time
}

// NewPRs builds the pull request source for Service.UsePRSource.
func NewPRs(cfg PRConfig) (*PRs, error) {
	if cfg.GitHub == nil || cfg.Store == nil {
		return nil, errors.New("watch: pull requests need GitHub and Store")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultPRConcurrency
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PRs{
		github: cfg.GitHub, store: cfg.Store, bus: cfg.Bus, now: cfg.Now,
		global: make(chan struct{}, cfg.Concurrency), members: map[domain.MemberID]*prMember{},
	}, nil
}

// prError is an evaluation failure as the watch shows it. A transient one
// creates the watch in the error state instead of refusing it. A gone one
// means the login cannot see the pull request any more, so it is not
// retried on a timer.
type prError struct {
	msg       string
	transient bool
	gone      bool
	retryAt   time.Time
}

func (e *prError) Error() string { return e.msg }

func (e *prError) Is(target error) bool {
	return e.transient && target == ErrTransient || e.gone && target == errRevoked
}

// RetryAt is when GitHub accepts requests again after a rate limit; zero
// otherwise.
func (e *prError) RetryAt() time.Time { return e.retryAt }

const connectFix = "run aether github connect on your computer, or use Onboarding → Agent → GitHub → Connect GitHub in the dashboard"

func credentialsError(err error) *prError {
	return &prError{msg: "no usable GitHub login in your Aether home (" + err.Error() + "); " + connectFix, transient: true}
}

func (p *PRs) member(id domain.MemberID) *prMember {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.members[id]
	if m == nil {
		m = &prMember{turn: make(chan struct{}, 1), refused: map[string]time.Time{}}
		p.members[id] = m
	}
	return m
}

// lock waits for member's turn and a global slot; unlock releases both.
func (p *PRs) lock(ctx context.Context, m *prMember) error {
	select {
	case m.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case p.global <- struct{}{}:
		return nil
	case <-ctx.Done():
		<-m.turn
		return ctx.Err()
	}
}

func (p *PRs) unlock(m *prMember) {
	<-p.global
	<-m.turn
}

// token is member's verified GitHub token, cached for prTokenTTL. Called
// with m locked.
func (p *PRs) token(ctx context.Context, member domain.MemberID, m *prMember, fresh bool) (string, error) {
	if !fresh && m.token != "" && p.now().Sub(m.tokenAt) < prTokenTTL {
		return m.token, nil
	}
	m.token = ""
	token, _, err := p.github.Credentials(ctx, member)
	if err != nil {
		var limited *github.RateLimitError
		if errors.As(err, &limited) {
			return "", p.limited(m, limited)
		}
		return "", credentialsError(err)
	}
	m.token, m.tokenAt = token, p.now()
	return token, nil
}

func (p *PRs) limited(m *prMember, err *github.RateLimitError) *prError {
	m.limitErr = &prError{
		msg:       "GitHub rate limit, retrying at " + err.RetryAt.UTC().Format(time.RFC3339) + " (" + err.Error() + ")",
		transient: true, retryAt: err.RetryAt,
	}
	return m.limitErr
}

// snapshot reads repo#number with member's credentials, conditionally
// against the last read, which it reuses outright when it is younger than
// prReuse; when ci and that read skipped the required checks, it reads only
// those. create is a read for watch.create, which a 403 or 404 refuses at
// once. moved reports a fresh read that differs from the last one.
func (p *PRs) snapshot(ctx context.Context, member domain.MemberID, repo string, number int, ci, create bool) (snap github.Snapshot, moved bool, err error) {
	m := p.member(member)
	if err = p.lock(ctx, m); err != nil {
		return github.Snapshot{}, false, err
	}
	defer p.unlock(m)
	var prev *github.Snapshot
	checkedAt, recent := p.now(), false
	saved, err := p.store.WatchPRSnapshot(ctx, member, repo, number)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return github.Snapshot{}, false, err
	default:
		var s github.Snapshot
		if json.Unmarshal(saved.Snapshot, &s) == nil {
			if recent = p.now().Sub(saved.CheckedAt) < prReuse; recent && (!ci || s.RequiredRead) {
				return s, false, nil
			}
			prev = &s
			if recent {
				checkedAt = saved.CheckedAt
			}
		}
	}
	if m.limitErr != nil {
		if p.now().Before(m.limitErr.retryAt) {
			return github.Snapshot{}, false, m.limitErr
		}
		m.limitErr = nil
	}
	if snap, err = p.read(ctx, member, m, repo, number, prev, ci, recent, create); err != nil {
		return github.Snapshot{}, false, err
	}
	snap.HeadSince = p.now()
	if prev != nil && prev.HeadSHA == snap.HeadSHA && !prev.HeadSince.IsZero() {
		snap.HeadSince = prev.HeadSince
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return github.Snapshot{}, false, err
	}
	if err = p.store.SaveWatchPRSnapshot(ctx, &store.WatchPRSnapshot{MemberID: member, Repo: repo, Number: number, Snapshot: data, CheckedAt: checkedAt}); err != nil {
		return github.Snapshot{}, false, err
	}
	moved = prev == nil || prev.State != snap.State || prev.HeadSHA != snap.HeadSHA ||
		prev.Checks().Key != snap.Checks().Key || len(prev.Reviews) != len(snap.Reviews)
	return snap, moved, nil
}

// read reads repo#number conditionally against prev, and the required
// checks when ci; only the required checks when recent.
func (p *PRs) read(ctx context.Context, member domain.MemberID, m *prMember, repo string, number int, prev *github.Snapshot, ci, recent, create bool) (github.Snapshot, error) {
	cached := m.token != ""
	token, err := p.token(ctx, member, m, false)
	if err != nil {
		return github.Snapshot{}, err
	}
	get := func(token string) (github.Snapshot, error) {
		if recent {
			snap := *prev
			return snap, p.github.RequiredChecks(ctx, token, prev, &snap)
		}
		snap, readErr := p.github.PullRequestSnapshot(ctx, token, repo, number, prev)
		switch {
		case readErr != nil:
		case ci:
			readErr = p.github.RequiredChecks(ctx, token, prev, &snap)
		default:
			snap.KeepRequired(prev)
		}
		return snap, readErr
	}
	snap, err := get(token)
	if errors.Is(err, github.ErrUnauthorized) && cached {
		if token, err = p.token(ctx, member, m, true); err != nil {
			return github.Snapshot{}, err
		}
		snap, err = get(token)
	}
	ref := fmt.Sprintf("%s#%d", repo, number)
	firstRefused, refusedBefore := m.refused[ref]
	var limited *github.RateLimitError
	var apiErr *github.APIError
	var pull *github.PullRequestError
	refusal := !errors.Is(err, github.ErrSSO) && errors.As(err, &pull) && errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden)
	if !refusal {
		delete(m.refused, ref)
	}
	switch {
	case err == nil:
		return snap, nil
	case errors.As(err, &limited):
		return github.Snapshot{}, p.limited(m, limited)
	case errors.Is(err, github.ErrUnauthorized):
		m.token = ""
		return github.Snapshot{}, credentialsError(err)
	case errors.Is(err, github.ErrSSO):
		return github.Snapshot{}, &prError{transient: true, msg: fmt.Sprintf(
			"read pull request %s: your GitHub token needs SAML single sign-on authorization for this organization (%v)", ref, err)}
	case errors.As(err, &pull) && errors.Is(err, github.ErrMoved):
		return github.Snapshot{}, &prError{gone: true, msg: fmt.Sprintf(
			"pull request %s is not reachable with your GitHub login: GitHub answers 301, the repository was renamed or transferred (%v); create the watch again under its new owner/name", ref, err)}
	case refusal:
		cause := "GitHub answers 404, so the pull request or repository is gone or your login lost access to it"
		if apiErr.Status == http.StatusForbidden {
			cause = "GitHub answers 403, so your login may no longer read it"
		}
		msg := fmt.Sprintf("pull request %s is not reachable with your GitHub login: %s (%v); give that login access, or connect one that has it with aether github connect", ref, cause, err)
		if !refusedBefore {
			firstRefused = p.now()
		}
		m.refused[ref] = firstRefused
		if !create && p.now().Sub(firstRefused) < prReuse {
			return github.Snapshot{}, &prError{msg: msg + "; asking once more before the watch stops"}
		}
		return github.Snapshot{}, &prError{gone: true, msg: msg + ", then create the watch again"}
	case ctx.Err() != nil:
		return github.Snapshot{}, err
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden:
		return github.Snapshot{}, &prError{transient: true, msg: fmt.Sprintf(
			"read pull request %s: GitHub refuses its checks, statuses or reviews to your login (%v); a fine-grained token needs Checks, Commit statuses and Pull requests read", ref, err)}
	}
	return github.Snapshot{}, &prError{msg: fmt.Sprintf("read pull request %s: %v", ref, err), transient: true}
}

// Validate implements PRSource.
func (p *PRs) Validate(ctx context.Context, member domain.MemberID, target protocol.WatchTarget) (string, error) {
	if target.Kind != protocol.WatchTargetPR {
		m := p.member(member)
		if err := p.lock(ctx, m); err != nil {
			return "", err
		}
		defer p.unlock(m)
		_, err := p.token(ctx, member, m, false)
		return "", err
	}
	snap, _, err := p.snapshot(ctx, member, strings.ToLower(target.Repo), target.Number, false, true)
	if err != nil {
		return "", err
	}
	if snap.State != "open" {
		return "", fmt.Errorf("pull request %s#%d is already %s on GitHub; there is nothing left to watch", target.Repo, target.Number, snap.State)
	}
	return fmt.Sprintf("%s#%d: %s", target.Repo, target.Number, snap.Title), nil
}

// prTarget is one pull request an evaluation reads, and the run that
// recorded it, if any.
type prTarget struct {
	repo   string
	number int
	run    domain.RunID
	prior  *store.RunPullRequest
}

// Evaluate implements PRSource. A pull request that cannot be read fails
// the evaluation, but the notices of the others it read are returned with
// the error, so one unreadable pull request of a run never hides another's.
func (p *PRs) Evaluate(ctx context.Context, check PRCheck) (PRResult, error) {
	targets, err := p.targets(ctx, check.Target)
	if err != nil {
		return PRResult{}, err
	}
	if len(targets) == 0 {
		return PRResult{Observed: protocol.WatchObserved{Summary: "No pull request recorded yet"}, Final: true}, nil
	}
	res := PRResult{Final: true}
	var failed error
	busy := false
	ci := slices.Contains(check.Conditions, protocol.WatchCI)
	for i, t := range targets {
		snap, moved, err := p.snapshot(ctx, check.Member, t.repo, t.number, ci, false)
		if err == nil && t.run != "" {
			err = p.refreshRun(ctx, t, snap)
		}
		var gone *prError
		if errors.As(err, &gone) && gone.gone && check.Target.Kind != protocol.WatchTargetPR {
			// Its run still has conditions to watch.
			err = &prError{msg: gone.msg}
		}
		if err != nil {
			failed = cmp.Or(failed, err)
			continue
		}
		r := evaluatePR(snap, check.Conditions, check.Since, p.now())
		res.Notices = append(res.Notices, r.notices...)
		if i == 0 {
			res.Title, res.Observed = r.title, r.observed
		}
		res.Final = res.Final && r.final
		busy = busy || r.busy || moved
	}
	if failed != nil {
		return PRResult{Notices: res.Notices}, failed
	}
	if !res.Final {
		wait := PRPollIdle
		if busy || changed(check.Previous, res.Observed) {
			wait = PRPollBusy
		}
		res.NextCheck = p.now().Add(jitter(wait))
	}
	return res, nil
}

// targets lists the pull requests a watch follows: a pr target's own, or
// every one its run recorded, newest first.
func (p *PRs) targets(ctx context.Context, t protocol.WatchTarget) ([]prTarget, error) {
	run := domain.RunID(t.RunID)
	var recorded []*store.RunPullRequest
	if run != "" {
		var err error
		if recorded, err = p.store.RunPullRequests(ctx, run); err != nil {
			return nil, err
		}
	}
	if t.Kind == protocol.WatchTargetPR {
		target := prTarget{repo: strings.ToLower(t.Repo), number: t.Number, run: run}
		for _, r := range recorded {
			if r.Repository == target.repo && r.Number == target.number {
				target.prior = r
			}
		}
		return []prTarget{target}, nil
	}
	out := make([]prTarget, 0, len(recorded))
	for _, r := range recorded {
		out = append(out, prTarget{repo: r.Repository, number: r.Number, run: run, prior: r})
	}
	return out, nil
}

// refreshRun records what GitHub says about a run's pull request, and
// publishes run.pr when that changes it, so every surface showing the run
// sees a merge. A failed record fails the evaluation, which retries it; a
// failed publish is only logged, as sshd's is.
func (p *PRs) refreshRun(ctx context.Context, t prTarget, snap github.Snapshot) error {
	if t.prior == nil || !prChanged(t.prior, snap) {
		return nil
	}
	run, err := p.store.GetRun(ctx, t.run)
	if err != nil {
		return fmt.Errorf("record run %s pull request: %w", t.run, err)
	}
	if err := p.store.ObserveRunPullRequest(ctx, &store.RunPullRequest{
		RunID: t.run, Repository: t.repo, Number: t.number, URL: snap.URL, State: snap.State, MergedAt: snap.MergedAt, ObservedAt: p.now().UTC(),
	}); err != nil {
		return fmt.Errorf("record run %s pull request: %w", t.run, err)
	}
	if p.bus == nil {
		return nil
	}
	if _, err := p.bus.Publish(ctx, events.Event{WorkspaceID: run.WorkspaceID, RunID: run.ID, Payload: events.RunPRPayload{
		RunID: run.ID, Number: t.number, URL: snap.URL, State: snap.State, MergedAt: snap.MergedAt,
	}}); err != nil {
		slog.Warn("watch: publish run pull request", "run", t.run, "error", err)
	}
	return nil
}

// prChanged is sshd's rule for recording a run's pull request: a merge is
// final, and anything else that differs is news.
func prChanged(recorded *store.RunPullRequest, snap github.Snapshot) bool {
	return recorded.State != "merged" && recorded.State != snap.State || recorded.MergedAt == nil && snap.MergedAt != nil
}
