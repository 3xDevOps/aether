package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/selfupdate/releasetest"
)

var at = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// releases serves signed manifests and pointers the way release
// downloads do; a path it does not hold answers 404.
type releases map[string][]byte

func (r releases) manifest(t *testing.T, tag, channel string, build, hotfix int, key ed25519.PrivateKey) {
	t.Helper()
	data, sig, err := selfupdate.SignManifest(selfupdate.Manifest{
		Schema: selfupdate.ManifestSchema, Channel: channel, Build: build, Hotfix: hotfix,
		Version: tag, Commit: commit(tag), PublishedAt: at, StoreSchema: 115,
		Assets: map[string]selfupdate.Asset{"aether-linux-amd64": {SHA256: strings.Repeat("a", 64), Size: 1}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	r["/releases/download/"+tag+"/"+selfupdate.ManifestName] = data
	r["/releases/download/"+tag+"/"+selfupdate.SignatureName] = sig
}

func (r releases) pointer(t *testing.T, channel, tag string, build, hotfix int) {
	t.Helper()
	data, sig := releasetest.SignPointer(t, selfupdate.Pointer{
		Schema: selfupdate.PointerSchema, Channel: channel, Tag: tag, Commit: commit(tag),
		Build: build, Hotfix: hotfix, StoreSchema: 115, PublishedAt: at,
	}, releasetest.Key)
	r["/releases/download/channels/"+channel+".json"] = data
	r["/releases/download/channels/"+channel+".json.sig"] = sig
}

// commit is a distinct full SHA per tag.
func commit(tag string) string { return fmt.Sprintf("%x", sha1.Sum([]byte(tag))) }

func (r releases) derive(t *testing.T, tag string) (string, error) {
	t.Helper()
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	t.Cleanup(selfupdate.TrustNightlyKeysForTest(releasetest.NightlyKey.Public().(ed25519.PublicKey)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, ok := r[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := run(context.Background(), []string{"-tag", tag, "-repo", srv.URL}, &out)
	return out.String(), err
}

func line(t *testing.T) releases {
	t.Helper()
	r := releases{}
	r.manifest(t, "v0.8.7", "stable", 1, 0, releasetest.Key)
	r.manifest(t, "v0.8.8", "stable", 1, 1, releasetest.Key)
	r.manifest(t, "v0.9.0", "stable", 412, 0, releasetest.Key)
	r.manifest(t, "v0.9.1", "stable", 412, 1, releasetest.Key)
	r.manifest(t, "v0.9.2", "stable", 412, 2, releasetest.Key)
	r.pointer(t, "stable", "v0.9.2", 412, 2)
	r.pointer(t, "beta", "v0.9.2", 412, 2)
	return r
}

func TestDerivesBuildAndHotfixFromSignedReleases(t *testing.T) {
	for name, c := range map[string]struct{ tag, base, previous, want string }{
		"next hotfix":  {"v0.9.3", "v0.9.0", "v0.9.2", "build=412\nhotfix=3\n"},
		"first hotfix": {"v0.9.1", "v0.9.0", "v0.9.0", "build=412\nhotfix=1\n"},
		// The first stable with channels is v0.8.7, so its line's base is
		// not vX.Y.0.
		"line based on v0.8.7": {"v0.8.9", "v0.8.7", "v0.8.8", "build=1\nhotfix=2\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := line(t).derive(t, c.tag)
			if err != nil {
				t.Fatal(err)
			}
			want := c.want + "base-tag=" + c.base + "\nbase-commit=" + commit(c.base) + "\nprevious-commit=" + commit(c.previous) + "\n"
			if got != want {
				t.Fatalf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestRefusesAHotfixItCannotOrder(t *testing.T) {
	untrusted := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	for name, c := range map[string]struct {
		tag    string
		change func(t *testing.T, r releases)
		want   string
	}{
		"beta tag":           {"v0.9.1-beta.1", nil, "not a hotfix tag"},
		"patch 0":            {"v0.9.0", nil, "not a hotfix tag"},
		"above 99":           {"v0.9.100", nil, "above 99"},
		"skipped number":     {"v0.9.5", nil, "v0.9.4 has no signed aether-release.json"},
		"line without one":   {"v0.7.1", nil, "v0.7.0 has no signed aether-release.json"},
		"pre-channel stable": {"v0.6.1", func(t *testing.T, r releases) { r.manifest(t, "v0.6.0", "", 0, 0, releasetest.Key) }, "before channels"},
		"beta previous": {"v0.9.1", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.0", "beta", 412, 0, releasetest.Key)
		}, "channel \"beta\" does not match v0.9.0"},
		"untrusted previous": {"v0.9.3", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.2", "stable", 412, 2, untrusted)
		}, "not signed by a trusted release key"},
		"nightly-key previous": {"v0.9.3", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.2", "stable", 412, 2, releasetest.NightlyKey)
		}, "signed by a nightly key"},
		"too many hotfixes": {"v0.9.1", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.0", "stable", 412, 1, releasetest.Key)
		}, "more hotfixes than its patch number allows"},
		"base on another build": {"v0.9.3", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.0", "stable", 400, 0, releasetest.Key)
		}, "v0.9.0 is a stable release at build 400, hotfix 0, so v0.9.2 at build 412, hotfix 2 does not follow it"},
		"base is a hotfix": {"v0.9.3", func(t *testing.T, r releases) {
			r.manifest(t, "v0.9.0", "stable", 412, 1, releasetest.Key)
		}, "does not follow it"},
		"no stable.json": {"v0.9.3", func(t *testing.T, r releases) {
			delete(r, "/releases/download/channels/stable.json")
		}, "channel pointer stable.json"},
		"no beta.json": {"v0.9.3", func(t *testing.T, r releases) {
			delete(r, "/releases/download/channels/beta.json.sig")
		}, "channel pointer beta.json"},
		"untrusted stable.json": {"v0.9.3", func(t *testing.T, r releases) {
			r["/releases/download/channels/stable.json.sig"] = r["/releases/download/v0.9.0/"+selfupdate.SignatureName]
		}, "stable.json"},
	} {
		t.Run(name, func(t *testing.T) {
			r := line(t)
			if c.change != nil {
				c.change(t, r)
			}
			got, err := r.derive(t, c.tag)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if got != "" {
				t.Fatalf("printed %q on a refusal", got)
			}
		})
	}
}
