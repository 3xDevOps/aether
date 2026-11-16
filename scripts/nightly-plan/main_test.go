package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

var now = time.Date(2026, 12, 20, 10, 0, 0, 0, time.UTC)

func commit(n int) string { return strings.Repeat(fmt.Sprintf("%x", n%16), 40) }

// tagRef is a v* tag: annotated with a build: line when build > 0.
type tagRef struct {
	commit string
	build  int
}

type pointer struct {
	tag, commit string
	build       int
	key         ed25519.PrivateKey
}

// world is what a nightly run reads: tags, releases (published when the
// time is set, a draft when zero), pointers and the manifests of published
// nightlies.
type world struct {
	refs      map[string]tagRef
	releases  map[string]time.Time
	pointers  map[string]pointer
	manifests map[string]selfupdate.Manifest
	keys      map[string]ed25519.PrivateKey
	// halfNightly leaves nightly.json without its .sig.
	halfNightly bool
}

// steady is three nightlies after the first stable: nightly.1 was pruned,
// nightly.3 is the newest and nightly.json names it.
func steady() world {
	w := world{
		refs: map[string]tagRef{
			"v0.8.7-beta.14":   {commit(1), 0},
			"v0.8.7":           {commit(1), 1},
			"v0.9.0-nightly.1": {commit(2), 2},
			"v0.9.0-nightly.2": {commit(3), 3},
			"v0.9.0-nightly.3": {commit(4), 4},
		},
		releases: map[string]time.Time{
			"v0.8.7-beta.14":   now.AddDate(0, -1, 0),
			"v0.8.7":           now.AddDate(0, -1, 0),
			"v0.9.0-nightly.2": now.AddDate(0, 0, -15),
			"v0.9.0-nightly.3": now.Add(-time.Hour),
		},
		pointers: map[string]pointer{
			"stable":  {"v0.8.7", commit(1), 1, nil},
			"beta":    {"v0.8.7", commit(1), 1, nil},
			"nightly": {"v0.9.0-nightly.3", commit(4), 4, releasetest.NightlyKey},
		},
		manifests: map[string]selfupdate.Manifest{},
		keys:      map[string]ed25519.PrivateKey{},
	}
	return w
}

// first is the world of migration step 4: the first stable and no nightly.
func first() world {
	w := steady()
	for _, tag := range []string{"v0.9.0-nightly.1", "v0.9.0-nightly.2", "v0.9.0-nightly.3"} {
		delete(w.refs, tag)
		delete(w.releases, tag)
	}
	delete(w.pointers, "nightly")
	return w
}

func (w world) publishNightly(tag string, published time.Time) {
	r := w.refs[tag]
	w.releases[tag] = published
	w.manifests[tag] = selfupdate.Manifest{Channel: "nightly", Build: r.build, Version: tag, Commit: r.commit}
}

func (w world) write(t *testing.T, manifestTags ...string) (dir string) {
	t.Helper()
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	t.Cleanup(selfupdate.TrustNightlyKeysForTest(releasetest.NightlyKey.Public().(ed25519.PublicKey)))
	dir = t.TempDir()
	var refs bytes.Buffer
	for tag, r := range w.refs {
		if r.build == 0 {
			fmt.Fprintf(&refs, "%s commit %s \n%s\n\x00\n", tag, r.commit, "subject of the commit")
		} else {
			fmt.Fprintf(&refs, "%s tag %s %s\n%s\n\nbuild: %d\n\x00\n", tag, strings.Repeat("e", 40), r.commit, tag, r.build)
		}
	}
	write(t, filepath.Join(dir, "refs"), refs.Bytes())
	var releases strings.Builder
	for tag, at := range w.releases {
		if at.IsZero() {
			fmt.Fprintf(&releases, "%s true -\n", tag)
		} else {
			fmt.Fprintf(&releases, "%s false %s\n", tag, at.Format(time.RFC3339))
		}
	}
	write(t, filepath.Join(dir, "releases"), []byte(releases.String()))
	ptrs := filepath.Join(dir, "pointers")
	mkdir(t, ptrs)
	for ch, p := range w.pointers {
		key := p.key
		if key == nil {
			key = releasetest.Key
		}
		data, sig := releasetest.SignPointer(t, selfupdate.Pointer{
			Schema: selfupdate.PointerSchema, Channel: ch, Tag: p.tag, Commit: p.commit,
			Build: p.build, StoreSchema: 115, PublishedAt: now.Add(-2 * time.Hour),
		}, key)
		write(t, filepath.Join(ptrs, ch+".json"), data)
		if ch != "nightly" || !w.halfNightly {
			write(t, filepath.Join(ptrs, ch+".json.sig"), sig)
		}
	}
	manifests := filepath.Join(dir, "manifests")
	mkdir(t, manifests)
	for _, tag := range manifestTags {
		m := w.manifests[tag]
		m.Schema, m.StoreSchema, m.PublishedAt = selfupdate.ManifestSchema, 115, now
		m.Assets = map[string]selfupdate.Asset{"aether-linux-amd64": {SHA256: strings.Repeat("a", 64), Size: 1}}
		key := w.keys[tag]
		if key == nil {
			key = releasetest.NightlyKey
		}
		data, sig, err := selfupdate.SignManifest(m, key)
		if err != nil {
			t.Fatal(err)
		}
		mkdir(t, filepath.Join(manifests, tag))
		write(t, filepath.Join(manifests, tag, selfupdate.ManifestName), data)
		write(t, filepath.Join(manifests, tag, selfupdate.ManifestName+".sig"), sig)
	}
	return dir
}

