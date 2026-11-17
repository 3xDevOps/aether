package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

var releaseKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

func edgeDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// serveRelease publishes v1.5.0, from before release channels, with edge
// as its signed edge section, signed by key, and the releases the tests
// upgrade from, which sign edgeDigest("a"). It returns a fetch that reads
// them the way release-check does.
func serveRelease(t *testing.T, edge *selfupdate.Edge, key ed25519.PrivateKey) fetchSigned {
	t.Helper()
	ms := []selfupdate.Manifest{{Version: "v1.5.0", Edge: edge}}
	for _, from := range []string{"v1.0.0", "v1.3.0", "v1.4.0", "v1.5.0-alpha.2", "v1.6.0"} {
		ms = append(ms, selfupdate.Manifest{Version: from, Edge: &selfupdate.Edge{
			Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("a"),
			StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
			MinUpgradeFrom: "v0.5.0", ManualSteps: []selfupdate.ManualStep{}}})
	}
	return serveManifests(t, key, ms...)
}

// signManifest completes m as a release and signs it with key.
func signManifest(t *testing.T, key ed25519.PrivateKey, m selfupdate.Manifest) (data, sig []byte) {
	t.Helper()
	m.Schema, m.Commit, m.PublishedAt, m.StoreSchema = selfupdate.ManifestSchema, "c0ffee", time.Now(), 1
	m.Assets = map[string]selfupdate.Asset{"aether-edge-linux-amd64": {SHA256: strings.Repeat("0", 64), Size: 1}}
	data, sig, err := selfupdate.SignManifest(m, key)
	if err != nil {
		t.Fatal(err)
	}
	return data, sig
}

