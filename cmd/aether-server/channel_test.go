package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// `aether-server update --channel` prints the confirmation with the real
// versions and builds and saves nothing without --yes; with it, it saves
// the channel and installs nothing. The host's update then follows the
// saved channel and waits on a build above it.
func TestUpdateChannelOnTheHost(t *testing.T) {
	oldVersion, oldBuild := version.Version, version.Build
	version.Version, version.Build = "v0.9.0-nightly.12", "412"
	t.Cleanup(func() { version.Version, version.Build = oldVersion, oldBuild })
	rel := releasetest.Serve(t, "v0.8.7-beta.14", map[string][]byte{"aether-linux-amd64": []byte("binary")})
	rel.SetBuild("v0.9.0-nightly.12", selfupdate.ChannelNightly, 412, 0)
	rel.SetBuild("v0.9.0-beta.1", selfupdate.ChannelBeta, 380, 0)
	for _, p := range []selfupdate.Pointer{
		{Channel: selfupdate.ChannelNightly, Tag: "v0.9.0-nightly.12", Build: 412},
		{Channel: selfupdate.ChannelBeta, Tag: "v0.9.0-beta.1", Build: 380},
	} {
		p.Schema, p.Commit, p.StoreSchema = selfupdate.PointerSchema, "0123456789abcdef0123456789abcdef01234567", store.SchemaVersion()
		p.PublishedAt = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		key := releasetest.Key
		if p.Channel == selfupdate.ChannelNightly {
			key = releasetest.NightlyKey
		}
		data, sig := releasetest.SignPointer(t, p, key)
		rel.SetPointer(p.Channel, data, sig)
	}
	stateDir, database := t.TempDir(), filepath.Join(t.TempDir(), "aether.db")
	db, openErr := store.Open(database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := db.SetReleaseChannel(t.Context(), selfupdate.ChannelNightly); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	channel := func() string {
		f, err := store.OpenChannelFile(database)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close() //nolint:errcheck // read-only
		ch, err := f.ReleaseChannel(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}

	var out bytes.Buffer
	err := setChannel(t.Context(), &out, rel.URL, stateDir, database, selfupdate.ChannelBeta, false)
	const want = "Switch to beta?\n\nThis server runs v0.9.0-nightly.12 (build 412). Beta is at v0.9.0-beta.1 (build 380). " +
		"The server stays on its current build until beta reaches build 412, then updates as usual. It never installs an older build.\n"
	if err == nil || !strings.Contains(err.Error(), "--yes") || out.String() != want || channel() != selfupdate.ChannelNightly {
		t.Fatalf("without --yes: %v, channel %s, printed\n%s\nwant\n%s", err, channel(), out.String(), want)
	}
	out.Reset()
	if err = setChannel(t.Context(), &out, rel.URL, stateDir, database, selfupdate.ChannelBeta, true); err != nil || channel() != selfupdate.ChannelBeta {
		t.Fatalf("with --yes: %v, channel %s", err, channel())
	}

	out.Reset()
	target, err := updateTarget(t.Context(), &out, rel.URL, stateDir, database, "")
	if err != nil || target != version.Version {
		t.Fatalf("update target = %q, %v; want the running version", target, err)
	}
	if got := out.String(); got != "aether-server v0.9.0-nightly.12 (build 412) stays on its build until beta reaches it: beta is at v0.9.0-beta.1 (build 380)\n" {
		t.Fatalf("update printed %q", got)
	}

	// A plain update only reads: a server that has not chosen its channel
	// is followed as beta, and the choice is left to the server.
	unchosen := filepath.Join(t.TempDir(), "aether.db")
	fresh, openErr := store.Open(unchosen)
	if openErr != nil {
		t.Fatal(openErr)
	}
	_ = fresh.Close()
	out.Reset()
	if target, err = updateTarget(t.Context(), &out, rel.URL, stateDir, unchosen, ""); err != nil || target != version.Version || !strings.Contains(out.String(), "until beta reaches it") {
		t.Fatalf("update on an unchosen server = %q, %v, printed %q", target, err, out.String())
	}
	f, openErr := store.OpenChannelFile(unchosen)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if ch, _ := f.ReleaseChannel(t.Context()); ch != "" {
		t.Fatalf("a plain update wrote channel %q", ch)
	}
	_ = f.Close()

	out.Reset()
	if err = setChannel(t.Context(), &out, rel.URL, stateDir, database, selfupdate.ChannelNightly, false); err == nil || !strings.HasPrefix(out.String(), "Switch to nightly builds?\n\nNot recommended unless you are developing Cloudfield.") {
		t.Fatalf("nightly: %v, printed %q", err, out.String())
	}
}
