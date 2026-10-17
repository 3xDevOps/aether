package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func testImages() selfupdate.ImageRelease {
	return selfupdate.ImageRelease{
		Images: map[string]string{"aether-browser": digest("b"), "aether-edge": digest("e"), "aether-standard": digest("5")},
		Edge: selfupdate.Edge{
			Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: digest("e"),
			StoreSchema: 3, StoreCompatFloor: 2, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
			MinUpgradeFrom: "v1.0.0",
			ManualSteps: []selfupdate.ManualStep{{
				ID: "env-rename", Since: "v1.2.0", Summary: "rename a variable",
				DocURL: "https://example.com/edge", Blocking: true,
			}},
		},
	}
}

func writeImages(t *testing.T, dir string, img selfupdate.ImageRelease) string {
	t.Helper()
	data, err := json.Marshal(img)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "images.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeneratedManifestVerifiesWithTheTrustedKey(t *testing.T) {
	seed := bytes.Repeat([]byte{3}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	defer selfupdate.TrustKeysForTest(key.Public().(ed25519.PublicKey))()

	dir := t.TempDir()
	cli := filepath.Join(dir, "aether-linux-amd64")
	if err := os.WriteFile(cli, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	images := writeImages(t, t.TempDir(), testImages())
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(seed))
	if err := run([]string{"-version", "v1.3.0", "-commit", "abc", "-store-schema", "71", "-images", images, "-out", dir, cli}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, selfupdate.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, selfupdate.SignatureName))
	if err != nil {
		t.Fatal(err)
	}
	m, err := selfupdate.VerifyManifest(data, sig, "v1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("cli"))
	want := selfupdate.Asset{SHA256: hex.EncodeToString(sum[:]), Size: 3}
	if got := m.Assets["aether-linux-amd64"]; got != want {
		t.Fatalf("asset = %+v, want %+v", got, want)
	}
	if m.StoreSchema != 71 || m.Commit != "abc" ||
		m.NotesURL != "https://github.com/3xDevOps/Aether/releases/tag/v1.3.0" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Edge == nil || !reflect.DeepEqual(selfupdate.ImageRelease{Images: m.Images, Edge: *m.Edge}, testImages()) {
		t.Fatalf("images = %v, edge = %+v", m.Images, m.Edge)
	}
}

func TestGenerateRefusesImagesThatDisagree(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	for _, c := range []struct {
		name   string
		change func(*selfupdate.ImageRelease)
		want   string
	}{
		{"another edge digest", func(r *selfupdate.ImageRelease) { r.Edge.ImageDigest = digest("f") }, "is not the aether-edge image"},
		{"another repository", func(r *selfupdate.ImageRelease) { r.Edge.Image = "ghcr.io/3xdevops/aether-browser" }, "is not ghcr.io/3xdevops/aether-edge"},
		{"a step newer than the release", func(r *selfupdate.ImageRelease) { r.Edge.ManualSteps[0].Since = "v1.4.0" }, `since "v1.4.0" is not a release tag at or below v1.3.0`},
	} {
		img := testImages()
		c.change(&img)
		_, _, err := generate("v1.3.0", "abc", 71, img, nil, key, time.Now())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestGenerateRefusesASeedThisBuildDoesNotTrust(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize))
	asset := filepath.Join(t.TempDir(), "aether-linux-amd64")
	if err := os.WriteFile(asset, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := generate("v1.3.0", "abc", 71, testImages(), []string{asset}, key, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not signed by a trusted release key") {
		t.Fatalf("err = %v, want the untrusted key refused", err)
	}
}

func TestRunRefusesAMissingKey(t *testing.T) {
	t.Setenv(keyEnv, "")
	err := run([]string{"-version", "v1.3.0", "-commit", "abc", "-store-schema", "71", "-images", writeImages(t, t.TempDir(), testImages()), "-out", t.TempDir(), "x"})
	if err == nil || !strings.Contains(err.Error(), keyEnv+" is not set") {
		t.Fatalf("err = %v, want the missing key named", err)
	}
}
