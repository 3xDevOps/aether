package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

const (
	nightlyCommit = "1111111111111111111111111111111111111111"
	betaCommit    = "2222222222222222222222222222222222222222"
	preCommit     = "3333333333333333333333333333333333333333"
	otherCommit   = "4444444444444444444444444444444444444444"
)

var now = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// world is the repository a Promote reads: its tags, published releases
// and the channels release's pointers.
type world struct {
	tags      map[string]string
	published []string
	pointers  map[string]int
}

func baseWorld() world {
	return world{
		tags: map[string]string{
			"v0.8.7-beta.14":    preCommit,
			"v0.8.7":            preCommit,
			"v0.9.0-nightly.11": otherCommit,
			"v0.9.0-nightly.12": nightlyCommit,
			"v0.9.0-beta.1":     otherCommit,
			"v0.9.0-beta.2":     betaCommit,
		},
		published: []string{"v0.8.7-beta.14", "v0.8.7", "v0.9.0-nightly.11", "v0.9.0-nightly.12", "v0.9.0-beta.1", "v0.9.0-beta.2"},
		pointers:  map[string]int{"stable": 1, "beta": 400},
	}
}

type release struct {
	tag, channel, commit string
	build                int
	key                  ed25519.PrivateKey
}

func (w world) resolve(t *testing.T, src release, args ...string) (map[string]string, error) {
	t.Helper()
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	for i, a := range args {
		if a == "-trust-nightly" {
			t.Cleanup(selfupdate.TrustNightlyKeysForTest(releasetest.NightlyKey.Public().(ed25519.PublicKey)))
			args = append(args[:i:i], args[i+1:]...)
			break
		}
	}
	dir := t.TempDir()
	var tags strings.Builder
	for tag, commit := range w.tags {
		tags.WriteString(tag + " " + commit + "\n")
	}
	write(t, filepath.Join(dir, "tags"), []byte(tags.String()))
	write(t, filepath.Join(dir, "published"), []byte(strings.Join(w.published, "\n")))
	key := src.key
	if key == nil {
		key = releasetest.Key
	}
	data, sig, err := selfupdate.SignManifest(selfupdate.Manifest{
		Schema: selfupdate.ManifestSchema, Channel: src.channel, Build: src.build,
		Version: src.tag, Commit: src.commit, PublishedAt: now, StoreSchema: 115,
		Assets: map[string]selfupdate.Asset{"aether-linux-amd64": {SHA256: strings.Repeat("a", 64), Size: 1}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, selfupdate.ManifestName)
	write(t, manifest, data)
	write(t, manifest+".sig", sig)
	pointers := filepath.Join(dir, "pointers")
	if err = os.Mkdir(pointers, 0o755); err != nil {
		t.Fatal(err)
	}
	for ch, build := range w.pointers {
		data, sig := releasetest.SignPointer(t, selfupdate.Pointer{
			Schema: selfupdate.PointerSchema, Channel: ch, Tag: "v0.0.1", Commit: otherCommit,
			Build: build, StoreSchema: 115, PublishedAt: now,
		}, releasetest.Key)
		write(t, filepath.Join(pointers, ch+".json"), data)
		write(t, filepath.Join(pointers, ch+".json.sig"), sig)
	}
	var out bytes.Buffer
	err = run(append([]string{"resolve", "-from", src.tag,
		"-tags", filepath.Join(dir, "tags"), "-published", filepath.Join(dir, "published"),
		"-manifest", manifest, "-pointers", pointers}, args...), &out)
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got, err
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

var (
	nightly12 = release{tag: "v0.9.0-nightly.12", channel: "nightly", commit: nightlyCommit, build: 412}
	beta2     = release{tag: "v0.9.0-beta.2", channel: "beta", commit: betaCommit, build: 405}
	pre14     = release{tag: "v0.8.7-beta.14", commit: preCommit}
)

func TestResolvesTheNewRelease(t *testing.T) {
	unpublishedBeta3 := baseWorld()
	unpublishedBeta3.tags["v0.9.0-beta.3"] = nightlyCommit
	noStable := baseWorld()
	delete(noStable.tags, "v0.8.7")
	noStable.published = []string{"v0.8.7-beta.14"}
	noStable.pointers = nil
	stableOverride := baseWorld()
	unpublishedStable := baseWorld()
	unpublishedStable.tags["v0.9.0"] = betaCommit
	for _, tc := range []struct {
		name  string
		w     world
		src   release
		args  []string
		want  string
		extra map[string]string
	}{
		{"nightly to the next beta", baseWorld(), nightly12, []string{"-to", "beta"}, "tag=v0.9.0-beta.3 commit=" + nightlyCommit + " channel=beta build=412 hotfix=0 pointer=beta pointer-if-above= first-pointer=false", nil},
		{"a failed Promote of the same commit resumes its tag", unpublishedBeta3, nightly12, []string{"-to", "beta"}, "tag=v0.9.0-beta.3", nil},
		{"an override at the base's floor or above", baseWorld(), nightly12, []string{"-to", "beta", "-version", "1.0.0"}, "tag=v1.0.0-beta.1", nil},
		{"beta to stable moves beta when above it", baseWorld(), beta2, []string{"-to", "stable"}, "tag=v0.9.0 commit=" + betaCommit + " channel=stable build=405 hotfix=0 pointer=stable pointer-if-above=beta first-pointer=false", nil},
		{"the migration's first stable", noStable, pre14, []string{"-to", "stable", "-first-pointer"}, "tag=v0.8.7 commit=" + preCommit + " channel=stable build=1 hotfix=0 pointer=stable pointer-if-above=beta first-pointer=true", nil},
		{"a failed stable Promote's tag resumes, its tag not raising the floor", unpublishedStable, beta2, []string{"-to", "stable"}, "tag=v0.9.0", nil},
		{"stable override", stableOverride, beta2, []string{"-to", "stable", "-version", "0.10.0"}, "tag=v0.10.0", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.w.resolve(t, tc.src, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, kv := range strings.Fields(tc.want) {
				k, v, _ := strings.Cut(kv, "=")
				if got[k] != v {
					t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestRefusesAPromotion(t *testing.T) {
	unpublished := baseWorld()
	unpublished.published = unpublished.published[:3]
	orphan := baseWorld()
	orphan.tags["v0.9.0-beta.3"] = otherCommit
	moved := baseWorld()
	moved.tags["v0.9.0-nightly.12"] = otherCommit
	highBeta := baseWorld()
	highBeta.pointers["beta"] = 420
	equalBeta := baseWorld()
	equalBeta.pointers["beta"] = 412
	noBeta := baseWorld()
	delete(noBeta.pointers, "beta")
	released := baseWorld()
	released.tags["v0.9.0"] = betaCommit
	released.published = append(released.published, "v0.9.0")
	noStable := baseWorld()
	delete(noStable.tags, "v0.8.7")
	noStable.pointers = nil
	halfStable := noStable
	halfStable.pointers = map[string]int{"stable": 1}
	oldPre := release{tag: "v0.8.6-beta.3", commit: preCommit}
	oldPreWorld := noStable
	oldPreWorld.tags = map[string]string{"v0.8.6-beta.3": preCommit}
	oldPreWorld.published = []string{"v0.8.6-beta.3"}
	nightlyKeyBeta := beta2
	nightlyKeyBeta.key = releasetest.NightlyKey
	for _, tc := range []struct {
		name string
		w    world
		src  release
		args []string
		want string
	}{
		{"to beta from a beta", baseWorld(), beta2, []string{"-to", "beta"}, "promotes only a published nightly"},
		{"to stable from a nightly", baseWorld(), nightly12, []string{"-to", "stable"}, "promotes only a published beta"},
		{"an unpublished source", unpublished, nightly12, []string{"-to", "beta"}, "not a published release"},
		{"a tag that moved off the signed commit", moved, nightly12, []string{"-to", "beta"}, "signed manifest names commit"},
		{"a manifest no trusted key signed", baseWorld(), nightlyKeyBeta, []string{"-to", "stable"}, "not signed by a trusted release key"},
		{"a version below the nightly base", baseWorld(), nightly12, []string{"-to", "beta", "-version", "0.8.9"}, "below the current nightly base 0.9.0"},
		{"a malformed version", baseWorld(), nightly12, []string{"-to", "beta", "-version", "1.0"}, "not X.Y.Z"},
		{"a failed Promote's tag on another commit", orphan, nightly12, []string{"-to", "beta"}, "has no published release"},
		{"a build below the channel's pointer", highBeta, nightly12, []string{"-to", "beta"}, "not above it, and a channel only moves up"},
		{"a build equal to the channel's pointer", equalBeta, nightly12, []string{"-to", "beta"}, "beta.json names v0.0.1 at build 412; build 412 is not above it"},
		{"a nightly key signing a beta", baseWorld(), nightlyKeyBeta, []string{"-to", "stable", "-trust-nightly"}, "signed by a nightly key but its channel is \"beta\""},
		{"a channel without a pointer", noBeta, nightly12, []string{"-to", "beta"}, "first-pointer"},
		{"a stable already released", released, beta2, []string{"-to", "stable", "-version", "0.9.0"}, "below the current nightly base 0.10.0"},
		{"first-pointer from a channel release", baseWorld(), beta2, []string{"-to", "stable", "-first-pointer"}, "only for promoting the pre-channel"},
		{"the pre-channel beta without first-pointer", noStable, pre14, []string{"-to", "stable"}, "release from before channels"},
		{"the pre-channel beta to beta", noStable, pre14, []string{"-to", "beta", "-first-pointer"}, "release from before channels"},
		{"another pre-channel release", oldPreWorld, oldPre, []string{"-to", "stable", "-first-pointer"}, "release from before channels"},
		{"the migration under another version", noStable, pre14, []string{"-to", "stable", "-first-pointer", "-version", "0.9.0"}, "first stable is v0.8.7"},
		{"the migration once a pointer exists", halfStable, pre14, []string{"-to", "stable", "-first-pointer"}, "channels already has stable.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.w.resolve(t, tc.src, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if len(got) != 0 {
				t.Fatalf("printed %v on a refusal", got)
			}
		})
	}
}

func TestSourceFindsTheReleaseACommitNames(t *testing.T) {
	dir := t.TempDir()
	tags := filepath.Join(dir, "tags")
	write(t, tags, []byte("v0.9.0-nightly.9 "+nightlyCommit+"\nv0.9.0-nightly.10 "+nightlyCommit+"\nv0.9.0-beta.1 "+nightlyCommit+"\nv0.9.0-beta.2 "+nightlyCommit+"\n"))
	// v0.9.0-beta.2 is a failed Promote's tag with no published release.
	published := filepath.Join(dir, "published")
	write(t, published, []byte("v0.9.0-nightly.9\nv0.9.0-nightly.10\nv0.9.0-beta.1\n"))
	for to, want := range map[string]string{"beta": "v0.9.0-nightly.10", "stable": "v0.9.0-beta.1"} {
		var out bytes.Buffer
		if err := run([]string{"source", "-from", nightlyCommit, "-to", to, "-tags", tags, "-published", published}, &out); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Errorf("to %s: source = %s, want %s", to, got, want)
		}
	}
	if err := run([]string{"source", "-from", otherCommit, "-to", "beta", "-tags", tags, "-published", published}, &bytes.Buffer{}); err == nil {
		t.Fatal("a commit no nightly names resolved")
	}
}
