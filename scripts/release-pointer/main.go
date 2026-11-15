// Command release-pointer signs a channel pointer, <channel>.json, naming
// a release whose signed manifest it is given:
//
//	release-pointer -channel beta -tag v0.9.0-beta.2 \
//	  -manifest dist/aether-release.json [-current current/beta.json] -out <dir>
//
// It verifies the manifest and the current pointer against the keys this
// build trusts, refuses to name a lower (build, hotfix) than the current
// pointer does, signs with AETHER_RELEASE_SIGNING_KEY, and verifies its own
// output the way an updater will. scripts/publish-pointer.sh runs it.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
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
	out := fs.String("out", "", "directory to write <channel>.json and its signature into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channel == "" || *tag == "" || *manifest == "" || *out == "" || fs.NArg() != 0 {
		return errors.New("usage: release-pointer -channel <c> -tag <tag> -manifest <file> [-current <file>] -out <dir>")
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
		if err = notBelow(*current, p); err != nil {
			return err
		}
	}
	key, err := signingKey(os.Getenv(keyEnv))
	if err != nil {
		return err
	}
	data, sig, err := sign(p, key)
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

// notBelow refuses p when the verified pointer at path names a higher
// (build, hotfix), so no pointer ever moves down.
func notBelow(path string, p selfupdate.Pointer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return err
	}
	cur, err := selfupdate.VerifyPointer(data, sig, p.Channel)
	if err != nil {
		return fmt.Errorf("the current %s does not verify, so it cannot be replaced safely: %w", selfupdate.PointerName(p.Channel), err)
	}
	if p.Build < cur.Build || p.Build == cur.Build && p.Hotfix < cur.Hotfix {
		return fmt.Errorf("%s names %s at %s; refusing to move it down to %s at %s",
			selfupdate.PointerName(p.Channel), cur.Tag, cur.Mark(), p.Tag, p.Mark())
	}
	return nil
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

// sign encodes p the way selfupdate.SignManifest encodes a manifest.
func sign(p selfupdate.Pointer, key ed25519.PrivateKey) (data, sig []byte, err error) {
	data, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	data = append(data, '\n')
	return data, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n"), nil
}
