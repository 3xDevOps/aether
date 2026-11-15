package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// ErrBadChannel is returned for a release channel that is not one of the
// three.
var ErrBadChannel = errors.New(`channel must be "stable", "beta" or "nightly"`)

// Offers reports whether a server on running takes target as its update:
// a higher (build, hotfix), or the same code under another channel's
// label, as when a server that left nightly reaches the channel it moved
// to. It never offers a lower build.
func Offers(running, target selfupdate.Release) bool {
	return selfupdate.Newer(target, running) || selfupdate.SameCode(running, target) && running.Channel != target.Channel
}

// Waits reports whether a server on running stays on its build because
// the newest release on its channel, target, is below it.
func Waits(running, target selfupdate.Release) bool {
	return selfupdate.Newer(running, target)
}

// storedChannel is the channel the server follows and whether it has
// chosen it. Until it chooses, it follows beta, which takes the stable
// releases and, while there are none, the releases from before channels.
func (s *Service) storedChannel(ctx context.Context) (string, bool, error) {
	channel, err := s.cfg.Store.ReleaseChannel(ctx)
	if err != nil || channel != "" {
		return channel, true, err
	}
	return selfupdate.ChannelBeta, false, nil
}

// ChannelStore holds the server's release channel: the store, or the
// database file on the host.
type ChannelStore interface {
	ReleaseChannel(ctx context.Context) (string, error)
	ChooseReleaseChannel(ctx context.Context, channel string) (string, error)
}

// ChooseChannel is the release channel a server with st follows, as the
// repository at baseURL decides it for a server that has not chosen one:
// stable once stable.json is published and verifies, beta while
// stable.json is not published. Any other answer, such as no network or
// a pointer that does not verify, leaves the choice to the next check
// and follows beta meanwhile.
func ChooseChannel(ctx context.Context, baseURL string, st ChannelStore) (string, error) {
	channel, err := st.ReleaseChannel(ctx)
	if err != nil || channel != "" {
		return channel, err
	}
	_, perr := selfupdate.FetchPointer(ctx, baseURL, selfupdate.ChannelStable)
	switch {
	case perr == nil:
		channel = selfupdate.ChannelStable
	case selfupdate.NotFound(perr):
		channel = selfupdate.ChannelBeta
	default:
		slog.Warn("serverupdate: cannot choose a release channel yet; following beta until a check can", "error", perr)
		return selfupdate.ChannelBeta, nil
	}
	return st.ChooseReleaseChannel(ctx, channel)
}

func (s *Service) chooseChannel(ctx context.Context) (string, error) {
	return ChooseChannel(ctx, s.cfg.Checker.BaseURL(), s.cfg.Store)
}

// ReadMarks reads the channel pointers' high-water marks the server keeps
// in check.json under stateDir.
func ReadMarks(stateDir string) (selfupdate.Marks, error) {
	st, err := loadCheck(stateDir)
	return st.Pointers, err
}

// describeChannel fills the channel and build fields of a status from the
// last check. A check on another channel than the one now set, as after a
// change on the host, says nothing about it, so it is left out and a
// check is started, at most once per refreshEvery.
func (s *Service) describeChannel(ctx context.Context, out *protocol.ServerUpdateStatusResult, check checkState) error {
	channel, chosen, err := s.storedChannel(ctx)
	if err != nil {
		return err
	}
	running := selfupdate.Running()
	out.ServerBuild, out.ServerHotfix = running.Build, running.Hotfix
	if s.cfg.Releases == nil {
		out.Channel, out.ChannelPending = channel, !chosen
		if check.Channel != channel {
			if s.cfg.Now().Sub(check.CheckedAt) >= refreshEvery {
				s.checkSoon()
			}
			return nil
		}
	}
	out.CheckError = check.Error
	latest := check.latest()
	out.LatestVersion, out.Latest, out.NotesURL = latest.Version, latest.Version, check.NotesURL
	out.LatestChannel, out.LatestBuild, out.LatestHotfix = latest.Channel, latest.Build, latest.Hotfix
	if latest.Version != "" {
		out.UpdateAvailable = Offers(running, latest)
		out.ChannelWait = Waits(running, latest)
	}
	return nil
}

// checkSoon moves the background check to now.
func (s *Service) checkSoon() {
	s.checkMu.Lock()
	s.check.NextAt = s.cfg.Now()
	s.checkMu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// SetChannel handles server.release_channel_set: it reports the running
// release, the newest release on the channel asked for and whether the
// server would wait for it, then, unless DryRun, saves the channel and
// checks it at once. Saving never installs anything: a server that moves
// to a channel below its build stays on it.
func (s *Service) SetChannel(ctx context.Context, p protocol.ServerReleaseChannelSetParams) (protocol.ServerReleaseChannelSetResult, error) {
	switch p.Channel {
	case selfupdate.ChannelStable, selfupdate.ChannelBeta, selfupdate.ChannelNightly:
	default:
		return protocol.ServerReleaseChannelSetResult{}, fmt.Errorf("%w (got %q)", ErrBadChannel, p.Channel)
	}
	previous, _, err := s.storedChannel(ctx)
	if err != nil {
		return protocol.ServerReleaseChannelSetResult{}, err
	}
	running := selfupdate.Running()
	out := protocol.ServerReleaseChannelSetResult{Channel: p.Channel, Previous: previous, Running: wireRelease(running)}
	if !p.DryRun {
		if err := s.cfg.Store.SetReleaseChannel(ctx, p.Channel); err != nil {
			return protocol.ServerReleaseChannelSetResult{}, err
		}
		out.Saved = true
	}
	if !s.checks() {
		out.TargetError = "this server does not check for releases (" + selfupdate.OptOutEnv + " is set, or it is a build from source)"
		return out, nil
	}
	var (
		target selfupdate.Release
		terr   error
	)
	if p.DryRun {
		target, terr = s.peek(ctx, p.Channel)
	} else {
		st := s.runCheck(ctx)
		s.checkSoon()
		target = st.latest()
		if st.Error != "" {
			terr = errors.New(st.Error)
		}
	}
	switch {
	case terr != nil:
		out.TargetError = terr.Error()
	case target.Version != "":
		t := wireRelease(target)
		out.Target = &t
		out.Wait = Waits(running, target)
	}
	return out, nil
}

// peek finds the newest release on channel against the marks recorded,
// without recording anything.
func (s *Service) peek(ctx context.Context, channel string) (selfupdate.Release, error) {
	if s.cfg.Releases != nil {
		m, err := s.cfg.Releases.Latest(ctx)
		return m.Release(), err
	}
	s.checkMu.Lock()
	marks := maps.Clone(s.check.Pointers)
	s.checkMu.Unlock()
	m, _, err := selfupdate.FollowChannel(ctx, s.cfg.Checker.BaseURL(), channel, marks)
	if errors.Is(err, selfupdate.ErrNoRelease) {
		return selfupdate.Release{}, nil
	}
	return m.Release(), err
}

func wireRelease(r selfupdate.Release) protocol.ReleaseBuild {
	return protocol.ReleaseBuild{Version: r.Version, Channel: r.Channel, Build: r.Build, Hotfix: r.Hotfix}
}
