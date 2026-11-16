package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

var now = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func trust(t *testing.T) {
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(releasetest.Key.Seed()))
}

// writeManifest writes a manifest for tag signed with the test release key
// into a new directory and returns its path.
func writeManifest(t *testing.T, tag, channel string, build int) string {
	t.Helper()
	m := selfupdate.Manifest{
		Schema:      selfupdate.ManifestSchema,
		Channel:     channel,
		Build:       build,
		Version:     tag,
		Commit:      commit,
		PublishedAt: now,
		StoreSchema: 115,
		Assets:      map[string]selfupdate.Asset{"aether-linux-amd64": {SHA256: strings.Repeat("a", 64), Size: 1}},
	}
	data, sig, err := selfupdate.SignManifest(m, releasetest.Key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), selfupdate.ManifestName)
	writeSigned(t, path, data, sig)
	return path
}

// writeCurrent writes beta's current pointer at build, signed with key.
func writeCurrent(t *testing.T, tag string, build int, key ed25519.PrivateKey) string {
	t.Helper()
	data, sig := releasetest.SignPointer(t, selfupdate.Pointer{
		Schema: selfupdate.PointerSchema, Channel: "beta", Tag: tag, Commit: commit,
		Build: build, StoreSchema: 115, PublishedAt: now.Add(-time.Hour),
	}, key)
	path := filepath.Join(t.TempDir(), "beta.json")
	writeSigned(t, path, data, sig)
	return path
}

func writeSigned(t *testing.T, path string, data, sig []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", sig, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSignsAPointerAnUpdaterFollows(t *testing.T) {
	trust(t)
	manifest := writeManifest(t, "v0.9.0-beta.2", "beta", 412)
	for name, current := range map[string]string{
		"first pointer": "",
		"equal build":   writeCurrent(t, "v0.9.0-beta.1", 412, releasetest.Key),
		"higher build":  writeCurrent(t, "v0.9.0-beta.1", 380, releasetest.Key),
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			args := []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", manifest, "-out", out}
			if current != "" {
				args = append(args, "-current", current)
			} else {
				args = append(args, "-first")
			}
			if err := run(args, now); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(out, "beta.json"))
			sig, _ := os.ReadFile(filepath.Join(out, "beta.json.sig"))
			p, err := selfupdate.VerifyPointer(data, sig, "beta")
			if err != nil {
				t.Fatal(err)
			}
			want := selfupdate.Pointer{
				Schema: selfupdate.PointerSchema, Channel: "beta", Tag: "v0.9.0-beta.2", Commit: commit,
				Build: 412, StoreSchema: 115, PublishedAt: now,
			}
			if p.Tag != want.Tag || p.Commit != want.Commit || p.Mark() != want.Mark() ||
				p.StoreSchema != want.StoreSchema || !p.PublishedAt.Equal(want.PublishedAt) {
				t.Fatalf("pointer %+v, want %+v", p, want)
			}
		})
	}
}

func TestRefuses(t *testing.T) {
	trust(t)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	beta := writeManifest(t, "v0.9.0-beta.2", "beta", 412)
	for _, tc := range []struct {
		name, want string
		args       []string
		key        ed25519.PrivateKey
	}{
		{
			name: "a lower build",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta,
				"-current", writeCurrent(t, "v0.9.0-beta.3", 413, releasetest.Key)},
			want: "beta.json names v0.9.0-beta.3 at build 413; refusing to move it down to v0.9.0-beta.2 at build 412",
		},
		{
			name: "no current pointer without -first",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta},
			want: "pass -current",
		},
		{
			name: "-first beside a pointer naming another release",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta, "-first",
				"-current", writeCurrent(t, "v0.9.0-beta.1", 380, releasetest.Key)},
			want: "beta.json already names v0.9.0-beta.1 at build 380; first-pointer is only",
		},
		{
			name: "-first beside a pointer naming this tag at another build",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta, "-first",
				"-current", writeCurrent(t, "v0.9.0-beta.2", 413, releasetest.Key)},
			want: "beta.json already names v0.9.0-beta.2 at build 413",
		},
		{
			name: "a channel the release is not on",
			args: []string{"-channel", "stable", "-tag", "v0.9.0-beta.2", "-manifest", beta, "-first"},
			want: "v0.9.0-beta.2 cannot be the stable pointer",
		},
		{
			name: "a current pointer it cannot verify",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta,
				"-current", writeCurrent(t, "v0.9.0-beta.1", 1, other)},
			want: "the current beta.json does not verify",
		},
		{
			name: "a key the build does not trust",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.2", "-manifest", beta, "-first"},
			key:  other,
			want: "verify the signed pointer",
		},
		{
			name: "a manifest of another tag",
			args: []string{"-channel", "beta", "-tag", "v0.9.0-beta.3", "-manifest", beta, "-first"},
			want: "describes v0.9.0-beta.2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.key != nil {
				t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(tc.key.Seed()))
			}
			out := t.TempDir()
			err := run(append(tc.args, "-out", out), now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if names, _ := os.ReadDir(out); len(names) != 0 {
				t.Fatalf("wrote %v after refusing", names)
			}
		})
	}
}

// Promoting to stable moves beta.json only when the stable is above it.
func TestIfAboveMovesOnlyUpward(t *testing.T) {
	trust(t)
	stable := writeManifest(t, "v0.9.0", "stable", 412)
	for _, tc := range []struct {
		name    string
		current string
		moved   bool
	}{
		{"no pointer yet", "", true},
		{"a lower pointer", writeCurrent(t, "v0.9.0-beta.1", 380, releasetest.Key), true},
		{"an equal pointer", writeCurrent(t, "v0.9.0-beta.2", 412, releasetest.Key), false},
		{"a higher pointer", writeCurrent(t, "v0.9.1-beta.1", 500, releasetest.Key), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			args := []string{"-channel", "beta", "-tag", "v0.9.0", "-manifest", stable, "-if-above", "-out", out}
			if tc.current != "" {
				args = append(args, "-current", tc.current)
			} else {
				args = append(args, "-first")
			}
			if err := run(args, now); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(out, "beta.json"))
			if !tc.moved {
				if !os.IsNotExist(err) {
					t.Fatalf("wrote beta.json %q, want it kept", data)
				}
				return
			}
			sig, _ := os.ReadFile(filepath.Join(out, "beta.json.sig"))
			p, err := selfupdate.VerifyPointer(data, sig, "beta")
			if err != nil || p.Tag != "v0.9.0" || p.Build != 412 {
				t.Fatalf("pointer %+v, %v; want v0.9.0 at build 412", p, err)
			}
		})
	}
}

// The migration's Promote writes the first stable.json and then the first
// beta.json; a re-run after the second failed must get past the first.
func TestFirstConvergesOnTheRunsOwnPointer(t *testing.T) {
	trust(t)
	beta := writeManifest(t, "v0.9.0-beta.3", "beta", 413)
	out := t.TempDir()
	if err := run([]string{"-channel", "beta", "-tag", "v0.9.0-beta.3", "-manifest", beta, "-first",
		"-current", writeCurrent(t, "v0.9.0-beta.3", 413, releasetest.Key), "-out", out}, now); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("wrote %v over the pointer this release already has", entries)
	}
}
