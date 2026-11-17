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

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
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
		m.NotesURL != "https://github.com/3xDevOps/cloudfield/releases/tag/v1.3.0" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Edge == nil || !reflect.DeepEqual(selfupdate.ImageRelease{Images: m.Images, Edge: *m.Edge}, testImages()) {
		t.Fatalf("images = %v, edge = %+v", m.Images, m.Edge)
	}
}

func TestRunWritesChannelFieldsOnlyWhenGiven(t *testing.T) {
	seed := bytes.Repeat([]byte{3}, ed25519.SeedSize)
	defer selfupdate.TrustKeysForTest(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))()
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(seed))
	asset := filepath.Join(t.TempDir(), "aether-linux-amd64")
	if err := os.WriteFile(asset, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	images := writeImages(t, t.TempDir(), testImages())
	for _, c := range []struct {
		tag   string
		flags []string
		want  selfupdate.Release
	}{
		{"v1.3.0-beta.14", nil, selfupdate.Release{Version: "v1.3.0-beta.14"}},
		{"v1.4.0-nightly.12", []string{"-channel", "nightly", "-build", "412"}, selfupdate.Release{Version: "v1.4.0-nightly.12", Channel: "nightly", Build: 412}},
		{"v1.4.0-beta.2", []string{"-channel", "beta", "-build", "412"}, selfupdate.Release{Version: "v1.4.0-beta.2", Channel: "beta", Build: 412}},
		{"v1.4.1", []string{"-channel", "stable", "-build", "412", "-hotfix", "1"}, selfupdate.Release{Version: "v1.4.1", Channel: "stable", Build: 412, Hotfix: 1}},
	} {
		out := t.TempDir()
		args := append([]string{"-version", c.tag, "-commit", "abc", "-store-schema", "71", "-images", images, "-out", out}, c.flags...)
		if err := run(append(args, asset)); err != nil {
			t.Fatalf("%s: %v", c.tag, err)
		}
		data, err := os.ReadFile(filepath.Join(out, selfupdate.ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		sig, err := os.ReadFile(filepath.Join(out, selfupdate.SignatureName))
		if err != nil {
			t.Fatal(err)
		}
		m, err := selfupdate.VerifyManifest(data, sig, c.tag)
		if err != nil {
			t.Fatalf("%s: %v", c.tag, err)
		}
		if m.Release() != c.want {
			t.Errorf("%s: signed %+v, want %+v", c.tag, m.Release(), c.want)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"channel", "build", "hotfix"} {
			_, written := fields[name]
			if want := c.flags != nil && (name != "hotfix" || c.want.Hotfix != 0); written != want {
				t.Errorf("%s: %s written = %v, want %v", c.tag, name, written, want)
			}
		}
	}
}

func TestGenerateRefusesChannelFieldsAnUpdaterRefuses(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	defer selfupdate.TrustKeysForTest(key.Public().(ed25519.PublicKey))()
	asset := filepath.Join(t.TempDir(), "aether-linux-amd64")
	if err := os.WriteFile(asset, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		order selfupdate.Release
		want  string
	}{
		{selfupdate.Release{Version: "v1.3.0", Channel: "beta", Build: 5}, `channel "beta" does not match v1.3.0, a stable tag`},
		{selfupdate.Release{Version: "v1.3.0-beta.1", Channel: "nightly", Build: 5}, `channel "nightly" does not match v1.3.0-beta.1, a beta tag`},
		{selfupdate.Release{Version: "v1.3.0-nightly.1", Channel: "stable", Build: 5}, `channel "stable" does not match v1.3.0-nightly.1, a nightly tag`},
		{selfupdate.Release{Version: "v1.3.0-alpha.1", Channel: "beta", Build: 5}, "a tag of no channel"},
		{selfupdate.Release{Version: "v1.3.0", Channel: "stable"}, "the build must be positive"},
		{selfupdate.Release{Version: "v1.3.0", Channel: "stable", Build: -1}, "the build must be positive"},
		{selfupdate.Release{Version: "v1.3.0", Channel: "stable", Build: 5, Hotfix: -1}, "the hotfix not negative"},
		{selfupdate.Release{Version: "v1.3.0", Build: 5}, "build 5 and hotfix 0 without a channel"},
		{selfupdate.Release{Version: "v1.3.0", Hotfix: 1}, "build 0 and hotfix 1 without a channel"},
		{selfupdate.Release{Version: "v1.3.0", Channel: "edge", Build: 5}, `channel "edge" does not match v1.3.0`},
	} {
		_, _, err := generate(c.order, "abc", 71, testImages(), []string{asset}, key, time.Now())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.order, err, c.want)
		}
	}
}

func TestRunRefusesBuildAndHotfixThatAreNotPlainDecimals(t *testing.T) {
	t.Setenv(keyEnv, "")
	out := t.TempDir()
	for _, c := range []struct{ flag, value, want string }{
		{"-build", "010", `-build "010" is not a decimal number`},
		{"-build", "0x19c", `-build "0x19c" is not a decimal number`},
		{"-build", "+5", `-build "+5" is not a decimal number`},
		{"-build", "-1", `-build "-1" is not a decimal number`},
		{"-build", "", `-build "" is not a decimal number`},
		{"-build", "20000000", "-build 20000000 is above 19999999"},
		{"-build", "99999999999999999999", "-build 99999999999999999999 is above 19999999"},
		{"-hotfix", "100", "-hotfix 100 is above 99"},
		{"-hotfix", "07", `-hotfix "07" is not a decimal number`},
	} {
		args := []string{"-version", "v1.4.0-beta.2", "-commit", "abc", "-store-schema", "71", "-channel", "beta", "-build", "412",
			c.flag, c.value, "-images", filepath.Join(out, "absent.json"), "-out", out, "x"}
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: err = %v, want %q", c.flag, c.value, err, c.want)
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("refused runs wrote %v", entries)
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
		_, _, err := generate(selfupdate.Release{Version: "v1.3.0"}, "abc", 71, img, nil, key, time.Now())
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
	_, _, err := generate(selfupdate.Release{Version: "v1.3.0"}, "abc", 71, testImages(), []string{asset}, key, time.Now())
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
