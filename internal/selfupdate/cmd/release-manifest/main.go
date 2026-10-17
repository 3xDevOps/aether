// Command release-manifest writes and signs a release's aether-release.json.
//
//	AETHER_RELEASE_SIGNING_KEY=<base64 seed> \
//	  go run ./internal/selfupdate/cmd/release-manifest -version v0.8.0 -commit <sha> \
//	  -store-schema "$(go run ./internal/store/cmd/schema-version)" \
//	  -images images.json -out <dir> <asset>...
//
// It takes the store schema as a flag and the image digests and edge
// section as a file written by internal/edge/cmd/release-edge, rather than
// importing the stores, so the process holding the signing key links
// nothing but the signer.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// keyEnv holds the std-base64 32-byte ed25519 seed releases are signed with.
const keyEnv = "AETHER_RELEASE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-manifest:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("release-manifest", flag.ContinueOnError)
	tag := fs.String("version", "", "release tag the manifest describes")
	commit := fs.String("commit", "", "full commit the release was built from")
	storeSchema := fs.Int("store-schema", 0, "store schema version the release migrates to")
	images := fs.String("images", "", "JSON file of the release's image digests and edge section")
	out := fs.String("out", "", "directory to write "+selfupdate.ManifestName+" and its signature into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tag == "" || *commit == "" || *storeSchema <= 0 || *images == "" || *out == "" || fs.NArg() == 0 {
		return errors.New("usage: release-manifest -version <tag> -commit <sha> -store-schema <n> -images <file> -out <dir> <asset> [<asset>]")
	}
	img, err := readImages(*images)
	if err != nil {
		return err
	}
	key, err := signingKey(os.Getenv(keyEnv))
	if err != nil {
		return err
	}
	data, sig, err := generate(*tag, *commit, *storeSchema, img, fs.Args(), key, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, selfupdate.ManifestName), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, selfupdate.SignatureName), sig, 0o644)
}

func readImages(path string) (selfupdate.ImageRelease, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return selfupdate.ImageRelease{}, err
	}
	var img selfupdate.ImageRelease
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&img); err != nil {
		return selfupdate.ImageRelease{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return img, nil
}

func signingKey(seed string) (ed25519.PrivateKey, error) {
	if seed == "" {
		return nil, fmt.Errorf("%s is not set; the release cannot be signed", keyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(seed)
	if err != nil || len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a base64 %d-byte ed25519 seed", keyEnv, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

// generate hashes every asset, signs the manifest, and verifies the result
// the way an updater will, so a seed that does not match a key this build
// trusts fails here rather than on every client.
func generate(tag, commit string, storeSchema int, img selfupdate.ImageRelease, assets []string, key ed25519.PrivateKey, now time.Time) (data, sig []byte, err error) {
	if err := img.Validate(tag); err != nil {
		return nil, nil, err
	}
	m := selfupdate.Manifest{
		Schema:      selfupdate.ManifestSchema,
		Version:     tag,
		Commit:      commit,
		PublishedAt: now,
		NotesURL:    "https://github.com/" + selfupdate.Repo + "/releases/tag/" + tag,
		StoreSchema: storeSchema,
		Assets:      map[string]selfupdate.Asset{},
		Images:      img.Images,
		Edge:        &img.Edge,
	}
	for _, path := range assets {
		name := filepath.Base(path)
		if _, dup := m.Assets[name]; dup {
			return nil, nil, fmt.Errorf("%s is listed twice", name)
		}
		if m.Assets[name], err = hashFile(path); err != nil {
			return nil, nil, err
		}
	}
	if data, sig, err = selfupdate.SignManifest(m, key); err != nil {
		return nil, nil, err
	}
	signed, err := selfupdate.VerifyManifest(data, sig, tag)
	if err != nil {
		return nil, nil, fmt.Errorf("verify the signed manifest: %w", err)
	}
	if signed.Edge == nil || !reflect.DeepEqual(selfupdate.ImageRelease{Images: signed.Images, Edge: *signed.Edge}, img) {
		return nil, nil, errors.New("the signed manifest does not carry the images and edge section it was given")
	}
	return data, sig, nil
}

func hashFile(path string) (selfupdate.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return selfupdate.Asset{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return selfupdate.Asset{}, fmt.Errorf("hash %s: %w", path, err)
	}
	return selfupdate.Asset{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}
