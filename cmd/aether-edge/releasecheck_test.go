package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

var releaseKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

func edgeDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// serveRelease publishes v1.5.0, from before release channels, with edge
// as its signed edge section, signed by key, and returns a fetch that
// reads it the way release-check does.
func serveRelease(t *testing.T, edge *selfupdate.Edge, key ed25519.PrivateKey) func(context.Context, string) (selfupdate.Manifest, error) {
	t.Helper()
	return serveManifests(t, key, selfupdate.Manifest{Version: "v1.5.0", Edge: edge})
}

// serveManifests publishes each manifest under its version, signed by key.
func serveManifests(t *testing.T, key ed25519.PrivateKey, ms ...selfupdate.Manifest) func(context.Context, string) (selfupdate.Manifest, error) {
	t.Helper()
	t.Cleanup(selfupdate.TrustKeysForTest(releaseKey.Public().(ed25519.PublicKey)))
	mux := http.NewServeMux()
	for _, m := range ms {
		m.Schema, m.Commit, m.PublishedAt, m.StoreSchema = selfupdate.ManifestSchema, "c0ffee", time.Now(), 1
		m.Assets = map[string]selfupdate.Asset{"aether-edge-linux-amd64": {SHA256: strings.Repeat("0", 64), Size: 1}}
		data, sig, err := selfupdate.SignManifest(m, key)
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("/releases/download/"+m.Version+"/"+selfupdate.ManifestName, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
		mux.HandleFunc("/releases/download/"+m.Version+"/"+selfupdate.SignatureName, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return func(ctx context.Context, name string) (selfupdate.Manifest, error) {
		return selfupdate.FetchManifestFrom(ctx, srv.URL, name)
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
		err := releaseCheck([]string{"--tag", "v1.5.0", "--digest", edgeDigest("e"), "--from", c.from, "--from-build", "0", "--channel", "beta", "--json"}, fetch, &out)
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
	if err := releaseCheck([]string{"--tag", "v1.5.0", "--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-build", "0", "--channel", "beta"}, fetch, &out); err == nil {
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
		fetch func(context.Context, string) (selfupdate.Manifest, error)
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
		{"no --from-build", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--channel", "beta"}, "--from-build <n>"},
		{"no --channel", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-build", "0"}, "--channel <nightly|beta|stable|latest>"},
		{"another channel name", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.4.0", "--from-build", "0", "--channel", "edge"}, `--channel "edge" is not nightly, beta, stable or latest`},
		{"an older release", serveRelease(t, testEdge(), releaseKey),
			[]string{"--digest", edgeDigest("e"), "--from", "v1.6.0", "--from-build", "0", "--channel", "beta"}, "refusing to upgrade: v1.5.0 is older than the running v1.6.0"},
		{"a step newer than the release", serveRelease(t, func() *selfupdate.Edge {
			e := testEdge()
			e.ManualSteps = append(e.ManualSteps, selfupdate.ManualStep{ID: "later", Since: "v1.6.0", Summary: "s", DocURL: "https://example.com/later", Blocking: true})
			return e
		}(), releaseKey), []string{"--digest", edgeDigest("e"), "--from", "v1.4.0"}, `since "v1.6.0" is not a release tag at or below v1.5.0`},
	}
	for _, c := range cases {
		args := append([]string{"--tag", "v1.5.0"}, c.args...)
		if !slices.Contains(args, "--from-build") && !slices.Contains(args, "--channel") {
			args = append(args, "--from-build", "0", "--channel", "beta")
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
		release("v0.9.0-beta.2", "beta", 412, 0),
		release("v0.9.0", "stable", 412, 0),
		release("v0.9.1", "stable", 412, 1),
	)
	cases := []struct {
		host, from string
		fromBuild  int
		fromHotfix int
		tag, want  string
	}{
		{"beta", "v0.9.0-beta.2", 412, 0, "v0.9.0-nightly.13", "v0.9.0-nightly.13 is a nightly release, which an install on beta does not take"},
		{"stable", "v0.8.7", 1, 0, "v0.9.0-beta.2", "v0.9.0-beta.2 is a beta release, which an install on stable does not take"},
		{"stable", "v0.8.6", 0, 0, "v0.8.7-beta.14", "v0.8.7-beta.14 is a no channel release, which an install on stable does not take"},
		{"nightly", "v0.9.0-beta.2", 412, 0, "v0.9.0-nightly.10", "v0.9.0-nightly.10 (build 400) is older than the running v0.9.0-beta.2 (build 412)"},
		{"latest", "v0.9.0-nightly.13", 420, 0, "v0.9.0", "v0.9.0 (build 412) is older than the running v0.9.0-nightly.13 (build 420)"},
		{"beta", "v0.8.7-beta.15", 0, 0, "v0.8.7-beta.14", "v0.8.7-beta.14 is older than the running v0.8.7-beta.15"},
		{"stable", "v0.9.1", 412, 1, "v0.9.0", "v0.9.0 (build 412) is older than the running v0.9.1 (build 412, hotfix 1)"},
		{"beta", "v0.9.0-nightly.10", 400, 0, "v0.9.0-beta.2", ""},
		{"stable", "v0.9.0-beta.2", 412, 0, "v0.9.0", ""},
		{"nightly", "v0.9.0-beta.2", 412, 0, "v0.9.0-nightly.13", ""},
		{"latest", "v0.9.0", 412, 0, "v0.9.0-nightly.13", "v0.9.0-nightly.13 is a nightly release, which an install on latest does not take"},
		{"latest", "v0.8.7-beta.14", 0, 0, "v0.9.0-beta.2", ""},
		{"beta", "v0.8.7-beta.13", 0, 0, "v0.8.7-beta.14", ""},
		{"beta", "v0.8.7-beta.14", 0, 0, "v0.8.7", ""},
		{"stable", "v0.9.0", 412, 0, "v0.9.1", ""},
		{"beta", "v0.9.0-nightly.13", 420, 0, "v0.9.0-nightly.13", ""},
	}
	for _, c := range cases {
		args := []string{"--tag", c.tag, "--digest", edgeDigest("e"), "--from", c.from,
			"--from-build", strconv.Itoa(c.fromBuild), "--from-hotfix", strconv.Itoa(c.fromHotfix), "--channel", c.host, "--json"}
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
