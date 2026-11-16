package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/version"
)

func TestNewerOrdersByBuildThenSemver(t *testing.T) {
	nightly10 := Release{Version: "v0.9.0-nightly.10", Channel: ChannelNightly, Build: 400}
	beta2 := Release{Version: "v0.9.0-beta.2", Channel: ChannelBeta, Build: 412}
	if !Behind(beta2.Version, nightly10.Version) {
		t.Fatal("semver no longer ranks beta.2 below nightly.10; this test no longer shows what build order fixes")
	}
	for _, tc := range []struct {
		name string
		a, b Release
		want bool
	}{
		{"beta from a later nightly over an earlier nightly", beta2, nightly10, true},
		{"earlier nightly under a later beta", nightly10, beta2, false},
		{"same code under another label", Release{Version: "v0.9.0-nightly.12", Build: 412}, beta2, false},
		{"hotfix over its stable", Release{Version: "v0.9.1", Build: 412, Hotfix: 1}, Release{Version: "v0.9.0", Build: 412}, true},
		{"later nightly over a hotfix", Release{Version: "v0.10.0-nightly.1", Build: 413}, Release{Version: "v0.9.1", Build: 412, Hotfix: 1}, true},
		{"any build over a release from before channels", Release{Version: "v0.8.7", Build: 1}, Release{Version: "v0.8.7-beta.14"}, true},
		{"before channels, semver decides", Release{Version: "v0.8.7-beta.14"}, Release{Version: "v0.8.7-beta.13"}, true},
		{"before channels, a build past a tag", Release{Version: "v0.8.7-beta.14"}, Release{Version: "v0.8.7-beta.14-3-gabc1234"}, false},
		{"a dev build is never behind", Release{Version: "v0.9.0", Build: 412}, Release{Version: "dev"}, false},
		{"nor ahead", Release{Version: "dev"}, Release{Version: "v0.9.0", Build: 412}, false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: Newer(%+v, %+v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func channelManifest(tag, channel string, build, hotfix int) Manifest {
	m := manifestFor(tag, testAssets)
	m.Channel, m.Build, m.Hotfix = channel, build, hotfix
	return m
}

func TestVerifyManifestRefusesAChannelThatDisagreesWithItsTag(t *testing.T) {
	for _, tc := range []struct {
		m    Manifest
		want string
	}{
		{channelManifest("v0.9.0", ChannelBeta, 412, 0), `channel "beta" does not match v0.9.0, a stable tag`},
		{channelManifest("v0.9.0-beta.2", ChannelStable, 412, 0), `channel "stable" does not match v0.9.0-beta.2, a beta tag`},
		{channelManifest("v0.9.0-beta.2", ChannelNightly, 412, 0), `channel "nightly" does not match v0.9.0-beta.2, a beta tag`},
		{channelManifest("v0.9.0-nightly.10", ChannelBeta, 400, 0), `channel "beta" does not match v0.9.0-nightly.10, a nightly tag`},
		{channelManifest("v0.9.0-alpha.1", ChannelBeta, 400, 0), `channel "beta" does not match v0.9.0-alpha.1, a tag of no channel`},
		{channelManifest("v0.9.0", "", 412, 0), "build 412 and hotfix 0 without a channel"},
		{channelManifest("v0.9.0", ChannelStable, 0, 0), "the build must be positive"},
		{channelManifest("v0.9.0", "edge", 412, 0), `channel "edge" does not match`},
	} {
		data, sig := signWith(t, tc.m, testKey)
		if _, err := VerifyManifest(data, sig, tc.m.Version); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s on %q: err = %v, want %q", tc.m.Version, tc.m.Channel, err, tc.want)
		}
	}
	for _, m := range []Manifest{
		channelManifest("v0.9.0-nightly.10", ChannelNightly, 400, 0),
		channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0),
		channelManifest("v0.9.1", ChannelStable, 412, 1),
		manifestFor("v0.8.7-beta.14", testAssets),
	} {
		data, sig := signWith(t, m, testKey)
		got, err := VerifyManifest(data, sig, m.Version)
		if err != nil {
			t.Errorf("%s: %v", m.Version, err)
		} else if got.Release() != m.Release() {
			t.Errorf("%s: release %+v, want %+v", m.Version, got.Release(), m.Release())
		}
	}
}

// A leaked nightly key cannot publish a beta or stable release, nor one
// that claims to predate channels.
func TestANightlyKeyVerifiesOnlyNightlies(t *testing.T) {
	nightly := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	signed := channelManifest("v0.9.0-nightly.10", ChannelNightly, 400, 0)
	data, sig := signWith(t, signed, nightly)
	if _, err := VerifyManifest(data, sig, signed.Version); err == nil || !strings.Contains(err.Error(), "not signed by a trusted release key") {
		t.Fatalf("an unknown nightly key: err = %v, want an untrusted signature", err)
	}

	old := nightlyKeys
	nightlyKeys = []ed25519.PublicKey{nightly.Public().(ed25519.PublicKey)}
	t.Cleanup(func() { nightlyKeys = old })
	if _, err := VerifyManifest(data, sig, signed.Version); err != nil {
		t.Fatalf("a nightly signed by a nightly key: %v", err)
	}
	for _, m := range []Manifest{
		channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0),
		channelManifest("v0.9.0", ChannelStable, 412, 0),
		manifestFor("v0.9.0", testAssets),
	} {
		forged, forgedSig := signWith(t, m, nightly)
		if _, err := VerifyManifest(forged, forgedSig, m.Version); err == nil || !strings.Contains(err.Error(), "signed by a nightly key") {
			t.Errorf("%s on %q signed by a nightly key: err = %v, want it refused", m.Version, m.Channel, err)
		}
	}
	m := channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0)
	data, sig = signWith(t, m, testKey)
	if _, err := VerifyManifest(data, sig, m.Version); err != nil {
		t.Fatalf("a beta signed by the release key: %v", err)
	}
}

