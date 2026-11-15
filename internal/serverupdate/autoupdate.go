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

// hold takes the check slot, which also serializes what the server
// schedules by itself with the cancels and rollbacks that decline it.
func (s *Service) hold(ctx context.Context) (func(), error) {
	select {
	case s.run <- struct{}{}:
		return func() { <-s.run }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// declineLocked records r in the store as declined, keeping the highest
// build declined so far, so the server schedules by itself only a build
// above it. A super admin's own server.update ignores it. The caller
// holds the slot.
func (s *Service) declineLocked(ctx context.Context, r selfupdate.Release) error {
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		return err
	}
	d := store.DeclinedServerUpdate{Version: r.Version, Build: r.Build, Hotfix: r.Hotfix}
	if old := state.Declined; old != nil && (old.Build > d.Build || old.Build == d.Build && old.Hotfix > d.Hotfix) {
		d.Build, d.Hotfix = old.Build, old.Hotfix
	}
	if err := s.cfg.Store.SetDeclinedServerUpdate(ctx, d); err != nil {
		return fmt.Errorf("serverupdate: record %s as declined: %w", r.Version, err)
	}
	return nil
}

// decline is declineLocked taking the slot.
func (s *Service) decline(ctx context.Context, r selfupdate.Release) error {
	release, err := s.hold(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.declineLocked(ctx, r)
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

// checkAndSchedule runs a release check and schedules what it found.
func (s *Service) checkAndSchedule(ctx context.Context) {
	s.runCheck(ctx)
	s.schedule(ctx)
}

// schedule records the newest release the last check found as the pending
// update, as server.update idle would, when the server should take it by
// itself: this binary is a published release, the server has chosen its
// channel and the check followed it, the channel self-applies, the release
// has a build, Offers it, is not declined, its last attempt did not fail,
// the preflight passes, and nothing is pending. It holds the slot
// throughout, so a cancel or rollback cannot slip between its reads and
// its write. Tick applies it at the first idle moment.
func (s *Service) schedule(ctx context.Context) {
	if !published() || s.incapableReason() != "" {
		return
	}
	release, err := s.hold(ctx)
	if err != nil {
		return
	}
	defer release()
	st := s.check
	latest := st.latest()
	stored, err := s.cfg.Store.ReleaseChannel(ctx)
	if err != nil || stored == "" || stored != st.Channel || !selfApplies(stored) || st.Error != "" ||
		latest.Build == 0 || !Offers(stored, selfupdate.Running(), latest) {
		return
	}
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		slog.Warn("serverupdate: read pending update", "error", err)
		return
	}
	if state.Pending != nil || refuses(state.Declined, latest) {
		return
	}
	if l := state.Last; l != nil && l.Version == latest.Version && l.Outcome != store.ServerUpdateApplied {
		return
	}
	pending := store.PendingServerUpdate{Version: latest.Version, RequestedBy: ServerActor, RequestedAt: s.cfg.Now().UTC()}
	if _, err = preflight(ctx, s.plan(pending), latest.Version, false); err != nil {
		slog.Warn("serverupdate: not scheduling the newest release", "version", latest.Version, "error", err)
		return
	}
	scheduled, err := s.cfg.Store.SchedulePendingServerUpdate(ctx, pending)
	if err != nil {
		slog.Warn("serverupdate: schedule the newest release", "version", latest.Version, "error", err)
		return
	}
	if scheduled {
		slog.Info("serverupdate: scheduled the newest release on the channel for the next idle moment", "version", latest.Version, "channel", stored)
		s.publish(ctx, ServerActor, events.ServerUpdateScheduled, latest.Version, "")
	}
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
