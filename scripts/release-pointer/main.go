// Command release-pointer signs a channel pointer, <channel>.json, naming
// a release whose signed manifest it is given:
//
//	release-pointer -channel beta -tag v0.9.0-beta.2 \
//	  -manifest dist/aether-release.json {-current current/beta.json | -first [-current ...]} [-if-above] -out <dir>
//
// It verifies the manifest and the current pointer against the keys this
// build trusts, refuses to name a lower (build, hotfix) than the current
// pointer does, signs with AETHER_RELEASE_SIGNING_KEY, and verifies its own
// output the way an updater will. A channel without a current pointer
// needs -first, so a pointer deleted by a failed upload or by hand is never
// silently replaced with a lower one. -first beside -current is the re-run
// of the run that wrote that first pointer: it succeeds without writing
// when the current pointer names exactly this release, and refuses any
// other. With -if-above it writes nothing, and
// succeeds, unless the release is above the current pointer: promoting to
// stable moves beta.json only then. scripts/publish-pointer.sh runs it.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

const keyEnv = "AETHER_RELEASE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:], time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "release-pointer:", err)
		os.Exit(1)
	}
}

func run(args []string, now time.Time) error {
	fs := flag.NewFlagSet("release-pointer", flag.ContinueOnError)
	channel := fs.String("channel", "", "channel whose pointer to sign: nightly, beta or stable")
	tag := fs.String("tag", "", "release tag the pointer names")
	manifest := fs.String("manifest", "", "the release's signed "+selfupdate.ManifestName+"; its signature is beside it")
	current := fs.String("current", "", "the channel's current pointer, if it has one; its signature is beside it")
	first := fs.Bool("first", false, "the channel has no pointer yet and this is its first")
	ifAbove := fs.Bool("if-above", false, "write the pointer only when the release is above the current one, and succeed without writing otherwise")
	out := fs.String("out", "", "directory to write <channel>.json and its signature into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channel == "" || *tag == "" || *manifest == "" || *out == "" || fs.NArg() != 0 {
		return errors.New("usage: release-pointer -channel <c> -tag <tag> -manifest <file> {-current <file> | -first [-current <file>]} [-if-above] -out <dir>")
	}
	if *current == "" && !*first {
		return errors.New("pass -current, the channel's pointer now, or -first, for a channel that has none yet")
	}
	switch *channel {
	case selfupdate.ChannelNightly, selfupdate.ChannelBeta, selfupdate.ChannelStable:
	default:
		return fmt.Errorf("%q is not a release channel (nightly, beta or stable)", *channel)
	}
	m, err := readManifest(*manifest, *tag)
	if err != nil {
		return err
	}
	if err = m.AcceptedOn(*channel); err != nil {
		return fmt.Errorf("%s cannot be the %s pointer: %w", *tag, *channel, err)
	}
	p := selfupdate.Pointer{
		Schema:      selfupdate.PointerSchema,
		Channel:     *channel,
		Tag:         m.Version,
		Commit:      m.Commit,
		Build:       m.Build,
		Hotfix:      m.Hotfix,
		StoreSchema: m.StoreSchema,
		PublishedAt: now.UTC().Truncate(time.Second),
	}
	if *current != "" {
		var cur selfupdate.Pointer
		if cur, err = readCurrent(*current, *channel); err != nil {
			return err
		}
		switch {
		case *first && (cur.Tag != p.Tag || cur.Commit != p.Commit || cur.Mark() != p.Mark()):
			return fmt.Errorf("%s already names %s at %s; first-pointer is only for a channel without a pointer, or the re-run of the run that wrote %s",
				selfupdate.PointerName(p.Channel), cur.Tag, cur.Mark(), p.Tag)
		case *first:
			fmt.Printf("%s already names %s at %s\n", selfupdate.PointerName(p.Channel), p.Tag, p.Mark())
			return nil
		case below(p.Mark(), cur.Mark()) && !*ifAbove:
			return fmt.Errorf("%s names %s at %s; refusing to move it down to %s at %s",
				selfupdate.PointerName(p.Channel), cur.Tag, cur.Mark(), p.Tag, p.Mark())
		case *ifAbove && !below(cur.Mark(), p.Mark()):
			fmt.Printf("%s stays at %s (%s): %s at %s is not above it\n",
				selfupdate.PointerName(p.Channel), cur.Tag, cur.Mark(), p.Tag, p.Mark())
			return nil
		}
	}
	key, err := signingKey(os.Getenv(keyEnv))
	if err != nil {
		return err
	}
	data, sig, err := selfupdate.SignPointer(p, key)
	if err != nil {
		return err
	}
	name := selfupdate.PointerName(*channel)
	if _, err = selfupdate.VerifyPointer(data, sig, *channel); err != nil {
		return fmt.Errorf("verify the signed pointer: %w", err)
	}
	if err = os.WriteFile(filepath.Join(*out, name), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, name+".sig"), sig, 0o644)
}

func readManifest(path, tag string) (selfupdate.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	return selfupdate.VerifyManifest(data, sig, tag)
}

func readCurrent(path, channel string) (selfupdate.Pointer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return selfupdate.Pointer{}, err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return selfupdate.Pointer{}, err
	}
	cur, err := selfupdate.VerifyPointer(data, sig, channel)
	if err != nil {
		return selfupdate.Pointer{}, fmt.Errorf("the current %s does not verify, so it cannot be replaced safely: %w; restore the last good pair from the pointer-%s artifact of the run that last moved it", selfupdate.PointerName(channel), err, channel)
	}
	return cur, nil
}

func below(a, b selfupdate.Mark) bool {
	return a.Build < b.Build || a.Build == b.Build && a.Hotfix < b.Hotfix
}

func signingKey(seed string) (ed25519.PrivateKey, error) {
	if seed == "" {
		return nil, fmt.Errorf("%s is not set; the pointer cannot be signed", keyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(seed)
	if err != nil || len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a base64 %d-byte ed25519 seed", keyEnv, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(raw), nil
}
