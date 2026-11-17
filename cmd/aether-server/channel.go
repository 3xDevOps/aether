package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/serverupdate"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// followChannel finds the newest release on the server's release channel,
// or on channel when it is set, through its signed pointer and the marks
// the server keeps in check.json. It only reads: a channel the server has
// not chosen yet is followed as beta, and the server chooses it and raises
// its own marks at its next check.
func followChannel(ctx context.Context, baseURL, stateDir, database, channel string) (string, selfupdate.Manifest, error) {
	if channel == "" {
		f, err := store.OpenChannelFile(database)
		if err != nil {
			return "", selfupdate.Manifest{}, err
		}
		channel, err = f.ReleaseChannel(ctx)
		_ = f.Close()
		if err != nil {
			return "", selfupdate.Manifest{}, err
		}
		if channel == "" {
			channel = selfupdate.ChannelBeta
		}
	}
	marks, err := serverupdate.ReadMarks(stateDir)
	if err != nil {
		return "", selfupdate.Manifest{}, fmt.Errorf("%w; fix it, or remove it, which forgets the channel marks", err)
	}
	m, _, err := selfupdate.FollowChannel(ctx, baseURL, channel, marks)
	return channel, m, err
}

// setChannel prints what moving the server to channel means, as the
// dashboard's confirmation does, and with yes saves it under the update
// lock. It installs nothing: the server checks the new channel at its
// next release check.
func setChannel(ctx context.Context, w io.Writer, baseURL, stateDir, database, channel string, yes bool) error {
	switch channel {
	case selfupdate.ChannelStable, selfupdate.ChannelBeta, selfupdate.ChannelNightly:
	default:
		return fmt.Errorf("%w (got %q)", serverupdate.ErrBadChannel, channel)
	}
	f, err := store.OpenChannelFile(database)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // the write is committed before Close
	current, err := f.ReleaseChannel(ctx)
	if err != nil {
		return err
	}
	if current == channel {
		_, _ = fmt.Fprintf(w, "aether-server already follows %s\n", channel)
		return nil
	}
	_, m, ferr := followChannel(ctx, baseURL, stateDir, database, channel)
	if errors.Is(ferr, selfupdate.ErrNoRelease) {
		ferr = nil
	}
	_, _ = fmt.Fprintln(w, channelConfirmation(channel, selfupdate.Running(), m.Release(), ferr))
	if !yes {
		return fmt.Errorf("nothing changed; run it again with --yes to switch to %s", channel)
	}
	unlock, err := serverupdate.Lock(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := f.SetReleaseChannel(ctx, channel); err != nil {
		return err
	}
	was := current
	if was == "" {
		was = "not chosen, beta meanwhile"
	}
	_, _ = fmt.Fprintf(w, "aether-server follows %s (was %s); it checks %s at its next release check, and `sudo aether-server update` installs what it finds\n",
		channel, was, channel)
	return nil
}

func channelConfirmation(channel string, running, target selfupdate.Release, targetErr error) string {
	name := strings.ToUpper(channel[:1]) + channel[1:]
	if channel == selfupdate.ChannelNightly {
		return `Switch to nightly builds?

Not recommended unless you are developing Cloudfield.

- Nightly builds come from Cloudfield's main branch. Breaking changes
  arrive as soon as they merge.
- A nightly build may contain security issues that a later build fixes.
- You cannot go back right away. This server stays on its nightly build
  until the channel you return to catches up with it.`
	}
	head := fmt.Sprintf("Switch to %s?\n\nThis server runs %s.", channel, buildLabel(running))
	switch {
	case targetErr != nil:
		return fmt.Sprintf("%s %s cannot be read: %v. The server never installs an older build.", head, name, targetErr)
	case target.Version == "":
		return fmt.Sprintf("%s %s has no release yet. The server never installs an older build.", head, name)
	case serverupdate.Waits(running, target):
		return fmt.Sprintf("%s %s is at %s. The server stays on its current build until %s reaches build %s, then updates as usual. It never installs an older build.",
			head, name, buildLabel(target), channel, buildNumber(running))
	}
	return fmt.Sprintf("%s %s is at %s.", head, name, buildLabel(target))
}

func buildLabel(r selfupdate.Release) string {
	if r.Build == 0 {
		return r.Version
	}
	return fmt.Sprintf("%s (build %s)", r.Version, buildNumber(r))
}

func buildNumber(r selfupdate.Release) string {
	if r.Hotfix == 0 {
		return fmt.Sprint(r.Build)
	}
	return fmt.Sprintf("%d hotfix %d", r.Build, r.Hotfix)
}
