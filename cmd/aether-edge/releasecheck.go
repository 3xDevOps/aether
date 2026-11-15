package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

const releaseCheckUsage = "usage: aether-edge release-check --tag <tag> --digest sha256:<digest> --from <tag> --from-digest sha256:<digest> --channel <nightly|beta|stable|latest> [--json]"

// channelLatest follows the image GitHub's Latest release publishes, with
// no promotion attestation. Latest is never a nightly, so it refuses one:
// a nightly key alone must not move a host.
const channelLatest = "latest"

// holdError is a release the edge must not move to until an operator acts.
type holdError struct{ reasons []string }

func (e *holdError) Error() string { return "hold: " + strings.Join(e.reasons, "; ") }

// releaseReport is what release-check prints, and with --json its schema.
type releaseReport struct {
	Version string          `json:"version"`
	Commit  string          `json:"commit"`
	Edge    selfupdate.Edge `json:"edge"`
	// ApplicableSteps are the manual steps an upgrade from --from to the
	// release crosses.
	ApplicableSteps []selfupdate.ManualStep `json:"applicable_steps"`
	Hold            bool                    `json:"hold"`
	Reasons         []string                `json:"reasons"`
}

// releaseCheck verifies tag's signed manifest names digest as its edge
// image, refuses it when the host's channel does not take it or it is not
// newer than --from by build order, and reports whether the upgrade to it
// must hold. The running release's build comes from --from's signed
// manifest, which must name --from-digest, never from what an image
// claims. A --tag equal to --from checks the running release itself, which
// needs neither refusal.
func releaseCheck(args []string, fetch func(context.Context, string) (selfupdate.Manifest, error), out io.Writer) error {
	fs := flag.NewFlagSet("aether-edge release-check", flag.ContinueOnError)
	tag := fs.String("tag", "", "release tag to check")
	digest := fs.String("digest", "", "index digest of the edge image to run")
	from := fs.String("from", "", "release tag the edge runs now (required); whether that release can open the database\n"+
		"after the upgrade migrates it is not checked here: run aether-edge schema --json with its image")
	fromDigest := fs.String("from-digest", "", "index digest of the edge image the edge runs now (required)")
	channel := fs.String("channel", "", "release channel the host follows: nightly, beta, stable or latest (required)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tag == "" || *digest == "" || *from == "" || *fromDigest == "" || *channel == "" || fs.NArg() != 0 {
		return errors.New(releaseCheckUsage)
	}
	if !selfupdate.ValidTag(*from) {
		return fmt.Errorf("--from %q is not a release tag such as v0.7.0", *from)
	}
	switch *channel {
	case selfupdate.ChannelNightly, selfupdate.ChannelBeta, selfupdate.ChannelStable, channelLatest:
	default:
		return fmt.Errorf("--channel %q is not nightly, beta, stable or latest", *channel)
	}
	m, err := signedEdge(fetch, *tag, *digest)
	if err != nil {
		return err
	}
	if *tag != *from {
		var running selfupdate.Manifest
		if running, err = signedEdge(fetch, *from, *fromDigest); err != nil {
			return fmt.Errorf("the running release: %w", err)
		}
		if self := selfupdate.Running(); self.Version == *from && (self.Build != running.Build || self.Hotfix != running.Hotfix) {
			return fmt.Errorf("this binary is %s build %d hotfix %d, but its signed %s says build %d hotfix %d; do not trust it to order releases",
				*from, self.Build, self.Hotfix, selfupdate.ManifestName, running.Build, running.Hotfix)
		}
		switch {
		case *channel == channelLatest && m.Channel == selfupdate.ChannelNightly:
			return fmt.Errorf("refusing to upgrade: %s is a nightly release, which an install on latest does not take", *tag)
		case *channel != channelLatest:
			if err = m.AcceptedOn(*channel); err != nil {
				return fmt.Errorf("refusing to upgrade: %w", err)
			}
		}
		if err = selfupdate.Downgrade(running.Release(), m.Release()); err != nil {
			return fmt.Errorf("refusing to upgrade: %w", err)
		}
	}
	r := releaseReport{Version: m.Version, Commit: m.Commit, Edge: *m.Edge,
		ApplicableSteps: []selfupdate.ManualStep{}, Reasons: []string{}}
	if selfupdate.Behind(*from, m.Edge.MinUpgradeFrom) {
		r.Reasons = append(r.Reasons, fmt.Sprintf("%s upgrades only from %s or later; upgrade %s to %s first",
			*tag, m.Edge.MinUpgradeFrom, *from, m.Edge.MinUpgradeFrom))
	}
	for _, s := range m.Edge.ManualSteps {
		if !selfupdate.Behind(*from, s.Since) {
			continue
		}
		r.ApplicableSteps = append(r.ApplicableSteps, s)
		if s.Blocking {
			r.Reasons = append(r.Reasons, fmt.Sprintf("manual step %s (since %s) must be done first: %s; see %s",
				s.ID, s.Since, s.Summary, s.DocURL))
		}
	}
	r.Hold = len(r.Reasons) > 0
	if *asJSON {
		err = json.NewEncoder(out).Encode(r)
	} else {
		err = writeReleaseReport(out, r, *from)
	}
	if err != nil {
		return err
	}
	if r.Hold {
		return &holdError{reasons: r.Reasons}
	}
	return nil
}

func writeReleaseReport(out io.Writer, r releaseReport, from string) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	e := r.Edge
	_, _ = fmt.Fprintf(tw, "release\t%s\ncommit\t%s\nimage\t%s@%s\n", r.Version, r.Commit, e.Image, e.ImageDigest)
	_, _ = fmt.Fprintf(tw, "store schema\t%d, compat floor %d\n", e.StoreSchema, e.StoreCompatFloor)
	_, _ = fmt.Fprintf(tw, "protocol\t%d to %d\n", e.Protocol.MinVersion, e.Protocol.Version)
	_, _ = fmt.Fprintf(tw, "upgrades from\t%s or later\n", e.MinUpgradeFrom)
	steps, label := r.ApplicableSteps, "manual steps from "+from
	if len(steps) == 0 {
		_, _ = fmt.Fprintf(tw, "%s\tnone\n", label)
	}
	for _, s := range steps {
		kind := "optional"
		if s.Blocking {
			kind = "blocking"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s %s (since %s): %s %s\n", label, kind, s.ID, s.Since, s.Summary, s.DocURL)
	}
	return tw.Flush()
}

// signedEdge fetches tag's signed manifest and checks its edge section
// names digest as the edge image.
func signedEdge(fetch func(context.Context, string) (selfupdate.Manifest, error), tag, digest string) (selfupdate.Manifest, error) {
	m, err := fetch(context.Background(), tag)
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	if m.Edge == nil {
		return selfupdate.Manifest{}, fmt.Errorf("release %s has no edge section in its signed %s; it predates signed edge releases", tag, selfupdate.ManifestName)
	}
	if err = m.Edge.Validate(tag); err != nil {
		return selfupdate.Manifest{}, fmt.Errorf("release %s: %w", tag, err)
	}
	if m.Edge.ImageDigest != digest {
		return selfupdate.Manifest{}, fmt.Errorf("release %s signs the edge image %s, not %s; do not run it", tag, m.Edge.ImageDigest, digest)
	}
	return m, nil
}
