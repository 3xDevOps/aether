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

const releaseCheckUsage = "usage: aether-edge release-check --tag <tag> --digest sha256:<digest> [--from <tag>] [--json]"

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
// image and, given --from, whether the upgrade to it must hold.
func releaseCheck(args []string, fetch func(context.Context, string) (selfupdate.Manifest, error), out io.Writer) error {
	fs := flag.NewFlagSet("aether-edge release-check", flag.ContinueOnError)
	tag := fs.String("tag", "", "release tag to check")
	digest := fs.String("digest", "", "index digest of the edge image to run")
	from := fs.String("from", "", "release tag the edge runs now")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tag == "" || *digest == "" || fs.NArg() != 0 {
		return errors.New(releaseCheckUsage)
	}
	if *from != "" && !selfupdate.ValidTag(*from) {
		return fmt.Errorf("--from %q is not a release tag such as v0.7.0", *from)
	}
	m, err := fetch(context.Background(), *tag)
	if err != nil {
		return err
	}
	if m.Edge == nil {
		return fmt.Errorf("release %s has no edge section in its signed %s; it predates signed edge releases", *tag, selfupdate.ManifestName)
	}
	if err := m.Edge.Validate(*tag); err != nil {
		return fmt.Errorf("release %s: %w", *tag, err)
	}
	if m.Edge.ImageDigest != *digest {
		return fmt.Errorf("release %s signs the edge image %s, not %s; do not run it", *tag, m.Edge.ImageDigest, *digest)
	}
	r := releaseReport{Version: m.Version, Commit: m.Commit, Edge: *m.Edge,
		ApplicableSteps: []selfupdate.ManualStep{}, Reasons: []string{}}
	if *from != "" {
		switch {
		case selfupdate.Behind(*tag, *from):
			r.Reasons = append(r.Reasons, fmt.Sprintf("refusing a downgrade: %s is older than %s", *tag, *from))
		case selfupdate.Behind(*from, m.Edge.MinUpgradeFrom):
			r.Reasons = append(r.Reasons, fmt.Sprintf("%s upgrades only from %s or later; upgrade %s to %s first",
				*tag, m.Edge.MinUpgradeFrom, *from, m.Edge.MinUpgradeFrom))
		}
		for _, s := range m.Edge.ManualSteps {
			if !selfupdate.Behind(*from, s.Since) || selfupdate.Behind(*tag, s.Since) {
				continue
			}
			r.ApplicableSteps = append(r.ApplicableSteps, s)
			if s.Blocking {
				r.Reasons = append(r.Reasons, fmt.Sprintf("manual step %s (since %s) must be done first: %s; see %s",
					s.ID, s.Since, s.Summary, s.DocURL))
			}
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
	steps, label := e.ManualSteps, "manual steps"
	if from != "" {
		steps, label = r.ApplicableSteps, "manual steps from "+from
	}
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