func TestAcceptedOnFollowsTheChannel(t *testing.T) {
	releases := map[string]Manifest{
		"nightly":     channelManifest("v0.9.0-nightly.10", ChannelNightly, 400, 0),
		"beta":        channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0),
		"stable":      channelManifest("v0.9.0", ChannelStable, 412, 0),
		"pre-channel": manifestFor("v0.8.7-beta.14", testAssets),
	}
	for channel, accepts := range map[string][]string{
		ChannelStable:  {"stable"},
		ChannelBeta:    {"beta", "stable", "pre-channel"},
		ChannelNightly: {"nightly", "beta", "stable", "pre-channel"},
	} {
		for name, m := range releases {
			want := false
			for _, a := range accepts {
				want = want || a == name
			}
			if err := m.AcceptedOn(channel); (err == nil) != want {
				t.Errorf("a %s release on %s: err = %v, want accepted %v", name, channel, err, want)
			}
		}
	}
	if err := releases["stable"].AcceptedOn(""); err == nil {
		t.Error("an install without a channel accepted a release")
	}
}

// A manifest field this build does not know is ignored, as today's
// verifier ignores channel and build: the signature covers the bytes, not
// the fields.
func TestVerifyManifestIgnoresUnknownFields(t *testing.T) {
	data, _ := signWith(t, channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0), testKey)
	data = bytes.Replace(data, []byte(`"schema": 1,`), []byte(`"schema": 1, "pointer": {"tag": "v0.9.0-beta.2"},`), 1)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(testKey, data)))
	if _, err := VerifyManifest(data, sig, "v0.9.0-beta.2"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckOffersABetaPromotedFromALaterNightly(t *testing.T) {
	setVersion(t, "v0.9.0-nightly.10", "abc1234")
	oldBuild := version.Build
	version.Build = "400"
	t.Cleanup(func() { version.Build = oldBuild })
	f, srv := newFakeRelease(t, "v0.9.0-beta.2", testAssets)
	f.builds = map[string]Release{"v0.9.0-beta.2": {Channel: ChannelBeta, Build: 412}}
	got, err := NewChecker(srv.URL, defaultTTL).CheckFresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Latest != "v0.9.0-beta.2" || !got.UpdateAvailable {
		t.Fatalf("check = %+v, want v0.9.0-beta.2 (build 412) offered over v0.9.0-nightly.10 (build 400)", got)
	}
}

func TestTagChannelTakesOnlyTheExactForms(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.9.0":                       ChannelStable,
		"v0.9.0-beta.2":                ChannelBeta,
		"v0.9.0-nightly.10":            ChannelNightly,
		"v1.0.0-rc.1-beta.2":           "",
		"v1.0.0-beta.1-nightly.2":      "",
		"v0.9.0-beta.2.1":              "",
		"v0.9.0-beta.0":                "",
		"v0.9.0-beta":                  "",
		"v0.9.0-alpha.1":               "",
		"v0.9.0-nightly.10-3-gabc1234": "",
	} {
		if got := TagChannel(tag); got != want {
			t.Errorf("TagChannel(%q) = %q, want %q", tag, got, want)
		}
	}
	m := channelManifest("v1.0.0-rc.1-beta.2", ChannelBeta, 412, 0)
	data, sig := signWith(t, m, testKey)
	if _, err := VerifyManifest(data, sig, m.Version); err == nil || !strings.Contains(err.Error(), "a tag of no channel") {
		t.Fatalf("beta on %s: err = %v, want it refused", m.Version, err)
	}
}

