package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

// nightly.yml hands the nightly key to this tool under keyEnv. It signs a
// nightly manifest with it and refuses every other channel, so a nightly
// run cannot produce a beta or stable release.
func TestRunSignsOnlyANightlyWithTheNightlyKey(t *testing.T) {
	release := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	defer selfupdate.TrustKeysForTest(release.Public().(ed25519.PublicKey))()
	defer selfupdate.TrustNightlyKeysForTest(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))()
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(seed))
	images := writeImages(t, t.TempDir(), testImages())
	for _, tc := range []struct{ tag, channel, refusal string }{
		{"v1.3.0-nightly.1", "nightly", ""},
		{"v1.3.0-beta.1", "beta", "signed by a nightly key but its channel is \"beta\""},
		{"v1.3.0", "stable", "signed by a nightly key but its channel is \"stable\""},
		{"v1.3.0-beta.15", "", "signed by a nightly key but its channel is \"\""},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			dir := t.TempDir()
			cli := filepath.Join(dir, "aether-linux-amd64")
			if err := os.WriteFile(cli, []byte("cli"), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"-version", tc.tag, "-commit", "abc", "-store-schema", "71", "-images", images, "-out", dir}
			if tc.channel != "" {
				args = append(args, "-channel", tc.channel, "-build", "2", "-hotfix", "0")
			}
			err := run(append(args, cli))
			_, statErr := os.Stat(filepath.Join(dir, selfupdate.ManifestName))
			if tc.refusal == "" {
				if err != nil || statErr != nil {
					t.Fatalf("run: %v, manifest: %v; want a signed nightly", err, statErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refusal) || statErr == nil {
				t.Fatalf("run: %v, manifest written: %t; want %q and no manifest", err, statErr == nil, tc.refusal)
			}
		})
	}
}
