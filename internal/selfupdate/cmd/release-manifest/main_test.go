package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

func TestGeneratedManifestVerifiesWithTheTrustedKey(t *testing.T) {
	seed := bytes.Repeat([]byte{3}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	defer selfupdate.TrustKeysForTest(key.Public().(ed25519.PublicKey))()

	dir := t.TempDir()
	cli := filepath.Join(dir, "aether-linux-amd64")
	if err := os.WriteFile(cli, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(seed))
	if err := run([]string{"-version", "v1.3.0", "-commit", "abc", "-store-schema", "71", "-out", dir, cli}); err != nil {
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
}

func TestGenerateRefusesASeedThisBuildDoesNotTrust(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize))
	asset := filepath.Join(t.TempDir(), "aether-linux-amd64")
	if err := os.WriteFile(asset, []byte("cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := generate("v1.3.0", "abc", 71, []string{asset}, key, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not signed by a trusted release key") {
		t.Fatalf("err = %v, want the untrusted key refused", err)
	}
}

func TestRunRefusesAMissingKey(t *testing.T) {
	t.Setenv(keyEnv, "")
	err := run([]string{"-version", "v1.3.0", "-commit", "abc", "-store-schema", "71", "-out", t.TempDir(), "x"})
	if err == nil || !strings.Contains(err.Error(), keyEnv+" is not set") {
		t.Fatalf("err = %v, want the missing key named", err)
	}
}
