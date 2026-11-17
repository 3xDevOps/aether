package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/store"
	"github.com/3xDevOps/cloudfield/internal/version"
)

// ServerActor is the actor of an update the server scheduled itself; no
// member ID has this form.
const ServerActor domain.MemberID = "server"

// selfApplies reports whether a server following channel schedules the
// newer builds its release check finds by itself. Every channel does;
// for nightly only it would return channel == selfupdate.ChannelNightly.
// A server on a fixed feed (Config.Releases, channel "") never does.
func selfApplies(channel string) bool { return channel != "" }

// errNotTaken refuses an update at its idle moment that the server's
// channel no longer takes, or that the server scheduled itself and has
// since declined. It is no decline of the release itself.
var errNotTaken = errors.New("the server no longer takes this update")

// published reports whether this binary is a published release: a build
// from source, past a tag or dirty, never replaces itself unattended.
func published() bool {
	return selfupdate.ValidTag(version.Version) && version.ReleaseCore(version.Version) == version.Version
}

// refuses reports whether d declines r: r is not above d's build.
func refuses(d *store.DeclinedServerUpdate, r selfupdate.Release) bool {
	return d != nil && (r.Build < d.Build || r.Build == d.Build && r.Hotfix <= d.Hotfix)
}

func wireDeclined(d *store.DeclinedServerUpdate) *protocol.ReleaseBuild {
	if d == nil {
		return nil
	}
	return &protocol.ReleaseBuild{Version: d.Version, Build: d.Build, Hotfix: d.Hotfix}
}

// raiseDeclined records r in st as declined, unless a higher build is
// declined already, so the server schedules by itself only a build above
// it. A super admin's own server.update ignores it. A release whose build
// is not known is not recorded, reported false: a mark by version alone
// could not be ordered against the next one.
func raiseDeclined(ctx context.Context, st store.Store, r selfupdate.Release) (bool, error) {
	if r.Build == 0 {
		return false, nil
	}
	if err := st.RaiseDeclinedServerUpdate(ctx, store.DeclinedServerUpdate{Version: r.Version, Build: r.Build, Hotfix: r.Hotfix}); err != nil {
		return false, fmt.Errorf("serverupdate: record %s as declined: %w", r.Version, err)
	}
	return true, nil
}

func (s *Service) decline(ctx context.Context, r selfupdate.Release) error {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	_, err := raiseDeclined(ctx, s.cfg.Store, r)
	return err
}

// resolve is v's place in the release order, from its signed manifest
// when this server does not know its build.
func (s *Service) resolve(ctx context.Context, v string) selfupdate.Release {
	r := s.known(v)
	if r.Build != 0 {
		return r
	}
	if m, err := feed(s.cfg.Releases, s.cfg.Checker.BaseURL()).Fetch(ctx, v); err == nil {
		return m.Release()
	}
	return r
}

// known is version's place in the release order as far as this server
// knows it: the running release, or the latest its check found.
func (s *Service) known(v string) selfupdate.Release {
	if running := selfupdate.Running(); running.Version == v {
		return running
	}
	s.checkMu.Lock()
	latest := s.check.latest()
	s.checkMu.Unlock()
	if latest.Version == v {
		return latest
	}
	return selfupdate.Release{Version: v}
}

// checkAndSchedule runs a release check, schedules what it found, then
// fetches the display pointers, each with its own time budget.
func (s *Service) checkAndSchedule(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	st := s.runCheck(checkCtx)
	cancel()
	scheduleCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	s.schedule(scheduleCtx)
	cancel()
	showCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	s.showPointers(showCtx, st, false)
}

// showPointers fetches the display pointers for st's channel outside the
// check slot.
func (s *Service) showPointers(ctx context.Context, st checkState, atStart bool) {
	pointers := s.published(ctx, st.Channel, st.followed, atStart)
	s.checkMu.Lock()
	s.pointers = pointers
	s.checkMu.Unlock()
}

// schedule records the newest release the last check found as the pending
// update, as server.update idle would, when the server should take it by
// itself: this is a stamped release binary, the server has chosen its
// channel and the check followed it, the channel self-applies, the release
// has a build, Offers it, is not declined, its last attempt did not fail,
// the preflight passes, and nothing is pending. The preflight runs first;
// the decision is then taken again under decideMu from the store, so a
// cancel, rollback or channel change since cannot be undone. Tick applies
// it at the first idle moment.
func (s *Service) schedule(ctx context.Context) {
	if !published() || selfupdate.Running().Build == 0 || s.incapableReason() != "" {
		return
	}
	s.checkMu.Lock()
	st := s.check
	s.checkMu.Unlock()
	latest := st.latest()
	if !s.takes(ctx, st, latest) {
		s.scheduled(ctx, latest, nil, false)
		return
	}
	pending := store.PendingServerUpdate{Version: latest.Version, RequestedBy: ServerActor, RequestedAt: s.cfg.Now().UTC()}
	if _, err := preflight(ctx, s.plan(pending), latest.Version, false); err != nil {
		slog.Warn("serverupdate: not scheduling the newest release", "version", latest.Version, "error", err)
		s.scheduled(ctx, latest, fmt.Errorf("not scheduling %s by itself: %w", latest.Version, err), errors.As(err, new(retryable)))
		return
	}
	scheduled, err := s.insert(ctx, st, latest, pending)
	if err != nil {
		slog.Warn("serverupdate: schedule the newest release", "version", latest.Version, "error", err)
		s.scheduled(ctx, latest, fmt.Errorf("not scheduling %s by itself: %w", latest.Version, err), true)
		return
	}
	s.scheduled(ctx, latest, nil, false)
	if scheduled {
		slog.Info("serverupdate: scheduled the newest release on the channel for the next idle moment", "version", latest.Version, "channel", st.Channel)
		s.publish(ctx, ServerActor, events.ServerUpdateScheduled, latest.Version, "")
	}
}

