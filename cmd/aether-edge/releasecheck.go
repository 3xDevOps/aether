package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

const releaseCheckUsage = "usage: aether-edge release-check --tag <tag> --digest sha256:<digest> --from <tag> --from-digest sha256:<digest> [--from-manifest <file>] [--keep-manifests <dir>] --channel <nightly|beta|stable|latest> [--json]"

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
// claims; --from-manifest reads a saved copy, so a release that has since
// been pruned still orders, and --keep-manifests saves the manifests it
// downloads once verified: the running release's, and --tag's when it is
// not refused. A --tag equal to --from checks the running
// release itself, which needs neither refusal.
func releaseCheck(args []string, fetch fetchSigned, out io.Writer) error {
	fs := flag.NewFlagSet("aether-edge release-check", flag.ContinueOnError)
	tag := fs.String("tag", "", "release tag to check")
	digest := fs.String("digest", "", "index digest of the edge image to run")
	from := fs.String("from", "", "release tag the edge runs now (required); whether that release can open the database\n"+
		"after the upgrade migrates it is not checked here: run aether-edge schema --json with its image")
	fromDigest := fs.String("from-digest", "", "index digest of the edge image the edge runs now (required)")
	fromManifest := fs.String("from-manifest", "", "saved "+selfupdate.ManifestName+" of the --from release, with its signature in <file>.sig; verified as a download is, and read instead of downloading it")
	keepDir := fs.String("keep-manifests", "", "directory to save the "+selfupdate.ManifestName+" and signature of --tag and, when downloaded, --from in, as <tag>.json and <tag>.json.sig, once each is verified")
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
	if *fromManifest != "" {
		download := fetch
		fetch = func(ctx context.Context, name string) (selfupdate.Manifest, []byte, []byte, error) {
			if name != *from {
				return download(ctx, name)
			}
			m, err := readManifest(*fromManifest, name)
			return m, nil, nil, err
		}
	}
	m, tagData, tagSig, err := signedEdge(fetch, *tag)
	if err != nil {
		return err
	}
	if m.Edge.ImageDigest != *digest {
		return fmt.Errorf("release %s signs the edge image %s, not %s; do not run it", *tag, m.Edge.ImageDigest, *digest)
	}
	before := func(boundary string) (bool, error) { return selfupdate.Behind(*from, boundary), nil }
	if *tag != *from {
		var running selfupdate.Manifest
		var data, sig []byte
		if running, data, sig, err = signedEdge(fetch, *from); err != nil {
			return fmt.Errorf("the running release: %w", err)
		}
		if running.Edge.ImageDigest != *fromDigest {
			return fmt.Errorf("the running release %s signs the edge image %s, not the running %s; pin the edge to the index digest %s",
				*from, running.Edge.ImageDigest, *fromDigest, running.Edge.ImageDigest)
		}
		if err = keepManifest(*keepDir, *from, data, sig); err != nil {
			return err
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
		if running.Build > 0 {
			before = buildBefore(fetch, running)
		}
	}
	if err = keepManifest(*keepDir, *tag, tagData, tagSig); err != nil {
		return err
	}
	r := releaseReport{Version: m.Version, Commit: m.Commit, Edge: *m.Edge,
		ApplicableSteps: []selfupdate.ManualStep{}, Reasons: []string{}}
	if below, unordered := before(m.Edge.MinUpgradeFrom); below {
		reason := fmt.Sprintf("%s upgrades only from %s or later; upgrade %s to %s first",
			*tag, m.Edge.MinUpgradeFrom, *from, m.Edge.MinUpgradeFrom)
		if unordered != nil {
			reason = fmt.Sprintf("%s upgrades only from %s or later, and %s cannot be ordered against it: %v", *tag, m.Edge.MinUpgradeFrom, *from, unordered)
		}
		r.Reasons = append(r.Reasons, reason)
	}
	for _, s := range m.Edge.ManualSteps {
		below, unordered := before(s.Since)
		if !below {
			continue
		}
		r.ApplicableSteps = append(r.ApplicableSteps, s)
		if s.Blocking {
			reason := fmt.Sprintf("manual step %s (since %s) must be done first: %s; see %s", s.ID, s.Since, s.Summary, s.DocURL)
			if unordered != nil {
				reason += fmt.Sprintf(" (%s cannot be ordered against %s, so the step counts as needed: %v)", *from, s.Since, unordered)
			}
			r.Reasons = append(r.Reasons, reason)
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

// fetchSigned fetches a tag's signed manifest, returning the bytes it
// verified, or none when they came from a kept copy.
type fetchSigned func(ctx context.Context, tag string) (m selfupdate.Manifest, data, sig []byte, err error)

// signedEdge fetches tag's signed manifest and checks its edge section.
func signedEdge(fetch fetchSigned, tag string) (selfupdate.Manifest, []byte, []byte, error) {
	m, data, sig, err := fetch(context.Background(), tag)
	if err != nil {
		return selfupdate.Manifest{}, nil, nil, err
	}
	if m.Edge == nil {
		return selfupdate.Manifest{}, nil, nil, fmt.Errorf("release %s has no edge section in its signed %s; it predates signed edge releases", tag, selfupdate.ManifestName)
	}
	if err = m.Edge.Validate(tag); err != nil {
		return selfupdate.Manifest{}, nil, nil, fmt.Errorf("release %s: %w", tag, err)
	}
	return m, data, sig, nil
}

// keepManifest saves tag's verified manifest data and signature sig in dir
// as <tag>.json and <tag>.json.sig, each replaced whole, the signature
// first, so a reader that finds the manifest finds its signature. It saves
// nothing without a dir or for bytes that came from a kept copy.
func keepManifest(dir, tag string, data, sig []byte) error {
	if dir == "" || data == nil {
		return nil
	}
	name := filepath.Join(dir, tag+".json")
	if err := replaceFile(name+".sig", sig); err != nil {
		return fmt.Errorf("keep the signed manifest of %s: %w", tag, err)
	}
	if err := replaceFile(name, data); err != nil {
		return fmt.Errorf("keep the signed manifest of %s: %w", tag, err)
	}
	return nil
}

func replaceFile(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".keep-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}

// readManifest verifies the manifest saved at path, with its signature at
// path.sig, as tag's.
func readManifest(path, tag string) (selfupdate.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	m, err := selfupdate.VerifyManifest(data, sig, tag)
	if err != nil {
		return selfupdate.Manifest{}, fmt.Errorf("saved %s: %w", path, err)
	}
	return m, nil
}

// firstBuiltRelease is the first release with a signed build: the plan's
// migration promotes it, build 1, from the last release before channels.
// Every release from it on is signed, so a missing manifest at or above it
// is not one from before channels.
const firstBuiltRelease = "v0.8.7"

// buildBefore reports whether running, a release with a build, is below a
// min_upgrade_from or manual step tag. A no-channel prerelease (-alpha.N,
// -rc.N) cannot be a channel release, so it predates every numbered one. A
// nightly tag cannot be ordered: nightlies are pruned, and the upgrade
// record never names one. A beta or stable tag is ordered by its signed
// manifest. Without one, a tag below firstBuiltRelease predates signed
// releases, so channels too; any other may not be published yet, or was
// withdrawn. A tag that cannot be ordered counts as above running, with
// the error: its step is needed, its floor not met.
func buildBefore(fetch fetchSigned, running selfupdate.Manifest) func(string) (bool, error) {
	seen := map[string]selfupdate.Manifest{}
	return func(boundary string) (bool, error) {
		switch {
		case boundary == running.Version, selfupdate.TagChannel(boundary) == "":
			return false, nil
		case selfupdate.TagChannel(boundary) == selfupdate.ChannelNightly:
			return true, fmt.Errorf("%s is a nightly tag, which cannot be ordered by build", boundary)
		}
		b, ok := seen[boundary]
		if !ok {
			var err error
			b, _, _, err = fetch(context.Background(), boundary)
			if errors.Is(err, selfupdate.ErrUnsigned) && selfupdate.Behind(boundary, firstBuiltRelease) {
				return false, nil
			}
			if err != nil {
				return true, err
			}
			seen[boundary] = b
		}
		return selfupdate.Newer(b.Release(), running.Release()), nil
	}
}
