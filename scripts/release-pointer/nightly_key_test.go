package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

// nightly.yml hands the nightly key to this tool under keyEnv. It signs
// nightly.json with it and nothing else.
func TestTheNightlyKeySignsOnlyNightlyJSON(t *testing.T) {
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	t.Cleanup(selfupdate.TrustNightlyKeysForTest(releasetest.NightlyKey.Public().(ed25519.PublicKey)))
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(releasetest.NightlyKey.Seed()))
	nightly := writeManifest(t, "v0.9.0-nightly.1", "nightly", 2)
	out := t.TempDir()
	if err := run([]string{"-channel", "nightly", "-tag", "v0.9.0-nightly.1", "-manifest", nightly, "-out", out, "-first"}, now); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(out, "nightly.json"))
	sig, _ := os.ReadFile(filepath.Join(out, "nightly.json.sig"))
	if _, err := selfupdate.VerifyPointer(data, sig, "nightly"); err != nil {
		t.Fatal(err)
	}

	for _, ch := range []string{"beta", "stable"} {
		out := t.TempDir()
		manifest := writeManifest(t, "v0.9.0", "stable", 2)
		err := run([]string{"-channel", ch, "-tag", "v0.9.0", "-manifest", manifest, "-out", out, "-first"}, now)
		if err == nil || !strings.Contains(err.Error(), "signed by a nightly key") {
			t.Fatalf("%s.json signed with the nightly key: %v, want it refused", ch, err)
		}
		if entries, _ := os.ReadDir(out); len(entries) != 0 {
			t.Fatalf("%s.json signed with the nightly key left %d files", ch, len(entries))
		}
	}
}