func (w world) state(t *testing.T, manifestTags ...string) (map[string]string, error) {
	t.Helper()
	dir := w.write(t, manifestTags...)
	var out bytes.Buffer
	err := run([]string{"state", "-refs", filepath.Join(dir, "refs"), "-releases", filepath.Join(dir, "releases"),
		"-pointers", filepath.Join(dir, "pointers"), "-manifests", filepath.Join(dir, "manifests")}, &out)
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

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func want(t *testing.T, got map[string]string, err error, pairs ...string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, "=")
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestStateNamesTheNextNightly(t *testing.T) {
	t.Run("the first nightly after the first stable", func(t *testing.T) {
		got, err := first().state(t)
		want(t, got, err, "next-tag=v0.9.0-nightly.1", "next-build=2", "first-pointer=true", "newest-tag=", "resume-tag=")
	})
	t.Run("the next nightly takes one above nightly.json, whatever was pruned", func(t *testing.T) {
		got, err := steady().state(t)
		want(t, got, err, "next-tag=v0.9.0-nightly.4", "next-build=5", "first-pointer=false",
			"newest-tag=v0.9.0-nightly.3", "newest-commit="+commit(4), "newest-published-at="+now.Add(-time.Hour).Format(time.RFC3339))
	})
	t.Run("a beta promoted under a higher version moves the base", func(t *testing.T) {
		w := steady()
		w.refs["v1.0.0-beta.1"] = tagRef{commit(4), 4}
		w.pointers["beta"] = pointer{"v1.0.0-beta.1", commit(4), 4, nil}
		got, err := w.state(t)
		want(t, got, err, "next-tag=v1.0.0-nightly.1", "next-build=5")
	})
	t.Run("a new stable bumps the minor, after any tag of that base", func(t *testing.T) {
		w := steady()
		w.refs["v0.9.0"] = tagRef{commit(4), 4}
		w.refs["v0.10.0-nightly.1"] = tagRef{commit(5), 0}
		w.pointers["stable"] = pointer{"v0.9.0", commit(4), 4, nil}
		w.pointers["beta"] = pointer{"v0.9.0", commit(4), 4, nil}
		got, err := w.state(t)
		want(t, got, err, "next-tag=v0.10.0-nightly.2", "next-build=5")
	})
	t.Run("a tag a failed run left is resumed with its numbers", func(t *testing.T) {
		w := steady()
		w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
		w.releases["v0.9.0-nightly.4"] = time.Time{}
		got, err := w.state(t)
		want(t, got, err, "resume-tag=v0.9.0-nightly.4", "resume-commit="+commit(5), "resume-build=5", "first-pointer=false")
	})
	t.Run("the first nightly's failed run is resumed with first-pointer", func(t *testing.T) {
		w := first()
		w.refs["v0.9.0-nightly.1"] = tagRef{commit(5), 2}
		got, err := w.state(t)
		want(t, got, err, "resume-tag=v0.9.0-nightly.1", "resume-build=2", "first-pointer=true")
	})
	t.Run("a nightly published without moving its pointer counts by its signed build", func(t *testing.T) {
		w := steady()
		w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
		w.publishNightly("v0.9.0-nightly.4", now.Add(-30*time.Minute))
		got, err := w.state(t, "v0.9.0-nightly.4")
		want(t, got, err, "next-tag=v0.9.0-nightly.5", "next-build=6", "newest-tag=v0.9.0-nightly.4", "newest-commit="+commit(5), "resume-tag=")
	})
	t.Run("a deleted or pruned nightly below the pointers is not resumed", func(t *testing.T) {
		w := steady()
		delete(w.releases, "v0.9.0-nightly.3")
		got, err := w.state(t)
		want(t, got, err, "resume-tag=", "next-build=5", "newest-tag=v0.9.0-nightly.3", "newest-published-at="+now.Add(-2*time.Hour).Format(time.RFC3339))
	})
}

func TestStateRefuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		w         func() world
		manifests []string
		want      string
	}{
		{"a tag claiming a build far above every signed one", func() world {
			w := steady()
			w.refs["v0.9.0-nightly.9"] = tagRef{commit(5), 19999990}
			return w
		}, nil, "claims build 19999990, but a failed run tags build 5"},
		{"two tags without releases above the signed builds", func() world {
			w := steady()
			w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
			w.refs["v0.9.0-nightly.5"] = tagRef{commit(6), 5}
			return w
		}, nil, "one failed run leaves one such tag"},
		{"a published tag above the pointers whose manifest was not read", func() world {
			w := steady()
			w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
			w.publishNightly("v0.9.0-nightly.4", now)
			return w
		}, nil, "its manifest was not read"},
		{"a tag whose build line disagrees with its signed manifest", func() world {
			w := steady()
			w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 50}
			w.publishNightly("v0.9.0-nightly.4", now)
			m := w.manifests["v0.9.0-nightly.4"]
			m.Build = 5
			w.manifests["v0.9.0-nightly.4"] = m
			return w
		}, []string{"v0.9.0-nightly.4"}, "its tag names build 50"},
		{"a beta manifest under a nightly tag", func() world {
			w := steady()
			w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
			w.publishNightly("v0.9.0-nightly.4", now)
			m := w.manifests["v0.9.0-nightly.4"]
			m.Channel = "beta"
			w.manifests["v0.9.0-nightly.4"] = m
			w.keys["v0.9.0-nightly.4"] = releasetest.NightlyKey
			return w
		}, []string{"v0.9.0-nightly.4"}, "signed by a nightly key"},
		{"a beta.json signed with the nightly key", func() world {
			w := steady()
			w.pointers["beta"] = pointer{"v0.9.0-beta.1", commit(4), 400, releasetest.NightlyKey}
			return w
		}, nil, "signed by a nightly key"},
		{"a pointer no trusted key signed", func() world {
			w := steady()
			w.pointers["nightly"] = pointer{"v0.9.0-nightly.3", commit(4), 400, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))}
			return w
		}, nil, "not signed by a trusted release key"},
		{"no stable.json yet", func() world {
			w := first()
			delete(w.pointers, "stable")
			return w
		}, nil, "channels has no stable.json"},
		{"nightlies without nightly.json", func() world {
			w := steady()
			delete(w.pointers, "nightly")
			w.publishNightly("v0.9.0-nightly.2", now.AddDate(0, 0, -15))
			w.publishNightly("v0.9.0-nightly.3", now.Add(-time.Hour))
			return w
		}, []string{"v0.9.0-nightly.2", "v0.9.0-nightly.3"}, "channels has no nightly.json, but nightly"},
		{"half of nightly.json", func() world {
			w := steady()
			w.halfNightly = true
			return w
		}, nil, "read nightly.json.sig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.w().state(t, tc.manifests...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("state: %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestManifestsListsPublishedTagsAboveThePointers(t *testing.T) {
	w := steady()
	w.refs["v0.9.0-nightly.4"] = tagRef{commit(5), 5}
	w.publishNightly("v0.9.0-nightly.4", now)
	w.refs["v0.9.0-nightly.5"] = tagRef{commit(6), 6}
	dir := w.write(t)
	var out bytes.Buffer
	if err := run([]string{"manifests", "-refs", filepath.Join(dir, "refs"), "-releases", filepath.Join(dir, "releases"),
		"-pointers", filepath.Join(dir, "pointers")}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "v0.9.0-nightly.4\n" {
		t.Fatalf("manifests = %q, want only the published v0.9.0-nightly.4", got)
	}
}

func TestPruneKeepsWhatAWaitingServerNeeds(t *testing.T) {
	// Nightlies 1 to 6 at builds 2 to 7; stable was promoted from build 4.
	w := steady()
	w.refs = map[string]tagRef{}
	w.releases = map[string]time.Time{}
	for i := 1; i <= 6; i++ {
		tag := fmt.Sprintf("v0.9.0-nightly.%d", i)
		w.refs[tag] = tagRef{commit(i), i + 1}
		w.publishNightly(tag, now.AddDate(0, 0, -30+i))
	}
	w.publishNightly("v0.9.0-nightly.6", now.AddDate(0, 0, -3))
	w.pointers["stable"] = pointer{"v0.9.0", commit(3), 4, nil}
	w.pointers["nightly"] = pointer{"v0.9.0-nightly.6", commit(6), 7, releasetest.NightlyKey}
	w.keys["v0.9.0-nightly.2"] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32))
	var tags []string
	for tag := range w.manifests {
		tags = append(tags, tag)
	}
	prune := func(w world) (string, error) {
		dir := w.write(t, tags...)
		var out bytes.Buffer
		err := run([]string{"prune", "-releases", filepath.Join(dir, "releases"), "-pointers", filepath.Join(dir, "pointers"),
			"-manifests", filepath.Join(dir, "manifests"), "-now", now.Format(time.RFC3339)}, &out)
		return out.String(), err
	}
	// 1 and 3 are old and at or below stable; 2 does not verify; 4 and 5
	// are above stable; 6 is the newest and young.
	got, err := prune(w)
	if err != nil {
		t.Fatal(err)
	}
	if want := "v0.9.0-nightly.1 " + commit(1) + "\nv0.9.0-nightly.3 " + commit(3) + "\n"; got != want {
		t.Fatalf("prune = %q, want %q", got, want)
	}

	old := w
	old.releases = map[string]time.Time{}
	for tag := range w.releases {
		old.releases[tag] = now.AddDate(0, -2, 0)
	}
	old.pointers = map[string]pointer{"stable": {"v0.9.1", commit(6), 7, nil}, "nightly": w.pointers["nightly"]}
	if got, err = prune(old); err != nil || strings.Contains(got, "nightly.6") || !strings.Contains(got, "nightly.5") {
		t.Fatalf("prune with everything old and stable at the newest build = %q, %v; want all but the newest nightly and the unverified one", got, err)
	}

	for _, ch := range []string{"stable", "nightly"} {
		missing := w
		missing.pointers = map[string]pointer{}
		for k, v := range w.pointers {
			if k != ch {
				missing.pointers[k] = v
			}
		}
		if got, err := prune(missing); err == nil || got != "" {
			t.Fatalf("prune without %s.json = %q, %v; want nothing and an error", ch, got, err)
		}
	}
	forged := w
	forged.pointers = map[string]pointer{"stable": {"v0.9.0", commit(3), 4, releasetest.NightlyKey}, "nightly": w.pointers["nightly"]}
	if got, err := prune(forged); err == nil || got != "" {
		t.Fatalf("prune with a stable.json the nightly key signed = %q, %v; want nothing and an error", got, err)
	}
}