// serveManifests publishes each manifest under its version, signed by key.
func serveManifests(t *testing.T, key ed25519.PrivateKey, ms ...selfupdate.Manifest) fetchSigned {
	t.Helper()
	t.Cleanup(selfupdate.TrustKeysForTest(releaseKey.Public().(ed25519.PublicKey)))
	mux := http.NewServeMux()
	for _, m := range ms {
		data, sig := signManifest(t, key, m)
		mux.HandleFunc("/releases/download/"+m.Version+"/"+selfupdate.ManifestName, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
		mux.HandleFunc("/releases/download/"+m.Version+"/"+selfupdate.SignatureName, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return func(ctx context.Context, name string) (selfupdate.Manifest, []byte, []byte, error) {
		return selfupdate.FetchSignedManifestFrom(ctx, srv.URL, name)
	}
}

func testEdge() *selfupdate.Edge {
	return &selfupdate.Edge{
		Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("e"),
		StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
		MinUpgradeFrom: "v1.1.0",
		ManualSteps: []selfupdate.ManualStep{
			{ID: "old", Since: "v1.2.0", Summary: "old step", DocURL: "https://example.com/old", Blocking: true},
			{ID: "env", Since: "v1.4.0", Summary: "set a variable", DocURL: "https://example.com/env"},
			{ID: "unit", Since: "v1.5.0-alpha.2", Summary: "replace the unit", DocURL: "https://example.com/unit", Blocking: true},
		},
	}
}

func TestReleaseCheckListsTheStepsAnUpgradeCrosses(t *testing.T) {
	fetch := serveRelease(t, testEdge(), releaseKey)
	cases := []struct {
		from  string
		steps []string
		hold  string
	}{
		{from: "v1.4.0", steps: []string{"unit"}, hold: "manual step unit (since v1.5.0-alpha.2) must be done first"},
		{from: "v1.3.0", steps: []string{"env", "unit"}, hold: "manual step unit"},
		{from: "v1.5.0-alpha.2", steps: []string{}},
		{from: "v1.5.0", steps: []string{}},
		{from: "v1.0.0", steps: []string{"old", "env", "unit"}, hold: "upgrades only from v1.1.0 or later"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		err := releaseCheck([]string{"--tag", "v1.5.0", "--digest", edgeDigest("e"), "--from", c.from, "--from-digest", edgeDigest("a"), "--channel", "beta", "--json"}, fetch, &out)
		var r releaseReport
		if jerr := json.Unmarshal(out.Bytes(), &r); jerr != nil {
			t.Fatalf("from %s: %v in %q", c.from, jerr, out.String())
		}
		ids := []string{}
		for _, s := range r.ApplicableSteps {
			ids = append(ids, s.ID)
		}
		if strings.Join(ids, ",") != strings.Join(c.steps, ",") {
			t.Errorf("from %s: steps %v, want %v", c.from, ids, c.steps)
		}
		var hold *holdError
		if c.hold == "" {
			if err != nil || r.Hold {
				t.Errorf("from %s: err %v, hold %v, want ok", c.from, err, r.Hold)
			}
			continue
		}
		if !errors.As(err, &hold) || !r.Hold || !strings.Contains(strings.Join(r.Reasons, "\n"), c.hold) {
			t.Errorf("from %s: err %v, reasons %v, want a hold with %q", c.from, err, r.Reasons, c.hold)
		}
	}
}

func TestReleaseCheckPrintsTheEdgeSection(t *testing.T) {
	fetch := serveRelease(t, testEdge(), releaseKey)
	var out bytes.Buffer
	if err := releaseCheck([]string{"--tag", "v1.5.0", "--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-digest", edgeDigest("a"), "--channel", "beta"}, fetch, &out); err == nil {
		t.Fatal("want the blocking unit step to hold")
	}
	got := regexp.MustCompile(` {2,}`).ReplaceAllString(out.String(), "  ")
	for _, want := range []string{
		"image  ghcr.io/3xdevops/aether-edge@" + edgeDigest("e"),
		"store schema  4, compat floor 3",
		"manual steps from v1.4.0  blocking unit (since v1.5.0-alpha.2): replace the unit https://example.com/unit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReleaseCheckRefuses(t *testing.T) {
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	cases := []struct {
		name  string
		fetch fetchSigned
		args  []string
		want  string
	}{
		{"another digest", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("f"), "--from", "v1.4.0"}, "signs the edge image " + edgeDigest("e") + ", not " + edgeDigest("f")},
		{"an untrusted signature", serveRelease(t, testEdge(), other),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0"}, "not signed by a trusted release key"},
		{"a release without an edge section", serveRelease(t, nil, releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0"}, "predates signed edge releases"},
		{"a --from that is not a tag", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "dev"}, `--from "dev" is not a release tag`},
		{"no --from", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e")}, "--from <tag>"},
		{"no --from-digest", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--channel", "beta"}, "--from-digest sha256:<digest>"},
		{"no --channel", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-digest", edgeDigest("a")}, "--channel <nightly|beta|stable|latest>"},
		{"another channel name", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-digest", edgeDigest("a"), "--channel", "edge"}, `--channel "edge" is not nightly, beta, stable or latest`},
		{"an older release", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.6.0", "--from-digest", edgeDigest("a"), "--channel", "beta"}, "refusing to upgrade: v1.5.0 is older than the running v1.6.0"},
		{"a running digest its release does not sign", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-digest", edgeDigest("b"), "--channel", "beta"}, "the running release v1.4.0 signs the edge image " + edgeDigest("a") + ", not the running " + edgeDigest("b") + "; pin the edge to the index digest " + edgeDigest("a")},
		{"a running release that cannot be fetched", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.2.0", "--from-digest", edgeDigest("a"), "--channel", "beta"}, "the running release: "},
		{"a step newer than the release", serveRelease(t, func() *selfupdate.Edge {
			e := testEdge()
			e.ManualSteps = append(e.ManualSteps, selfupdate.ManualStep{ID: "later", Since: "v1.6.0", Summary: "s", DocURL: "https://example.com/later", Blocking: true})
			return e
		}(), releaseKey), []string{"--digest", edgeDigest("e"), "--from", "v1.4.0"}, `since "v1.6.0" is not a release tag at or below v1.5.0`},
	}
	for _, c := range cases {
		args := append([]string{"--tag", "v1.5.0"}, c.args...)
		if !slices.Contains(args, "--from-digest") && !slices.Contains(args, "--channel") {
			args = append(args, "--from-digest", edgeDigest("a"), "--channel", "beta")
		}
		err := releaseCheck(args, c.fetch, &bytes.Buffer{})
		var hold *holdError
		if err == nil || errors.As(err, &hold) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// TestReleaseCheckFollowsTheHostsChannelByBuild runs release-check as an
// edge host on each channel would, against releases published on each.
func TestReleaseCheckFollowsTheHostsChannelByBuild(t *testing.T) {
	release := func(tag, channel string, build, hotfix int) selfupdate.Manifest {
		return selfupdate.Manifest{Version: tag, Channel: channel, Build: build, Hotfix: hotfix, Edge: &selfupdate.Edge{
			Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("e"),
			StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
			MinUpgradeFrom: "v0.5.0", ManualSteps: []selfupdate.ManualStep{}}}
	}
	fetch := serveManifests(t, releaseKey,
		release("v0.8.7-beta.14", "", 0, 0),
		release("v0.8.7", "stable", 1, 0),
		release("v0.9.0-nightly.10", "nightly", 400, 0),
		release("v0.9.0-nightly.13", "nightly", 420, 0),
		release("v0.8.6", "", 0, 0),
		release("v0.8.7-beta.13", "", 0, 0),
		release("v0.8.7-beta.15", "", 0, 0),
		release("v0.9.0-beta.1", "beta", 300, 0),
		release("v0.9.0-beta.2", "beta", 412, 0),
		release("v0.9.0-beta.5", "beta", 430, 0),
		release("v0.9.0", "stable", 412, 0),
		release("v0.9.1", "stable", 412, 1),
	)
	cases := []struct {
		host, from, tag, want string
	}{
		{"beta", "v0.9.0-beta.5", "v0.9.0-beta.1", "v0.9.0-beta.1 (build 300) is older than the running v0.9.0-beta.5 (build 430)"},
		{"beta", "v0.9.0-beta.2", "v0.9.0-nightly.13", "v0.9.0-nightly.13 is a nightly release, which an install on beta does not take"},
		{"stable", "v0.8.7", "v0.9.0-beta.2", "v0.9.0-beta.2 is a beta release, which an install on stable does not take"},
		{"stable", "v0.8.6", "v0.8.7-beta.14", "v0.8.7-beta.14 is a no channel release, which an install on stable does not take"},
		{"nightly", "v0.9.0-beta.2", "v0.9.0-nightly.10", "v0.9.0-nightly.10 (build 400) is older than the running v0.9.0-beta.2 (build 412)"},
		{"latest", "v0.9.0-nightly.13", "v0.9.0", "v0.9.0 (build 412) is older than the running v0.9.0-nightly.13 (build 420)"},
		{"beta", "v0.8.7-beta.15", "v0.8.7-beta.14", "v0.8.7-beta.14 is older than the running v0.8.7-beta.15"},
		{"stable", "v0.9.1", "v0.9.0", "v0.9.0 (build 412) is older than the running v0.9.1 (build 412, hotfix 1)"},
		{"beta", "v0.9.0-nightly.10", "v0.9.0-beta.2", ""},
		{"stable", "v0.9.0-beta.2", "v0.9.0", ""},
		{"nightly", "v0.9.0-beta.2", "v0.9.0-nightly.13", ""},
		{"latest", "v0.9.0", "v0.9.0-nightly.13", "v0.9.0-nightly.13 is a nightly release, which an install on latest does not take"},
		{"latest", "v0.8.7-beta.14", "v0.9.0-beta.2", ""},
		{"beta", "v0.8.7-beta.13", "v0.8.7-beta.14", ""},
		{"beta", "v0.8.7-beta.14", "v0.8.7", ""},
		{"stable", "v0.9.0", "v0.9.1", ""},
		{"beta", "v0.9.0-nightly.13", "v0.9.0-nightly.13", ""},
	}
	for _, c := range cases {
		args := []string{"--tag", c.tag, "--digest", edgeDigest("e"), "--from", c.from, "--from-digest", edgeDigest("e"), "--channel", c.host, "--json"}
		err := releaseCheck(args, fetch, &bytes.Buffer{})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s host on %s, to %s: %v, want it to go ahead", c.host, c.from, c.tag, err)
		case c.want != "" && (err == nil || err.Error() != "refusing to upgrade: "+c.want):
			t.Errorf("%s host on %s, to %s: err = %v, want refusing to upgrade: %s", c.host, c.from, c.tag, err, c.want)
		}
	}
}

func TestVersionJSONCarriesTheBuild(t *testing.T) {
	var out bytes.Buffer
	if err := versionCmd([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var v struct{ Build, Hotfix *int }
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Build == nil || v.Hotfix == nil || *v.Build != 0 || *v.Hotfix != 0 {
		t.Fatalf("version --json = %s (%v), want build 0 and hotfix 0 in an unstamped binary", out.String(), err)
	}
}

// A running release pruned from GitHub still orders from the copy of its
// signed manifest the updater saved; a saved copy that does not verify is
// refused.
func TestReleaseCheckOrdersFromASavedManifest(t *testing.T) {
	edge := &selfupdate.Edge{Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("e"),
		StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
		MinUpgradeFrom: "v0.5.0", ManualSteps: []selfupdate.ManualStep{}}
	fetch := serveManifests(t, releaseKey,
		selfupdate.Manifest{Version: "v0.9.0-beta.1", Channel: "beta", Build: 300, Edge: edge},
		selfupdate.Manifest{Version: "v0.9.0-beta.6", Channel: "beta", Build: 440, Edge: edge})
	data, sig := signManifest(t, releaseKey, selfupdate.Manifest{Version: "v0.9.0-nightly.12", Channel: "nightly", Build: 430, Edge: edge})
	saved := filepath.Join(t.TempDir(), "v0.9.0-nightly.12.json")
	check := func(tag string) error {
		return releaseCheck([]string{"--tag", tag, "--digest", edgeDigest("e"), "--from", "v0.9.0-nightly.12",
			"--from-digest", edgeDigest("e"), "--from-manifest", saved, "--channel", "beta"}, fetch, &bytes.Buffer{})
	}
	for _, c := range []struct {
		data []byte
		tag  string
		want string
	}{
		{data, "v0.9.0-beta.6", ""},
		{data, "v0.9.0-beta.1", "refusing to upgrade: v0.9.0-beta.1 (build 300) is older than the running v0.9.0-nightly.12 (build 430)"},
		{bytes.Replace(data, []byte("430"), []byte("200"), 1), "v0.9.0-beta.1", "is not signed by a trusted release key"},
	} {
		if err := os.WriteFile(saved, c.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(saved+".sig", sig, 0o600); err != nil {
			t.Fatal(err)
		}
		err := check(c.tag)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("to %s: err = %v, want %q", c.tag, err, c.want)
		}
	}
	if err := os.WriteFile(saved, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := check("v0.9.0-beta.6"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(saved); err != nil {
		t.Fatal(err)
	}
	if err := check("v0.9.0-beta.6"); err == nil || !strings.Contains(err.Error(), "the running release: ") {
		t.Errorf("without the saved manifest: err = %v, want it refused", err)
	}
}

// With a build, the running release meets min_upgrade_from and crosses
// manual steps by build, not by version, so crossing channel labels cannot
// skip a step; a boundary it cannot order counts against it.
func TestReleaseCheckOrdersUpgradeBoundariesByBuild(t *testing.T) {
	edge := func(minFrom, since string) *selfupdate.Edge {
		e := &selfupdate.Edge{Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("e"),
			StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
			MinUpgradeFrom: minFrom, ManualSteps: []selfupdate.ManualStep{}}
		if since != "" {
			e.ManualSteps = []selfupdate.ManualStep{{ID: "rotate", Since: since, Summary: "rotate the key", DocURL: "https://example.com/rotate", Blocking: true}}
		}
		return e
	}
	release := func(tag, channel string, build int, e *selfupdate.Edge) selfupdate.Manifest {
		return selfupdate.Manifest{Version: tag, Channel: channel, Build: build, Edge: e}
	}
	none := edge("v0.5.0", "")
	trusted := serveManifests(t, releaseKey,
		release("v0.9.0-nightly.10", "nightly", 400, none),
		release("v0.9.0-beta.1", "beta", 405, none),
		release("v0.8.9-beta.3", "beta", 395, none),
		release("v0.8.8", "", 0, none),
		release("v0.9.0-beta.2", "beta", 412, edge("v0.5.0", "v0.9.0-beta.1")),
		release("v0.9.0-beta.3", "beta", 413, edge("v0.8.9-beta.3", "v0.8.9-beta.3")),
		release("v0.9.0-beta.4", "beta", 414, edge("v0.5.0", "v0.8.9-beta.4")),
		release("v0.9.0-beta.5", "beta", 415, edge("v0.9.0-beta.1", "")),
		release("v0.9.0-beta.6", "beta", 416, edge("v0.8.8", "v0.9.0-alpha.1")),
		release("v0.9.0-beta.7", "beta", 418, edge("v0.5.0", "")),
		release("v0.9.0", "stable", 417, edge("v0.5.0", "v0.9.0-nightly.12")),
		release("v0.9.0-nightly.11", "nightly", 401, edge("v0.9.0-beta.9", "v0.9.0-beta.9")),
	)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	untrusted := serveManifests(t, other, release("v0.8.9-beta.4", "beta", 390, none))
	fetch := func(ctx context.Context, tag string) (selfupdate.Manifest, []byte, []byte, error) {
		if tag == "v0.8.9-beta.4" {
			return untrusted(ctx, tag)
		}
		return trusted(ctx, tag)
	}
	for _, c := range []struct{ tag, want string }{
		{"v0.9.0-beta.2", "manual step rotate (since v0.9.0-beta.1) must be done first"},
		{"v0.9.0-beta.3", ""},
		{"v0.9.0-beta.4", "manual step rotate (since v0.8.9-beta.4) must be done first: rotate the key; see https://example.com/rotate (v0.9.0-nightly.10 cannot be ordered against v0.8.9-beta.4, so the step counts as needed: "},
		{"v0.9.0-beta.5", "v0.9.0-beta.5 upgrades only from v0.9.0-beta.1 or later; upgrade v0.9.0-nightly.10 to v0.9.0-beta.1 first"},
		{"v0.9.0-beta.6", ""},
		{"v0.9.0-beta.7", ""},
		{"v0.9.0-nightly.11", "v0.9.0-nightly.11 upgrades only from v0.9.0-beta.9 or later, and v0.9.0-nightly.10 cannot be ordered against it: release v0.9.0-beta.9 has no signed aether-release.json"},
		{"v0.9.0-nightly.11", "manual step rotate (since v0.9.0-beta.9) must be done first"},
		{"v0.9.0", "v0.9.0-nightly.12 is a nightly tag, which cannot be ordered by build"},
	} {
		var out bytes.Buffer
		err := releaseCheck([]string{"--tag", c.tag, "--digest", edgeDigest("e"), "--from", "v0.9.0-nightly.10",
			"--from-digest", edgeDigest("e"), "--channel", "nightly", "--json"}, fetch, &out)
		var hold *holdError
		switch {
		case c.want == "" && err != nil:
			t.Errorf("to %s: %v, want it to go ahead", c.tag, err)
		case c.want != "" && (!errors.As(err, &hold) || !strings.Contains(err.Error(), c.want)):
			t.Errorf("to %s: err = %v, want a hold with %q", c.tag, err, c.want)
		}
	}
}

// --keep-manifests saves exactly the bytes release-check verified: the
// running release's, and the new release's only when it is not refused.
func TestReleaseCheckKeepsTheManifestsItVerified(t *testing.T) {
	edge := &selfupdate.Edge{Image: "ghcr.io/3xdevops/aether-edge", ImageDigest: edgeDigest("e"),
		StoreSchema: 4, StoreCompatFloor: 3, Protocol: selfupdate.EdgeProtocol{Version: 2, MinVersion: 1},
		MinUpgradeFrom: "v0.5.0", ManualSteps: []selfupdate.ManualStep{}}
	fetch := serveManifests(t, releaseKey,
		selfupdate.Manifest{Version: "v0.9.0-beta.1", Channel: "beta", Build: 405, Edge: edge},
		selfupdate.Manifest{Version: "v0.9.0-beta.2", Channel: "beta", Build: 412, Edge: edge},
		selfupdate.Manifest{Version: "v0.8.9-beta.1", Channel: "beta", Build: 300, Edge: edge})
	dir := t.TempDir()
	check := func(digest string, extra ...string) error {
		return releaseCheck(append([]string{"--tag", "v0.9.0-beta.2", "--digest", digest, "--from", "v0.9.0-beta.1",
			"--from-digest", edgeDigest("e"), "--channel", "beta", "--keep-manifests", dir}, extra...), fetch, &bytes.Buffer{})
	}
	if err := check(edgeDigest("f")); err == nil {
		t.Fatal("want another digest refused")
	}
	if names, _ := filepath.Glob(filepath.Join(dir, "*")); len(names) != 0 {
		t.Fatalf("kept %v for a refused release", names)
	}
	err := releaseCheck([]string{"--tag", "v0.8.9-beta.1", "--digest", edgeDigest("e"), "--from", "v0.9.0-beta.1",
		"--from-digest", edgeDigest("e"), "--channel", "beta", "--keep-manifests", dir}, fetch, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "is older than the running") {
		t.Fatalf("err = %v, want the older release refused", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "v0.8.9-beta.1.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("kept the refused v0.8.9-beta.1: %v", err)
	}
	if _, err = readManifest(filepath.Join(dir, "v0.9.0-beta.1.json"), "v0.9.0-beta.1"); err != nil {
		t.Errorf("the running release is not kept: %v", err)
	}
	if err := check(edgeDigest("e")); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v0.9.0-beta.1", "v0.9.0-beta.2"} {
		if _, err := readManifest(filepath.Join(dir, tag+".json"), tag); err != nil {
			t.Errorf("kept %s: %v", tag, err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "v0.9.0-beta.2.json")); err != nil {
		t.Fatal(err)
	}
	if err := check(edgeDigest("e"), "--from-manifest", filepath.Join(dir, "v0.9.0-beta.1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(filepath.Join(dir, "v0.9.0-beta.2.json"), "v0.9.0-beta.2"); err != nil {
		t.Errorf("the new release is not kept again: %v", err)
	}
}
