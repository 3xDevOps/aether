package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// ServerActor is the actor of an update the server scheduled itself; no
// member ID has this form.
const ServerActor domain.MemberID = "server"

// selfApplies reports whether a server following channel schedules the
// newer builds its release check finds by itself. Every channel does, an
// assumption pending the owner; for nightly only it would return
// channel == selfupdate.ChannelNightly. A server on a fixed feed
// (Config.Releases, channel "") never does.
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

// refuses reports whether d declines r: r is d's release or not above
// d's build. Build 0 declines by version alone.
func refuses(d *store.DeclinedServerUpdate, r selfupdate.Release) bool {
	if d == nil {
		return false
	}
	if r.Version == d.Version {
		return true
	}
	return d.Build > 0 && (r.Build < d.Build || r.Build == d.Build && r.Hotfix <= d.Hotfix)
}

func wireDeclined(d *store.DeclinedServerUpdate) *protocol.ReleaseBuild {
	if d == nil {
		return nil
	}
	return &protocol.ReleaseBuild{Version: d.Version, Build: d.Build, Hotfix: d.Hotfix}
}

// mergeDeclined is d raised to old's build, so the mark only rises. A
// mark whose build was not known keeps its version beside a known build,
// which then covers d's release by build.
func mergeDeclined(old *store.DeclinedServerUpdate, d store.DeclinedServerUpdate) store.DeclinedServerUpdate {
	if old == nil {
		return d
	}
	if old.Build == 0 && d.Build > 0 {
		d.Version = old.Version
	}
	if old.Build > d.Build || old.Build == d.Build && old.Hotfix > d.Hotfix {
		d.Build, d.Hotfix = old.Build, old.Hotfix
	}
	return d
}

// raiseDeclined records r in st as declined, keeping the highest build
// declined so far, so the server schedules by itself only a build above
// it. A super admin's own server.update ignores it.
func raiseDeclined(ctx context.Context, st store.Store, r selfupdate.Release) error {
	state, err := st.GetServerUpdate(ctx)
	if err != nil {
		return err
	}
	d := mergeDeclined(state.Declined, store.DeclinedServerUpdate{Version: r.Version, Build: r.Build, Hotfix: r.Hotfix})
	if err := st.SetDeclinedServerUpdate(ctx, d); err != nil {
		return fmt.Errorf("serverupdate: record %s as declined: %w", r.Version, err)
	}
	return nil
}

// decline is raiseDeclined under decideMu.
func (s *Service) decline(ctx context.Context, r selfupdate.Release) error {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	return raiseDeclined(ctx, s.cfg.Store, r)
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

// checkAndSchedule runs a release check, records the display pointers
// and schedules what it found.
func (s *Service) checkAndSchedule(ctx context.Context) {
	s.showPointers(ctx, s.runCheck(ctx))
	s.schedule(ctx)
}

// showPointers fetches the display pointers for st's channel outside the
// check slot.
func (s *Service) showPointers(ctx context.Context, st checkState) {
	pointers := s.published(ctx, st.Channel, st.followed)
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
		return
	}
	pending := store.PendingServerUpdate{Version: latest.Version, RequestedBy: ServerActor, RequestedAt: s.cfg.Now().UTC()}
	if _, err := preflight(ctx, s.plan(pending), latest.Version, false); err != nil {
		slog.Warn("serverupdate: not scheduling the newest release", "version", latest.Version, "error", err)
		return
	}
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	if !s.takes(ctx, st, latest) {
		return
	}
	scheduled, err := s.cfg.Store.SchedulePendingServerUpdate(ctx, pending)
	if err != nil {
		slog.Warn("serverupdate: schedule the newest release", "version", latest.Version, "error", err)
		return
	}
	if scheduled {
		slog.Info("serverupdate: scheduled the newest release on the channel for the next idle moment", "version", latest.Version, "channel", st.Channel)
		s.publish(ctx, ServerActor, events.ServerUpdateScheduled, latest.Version, "")
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