func TestReadsTheRefsNightlyPlanShLists(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "nightly-plan.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`git for-each-ref --format='([^']*)' 'refs/tags/v\*'`).FindSubmatch(script)
	if m == nil {
		t.Fatal("nightly-plan.sh lists no refs/tags/v* with git for-each-ref --format")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("git %v: %v\n%s", args, runErr, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "one")
	head := git("rev-parse", "HEAD")
	git("tag", "-a", "v0.9.0-nightly.1", "-m", "v0.9.0-nightly.1\n\nbuild: 2")
	git("tag", "-a", "v0.9.0-nightly.2", "-m", "v0.9.0-nightly.2\n\nno build line\nbuild: 07")
	git("tag", "v0.9.0-nightly.3")
	path := filepath.Join(t.TempDir(), "refs")
	write(t, path, []byte(git("for-each-ref", "--format="+string(m[1]), "refs/tags/v*")))
	refs, err := readRefs(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ref{
		"v0.9.0-nightly.1": {"v0.9.0-nightly.1", head, 2},
		"v0.9.0-nightly.2": {"v0.9.0-nightly.2", head, 0},
		"v0.9.0-nightly.3": {"v0.9.0-nightly.3", head, 0},
	}
	if fmt.Sprint(refs) != fmt.Sprint(want) {
		t.Fatalf("readRefs = %v, want %v", refs, want)
	}
}

func TestKeyCheck(t *testing.T) {
	t.Cleanup(selfupdate.TrustKeysForTest(releasetest.PublicKey()))
	t.Cleanup(selfupdate.TrustNightlyKeysForTest(releasetest.NightlyKey.Public().(ed25519.PublicKey)))
	seed := func(k ed25519.PrivateKey) string { return base64.StdEncoding.EncodeToString(k.Seed()) }
	untrusted := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x33}, ed25519.SeedSize))
	if err := keyCheck(seed(releasetest.NightlyKey)); err != nil {
		t.Fatalf("a trusted nightly key: %v", err)
	}
	for name, tc := range map[string]struct{ seed, want string }{
		"untrusted":     {seed(untrusted), "not signed by a trusted release key; do not install it; its public half must be in nightlyKeys"},
		"a release key": {seed(releasetest.Key), "so it is a release key"},
		"empty":         {"", "is not a base64 32-byte ed25519 seed"},
		"not a seed":    {"c2hvcnQ=", "is not a base64 32-byte ed25519 seed"},
	} {
		t.Run(name, func(t *testing.T) {
			err := keyCheck(tc.seed)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("keyCheck: %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.seed) && tc.seed != "" {
				t.Fatal("the error prints the seed")
			}
		})
	}
}
