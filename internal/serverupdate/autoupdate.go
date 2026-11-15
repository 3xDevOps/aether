package serverupdate

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
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

// declined is the build the server no longer schedules by itself. Build 0
// is a release whose build was not known, refused by its version alone.
type declined struct {
	Version string `json:"version"`
	Build   int    `json:"build,omitempty"`
	Hotfix  int    `json:"hotfix,omitempty"`
}

// refuses reports whether r is d's release or not above d's build.
func (d *declined) refuses(r selfupdate.Release) bool {
	if d == nil {
		return false
	}
	if r.Version == d.Version {
		return true
	}
	return d.Build > 0 && (r.Build < d.Build || r.Build == d.Build && r.Hotfix <= d.Hotfix)
}

func (d *declined) wire() *protocol.ReleaseBuild {
	if d == nil {
		return nil
	}
	return &protocol.ReleaseBuild{Version: d.Version, Build: d.Build, Hotfix: d.Hotfix}
}

// decline records r in check.json as declined, keeping the highest build
// declined so far, so the server schedules by itself only a build above
// it. A super admin's own server.update ignores it.
func (s *Service) decline(ctx context.Context, r selfupdate.Release) {
	select {
	case s.run <- struct{}{}:
	case <-ctx.Done():
		slog.Warn("serverupdate: record a declined build", "version", r.Version, "error", ctx.Err())
		return
	}
	defer func() { <-s.run }()
	s.reloadState()
	st := s.check
	d := &declined{Version: r.Version, Build: r.Build, Hotfix: r.Hotfix}
	if old := st.Declined; old != nil && (old.Build > d.Build || old.Build == d.Build && old.Hotfix > d.Hotfix) {
		d.Build, d.Hotfix = old.Build, old.Hotfix
	}
	st.Declined = d
	if err := s.persistCheck(st); err != nil {
		slog.Warn("serverupdate: record a declined build", "version", r.Version, "error", err)
		return
	}
	s.checkMu.Lock()
	s.check = st
	s.checkMu.Unlock()
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
	s.schedule(ctx, s.runCheck(ctx))
}

// schedule records the newest release a check found as the pending update,
// as server.update idle would, when the server should take it by itself:
// its channel self-applies, the release has a build, Offers it, it is not
// declined, its last attempt did not fail, the preflight passes, and
// nothing is pending. Tick applies it at the first idle moment. A release
// from before channels (build 0) is never scheduled by the server.
func (s *Service) schedule(ctx context.Context, st checkState) {
	latest := st.latest()
	if !selfApplies(st.Channel) || st.Error != "" || latest.Build == 0 ||
		!Offers(st.Channel, selfupdate.Running(), latest) || st.Declined.refuses(latest) || s.incapableReason() != "" {
		return
	}
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil {
		slog.Warn("serverupdate: read pending update", "error", err)
		return
	}
	if state.Pending != nil {
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
		slog.Info("serverupdate: scheduled the newest release on the channel for the next idle moment", "version", latest.Version, "channel", st.Channel)
		s.publish(ctx, ServerActor, events.ServerUpdateScheduled, latest.Version, "")
	}
}
