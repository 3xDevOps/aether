package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// ErrBadChannel is returned for a release channel that is not one of the
// three.
var ErrBadChannel = errors.New(`channel must be "stable", "beta" or "nightly"`)

// Offers reports whether a server on channel running takes target as its
// update: a higher (build, hotfix), or the same code under another label
// when channel does not take the running one's, as when a server that left
// nightly reaches the channel it moved to. A stable label on beta, say, is
// kept. It never offers a lower build. On a fixed feed (channel "") any
// other label is taken.
func Offers(channel string, running, target selfupdate.Release) bool {
	if selfupdate.Newer(target, running) {
		return true
	}
	if !selfupdate.SameCode(running, target) || running.Channel == target.Channel {
		return false
	}
	return channel == "" || selfupdate.Manifest{Version: running.Version, Channel: running.Channel}.AcceptedOn(channel) != nil
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

// stableMissingChecks is how many checks in a row must find no
// stable.json before a new server settles on beta, so one 404 while the
// asset is replaced does not pin it there.
const stableMissingChecks = 2

// chooseChannel is the channel a check follows. A server that has not
// chosen one reads the pointers: stable once stable.json is published and
// verifies, beta once stable.json itself has answered 404 at stableMissingChecks
// checks in a row. Until then, and on any other answer, such as no
// network or a pointer that does not verify, it follows beta and asks
// again at the next check. The caller holds run.
func (s *Service) chooseChannel(ctx context.Context) (string, error) {
	channel, chosen, err := s.storedChannel(ctx)
	if err != nil || chosen {
		return channel, err
	}
	_, perr := selfupdate.FetchPointer(ctx, s.cfg.Checker.BaseURL(), selfupdate.ChannelStable)
	switch {
	case perr == nil:
		channel = selfupdate.ChannelStable
	case selfupdate.PointerMissing(perr):
		if s.stableMissing++; s.stableMissing < stableMissingChecks {
			return selfupdate.ChannelBeta, nil
		}
		channel = selfupdate.ChannelBeta
	default:
		s.stableMissing = 0
		slog.Warn("serverupdate: cannot choose a release channel yet; following beta until a check can", "error", perr)
		return selfupdate.ChannelBeta, nil
	}
	return s.cfg.Store.ChooseReleaseChannel(ctx, channel)
}

func ReadMarks(stateDir string) (selfupdate.Marks, error) {
	st, err := loadCheck(stateDir)
	return st.Pointers, err
}

// describeChannel fills the channel and build fields of a status from the
// last check. A check on another channel than the one now set, as after a
// change on the host, says nothing about it, so it is left out and a
// check is started, at most once per refreshEvery; before the first check
// the background loop is already about to run one.
func (s *Service) describeChannel(ctx context.Context, out *protocol.ServerUpdateStatusResult, check checkState) error {
	channel, chosen, err := s.storedChannel(ctx)
	if err != nil {
		return err
	}
	running := selfupdate.Running()
	out.ServerBuild, out.ServerHotfix = running.Build, running.Hotfix
	s.checkMu.Lock()
	pointers := s.pointers
	s.checkMu.Unlock()
	for ch, p := range pointers {
		if out.ChannelPointers == nil {
			out.ChannelPointers = map[string]protocol.ChannelPointer{}
		}
		out.ChannelPointers[ch] = protocol.ChannelPointer{Version: p.Version, Build: p.Build, Hotfix: p.Hotfix, PublishedAt: p.PublishedAt.UTC().Format(time.RFC3339)}
	}
	if s.cfg.Releases == nil {
		out.Channel, out.ChannelPending = channel, !chosen
		if check.Channel != channel {
			if !check.CheckedAt.IsZero() && s.cfg.Now().Sub(check.CheckedAt) >= refreshEvery {
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
		out.UpdateAvailable = Offers(check.Channel, running, latest)
		out.ChannelWait = Waits(running, latest)
	}
	return nil
}

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
// to a channel below its build stays on it, and a pending update the new
// channel does not take is retired (Retired; a dry run names the one it
// would retire).
func (s *Service) SetChannel(ctx context.Context, actor domain.MemberID, p protocol.ServerReleaseChannelSetParams) (protocol.ServerReleaseChannelSetResult, error) {
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
	pending, reason, kept, err := s.untaken(ctx, p.Channel)
	if err != nil {
		return protocol.ServerReleaseChannelSetResult{}, err
	}
	out.Kept, out.KeptError = kept.Version, kept.Error
	if pending != nil {
		out.Retired = pending.Version
	}
	if !p.DryRun {
		retired, err := s.saveChannel(ctx, p.Channel, pending, reason)
		if err != nil {
			return protocol.ServerReleaseChannelSetResult{}, err
		}
		out.Saved = true
		if retired {
			s.publish(ctx, actor, events.ServerUpdateCancelled, pending.Version, retiredDetail(p.Channel, reason))
		} else {
			out.Retired = ""
		}
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
		target, terr = s.checkNow(ctx)
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

// saveChannel saves channel and retires pending, if it is still the
// pending update, under decideMu, reporting whether it retired it.
func (s *Service) saveChannel(ctx context.Context, channel string, pending *store.PendingServerUpdate, reason string) (bool, error) {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	if err := s.cfg.Store.SetReleaseChannel(ctx, channel); err != nil || pending == nil {
		return false, err
	}
	return s.cfg.Store.RetirePendingServerUpdate(ctx, *pending, store.ServerUpdateAttempt{
		Version: pending.Version, Outcome: store.ServerUpdateFailed, Detail: retiredDetail(channel, reason), At: s.cfg.Now().UTC(),
	})
}

func retiredDetail(channel, reason string) string {
	return fmt.Sprintf("the server moved to the %s channel, which does not take it: %s", channel, reason)
}

// untaken is the pending update channel does not take, and why: its
// signed manifest's channel is not one channel accepts. Nightly takes
// every release. A manifest that cannot be read keeps the pending update,
// reported as kept; Tick refuses it at its idle moment if the channel
// does not take it.
func (s *Service) untaken(ctx context.Context, channel string) (*store.PendingServerUpdate, string, keptUpdate, error) {
	state, err := s.cfg.Store.GetServerUpdate(ctx)
	if err != nil || state.Pending == nil || channel == selfupdate.ChannelNightly {
		return nil, "", keptUpdate{}, err
	}
	m, err := feed(s.cfg.Releases, s.cfg.Checker.BaseURL()).Fetch(ctx, state.Pending.Version)
	if err != nil {
		slog.Warn("serverupdate: keeping the pending update across a channel change", "version", state.Pending.Version, "error", err)
		return nil, "", keptUpdate{Version: state.Pending.Version, Error: err.Error()}, nil
	}
	if err := m.AcceptedOn(channel); err != nil {
		return state.Pending, err.Error(), keptUpdate{}, nil
	}
	return nil, "", keptUpdate{}, nil
}

// keptUpdate is a pending update a channel change kept because its
// manifest could not be fetched to check it, and the fetch error.
type keptUpdate struct{ Version, Error string }

// checkNow runs a check for a request, waiting for a running one no
// longer than ctx, and schedules what it found.
func (s *Service) checkNow(ctx context.Context) (selfupdate.Release, error) {
	select {
	case s.run <- struct{}{}:
	case <-ctx.Done():
		return selfupdate.Release{}, ctx.Err()
	}
	st := s.checkLocked(ctx)
	<-s.run
	s.schedule(ctx)
	s.showPointers(ctx, st, false)
	if st.Error != "" {
		return selfupdate.Release{}, errors.New(st.Error)
	}
	return st.latest(), nil
}

// takenNow refuses m for an update applied at an idle moment when the
// channel the server follows now does not accept m, as after a channel
// change on the host. Only an update applied now installs such a
// release.
func (s *Service) takenNow(ctx context.Context, m selfupdate.Manifest) error {
	if s.cfg.Releases != nil {
		return nil
	}
	channel, _, err := s.storedChannel(ctx)
	if err != nil {
		return err
	}
	if err := m.AcceptedOn(channel); err != nil {
		return fmt.Errorf("%w: %w: %w; update with --when now to install it anyway", ErrPreflight, errNotTaken, err)
	}
	return nil
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