// Shell tooling reading the same signed bytes with jq must never see
// another channel than the Go verifier does.
func TestVerifyManifestRefusesRepeatedOrMiscasedKeys(t *testing.T) {
	base, _ := signWith(t, channelManifest("v0.9.0-beta.2", ChannelBeta, 412, 0), testKey)
	for insert, want := range map[string]string{
		`"Channel": "nightly",`: `key "Channel" is not spelled "channel"`,
		`"channel": "nightly",`: `key "channel" repeats`,
		`"BUILD": 999,`:         `key "BUILD" is not spelled "build"`,
	} {
		data := bytes.Replace(base, []byte(`"schema": 1,`), []byte(`"schema": 1, `+insert), 1)
		sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(testKey, data)))
		if _, err := VerifyManifest(data, sig, "v0.9.0-beta.2"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("with %s: err = %v, want %q", insert, err, want)
		}
	}
}

func TestRunningRefusesABuildThatIsNotANumber(t *testing.T) {
	for _, bad := range []string{"", "4x", "-1", "99999999999999999999"} {
		old := version.Build
		version.Build = bad
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("version.Build=%q: Running did not panic", bad)
				}
			}()
			Running()
		}()
		version.Build = old
	}
}

func TestRefuseDowngradeReadsTheSignedBuild(t *testing.T) {
	setVersion(t, "v0.9.0-beta.2", "abc1234")
	oldBuild := version.Build
	version.Build = "412"
	t.Cleanup(func() { version.Build = oldBuild })
	f, srv := newFakeRelease(t, "v0.9.0-beta.2", testAssets)
	f.builds = map[string]Release{
		"v0.9.0-nightly.10": {Channel: ChannelNightly, Build: 400},
		"v0.9.0-nightly.13": {Channel: ChannelNightly, Build: 413},
	}
	if err := RefuseDowngrade(t.Context(), srv.URL, "v0.9.0-nightly.10"); err == nil || !strings.Contains(err.Error(), "v0.9.0-nightly.10 (build 400) is older than the running v0.9.0-beta.2 (build 412)") {
		t.Fatalf("err = %v, want the lower build refused", err)
	}
	if err := RefuseDowngrade(t.Context(), srv.URL, "v0.9.0-nightly.13"); err != nil {
		t.Fatalf("a higher build: %v", err)
	}
}