// insert decides again under decideMu and records pending when the server
// still takes latest. The lock is released before the caller records the
// outcome, which may decline the release under the same lock.
func (s *Service) insert(ctx context.Context, st checkState, latest selfupdate.Release, pending store.PendingServerUpdate) (bool, error) {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	if !s.takes(ctx, st, latest) {
		return false, nil
	}
	return s.cfg.Store.SchedulePendingServerUpdate(ctx, pending)
}

// scheduled records why the last self-schedule of latest failed, ""
// when it did not, for server.update_status. Failures of one release are
// counted: one worth retrying moves the next check to the backoff for
// that count rather than the next 6-hourly one, and after maxRetries in a
// row the release is declined, as a failed apply is.
func (s *Service) scheduled(ctx context.Context, latest selfupdate.Release, err error, retry bool) {
	s.checkMu.Lock()
	if err == nil {
		s.scheduleError, s.scheduleFails = "", 0
		s.checkMu.Unlock()
		return
	}
	if s.scheduleFailing != latest.Version {
		s.scheduleFailing, s.scheduleFails = latest.Version, 0
	}
	s.scheduleFails++
	s.scheduleError = err.Error()
	fails := s.scheduleFails
	if next := s.cfg.Now().Add(backoff(fails)); retry && fails < maxRetries && s.check.NextAt.After(next) {
		s.check.NextAt = next
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	s.checkMu.Unlock()
	if fails >= maxRetries {
		if derr := s.decline(ctx, latest); derr != nil {
			slog.Error("serverupdate: a release that keeps failing to schedule stays eligible", "version", latest.Version, "error", derr)
		}
	}
}

// takes reports, from the store, whether the server schedules latest,
// which the check st found, by itself.
func (s *Service) takes(ctx context.Context, st checkState, latest selfupdate.Release) bool {
	stored, err := s.cfg.Store.ReleaseChannel(ctx)
	if err != nil || stored == "" || stored != st.Channel || !selfApplies(stored) || st.Error != "" ||
		latest.Build == 0 || !Offers(stored, selfupdate.Running(), latest) {
		return false
	}
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		slog.Warn("serverupdate: read pending update", "error", err)
		return false
	}
	if state.Pending != nil || refuses(state.Declined, latest) {
		return false
	}
	l := state.Last
	return l == nil || l.Version != latest.Version || l.Outcome == store.ServerUpdateApplied
}

// stillPending reports, under decideMu, that pending has not been
// cancelled or replaced since it was read.
func (s *Service) stillPending(ctx context.Context, pending store.PendingServerUpdate) bool {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	return s.stillPendingLocked(ctx, pending)
}

// takePending clears pending as the update applies, under decideMu, only
// while it is still the pending update: one cancelled or replaced during
// the download is not applied. An update applied now clears whatever is
// pending, as it always has.
func (s *Service) takePending(ctx context.Context, pending store.PendingServerUpdate, scheduled bool) bool {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	if scheduled && !s.stillPendingLocked(ctx, pending) {
		return false
	}
	s.setCommitted()
	if err := s.cfg.Store.SetPendingServerUpdate(ctx, nil); err != nil {
		slog.Warn("serverupdate: clear the applied pending update", "error", err)
	}
	return true
}

func (s *Service) stillPendingLocked(ctx context.Context, pending store.PendingServerUpdate) bool {
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	return err == nil && state.Pending != nil && state.Pending.Version == pending.Version &&
		state.Pending.RequestedBy == pending.RequestedBy && state.Pending.RequestedAt.Equal(pending.RequestedAt)
}

// stillTaken refuses, at its idle moment, a pending update whose release m
// the server's channel no longer takes, or, for one the server scheduled
// itself, that the declined mark now refuses. Only an update applied now
// installs such a release.
func (s *Service) stillTaken(ctx context.Context, pending store.PendingServerUpdate, m selfupdate.Manifest) error {
	if s.cfg.Releases != nil {
		return nil
	}
	if pending.RequestedBy == ServerActor {
		state, err := s.cfg.Store.GetServerUpdate(ctx)
		if err != nil {
			return err
		}
		if refuses(state.Declined, m.Release()) {
			return fmt.Errorf("%w: %w: %s was declined", ErrPreflight, errNotTaken, m.Version)
		}
	}
	return s.takenNow(ctx, m)
}
