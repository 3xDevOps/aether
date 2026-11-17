package serverupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/selfupdate/releasetest"
	"github.com/3xDevOps/cloudfield/internal/store"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

const pointerCommit = "0123456789abcdef0123456789abcdef01234567"

// channelHost is a release host publishing a nightly, a beta and a stable,
// with no pointer yet.
func channelHost(t *testing.T) *releasetest.Release {
	t.Helper()
	rel := releasetest.Serve(t, "v0.9.0", map[string][]byte{"aether-linux-amd64": []byte("binary")})
	rel.SetBuild("v0.9.0-nightly.12", selfupdate.ChannelNightly, 412, 0)
	rel.SetBuild("v0.9.0-beta.1", selfupdate.ChannelBeta, 380, 0)
	rel.SetBuild("v0.9.0-beta.2", selfupdate.ChannelBeta, 412, 0)
	rel.SetBuild("v0.9.0", selfupdate.ChannelStable, 412, 0)
	return rel
}

func pointer(channel, tag string, build int) selfupdate.Pointer {
	return selfupdate.Pointer{
		Schema:      selfupdate.PointerSchema,
		Channel:     channel,
		Tag:         tag,
		Commit:      pointerCommit,
		Build:       build,
		StoreSchema: store.SchemaVersion(),
		PublishedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
}

func publishPointer(t *testing.T, rel *releasetest.Release, at string, p selfupdate.Pointer, key ed25519.PrivateKey) {
	t.Helper()
	data, sig := releasetest.SignPointer(t, p, key)
	rel.SetPointer(at, data, sig)
}

// publishEdited publishes p as beta.json with insert added after its
// schema, signed by the release key.
func publishEdited(t *testing.T, rel *releasetest.Release, p selfupdate.Pointer, insert string) {
	t.Helper()
	data, _ := releasetest.SignPointer(t, p, releasetest.Key)
	data = bytes.Replace(data, []byte(`"schema": 1,`), []byte(`"schema": 1, `+insert), 1)
	rel.SetPointer("beta", data, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(releasetest.Key, data))))
}

// channelService is a server following channel against baseURL, keeping
// its state in dir.
func channelService(t *testing.T, dir, baseURL, channel string) *Service {
	t.Helper()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.SetReleaseChannel(t.Context(), channel); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{
		Store: db, Bus: newTestBus(t), StateDir: dir, Now: time.Now,
		Checker: selfupdate.NewChecker(baseURL, time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// After accepting build 412 from beta.json, a validly signed older pointer
// is refused, the server offers nothing from it, and the mark holds across
// a restart.
func TestChannelPointerReplayIsRefusedAcrossARestart(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	if st := channelService(t, dir, rel.URL, "beta").runCheck(t.Context()); st.Error != "" || st.LatestVersion != "v0.9.0-beta.2" {
		t.Fatalf("check = %+v, want v0.9.0-beta.2", st)
	}

	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.1", 380), releasetest.Key)
	svc := channelService(t, dir, rel.URL, "beta")
	const want = "channel pointer beta.json names build 380, below build 412 seen before"
	st := svc.runCheck(t.Context())
	if st.Error != want || st.Failures != 1 || st.LatestVersion != "v0.9.0-beta.2" {
		t.Fatalf("check after replay = %+v, want the failure %q and v0.9.0-beta.2 kept", st, want)
	}
	if _, err := svc.resolveTag(t.Context(), ""); err == nil || err.Error() != want {
		t.Fatalf("update with no version = %v, want %q", err, want)
	}
	if n := rel.Requests("/releases/download/v0.9.0-beta.1/" + selfupdate.ManifestName); n != 0 {
		t.Fatalf("fetched the replayed pointer's manifest %d times", n)
	}
}

// Once a pointer verified, a missing one is a failed check and the server
// never asks /releases/latest; before that, it does.
func TestChannelPointerNeverFallsBackToLatestOnceVerified(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check.json"), []byte(`{"latest_version":"v0.8.7-beta.14","failures":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := channelService(t, dir, rel.URL, "beta")
	if st := svc.runCheck(t.Context()); st.Error != "" || st.LatestVersion != "v0.9.0" || rel.Requests("/releases/latest") != 1 {
		t.Fatalf("check without a pointer = %+v after %d Latest requests, want Latest's v0.9.0", st, rel.Requests("/releases/latest"))
	}

	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	if st := svc.runCheck(t.Context()); st.Error != "" || st.LatestVersion != "v0.9.0-beta.2" {
		t.Fatalf("check = %+v, want v0.9.0-beta.2", st)
	}
	rel.SetPointer("beta", nil, nil)
	st := channelService(t, dir, rel.URL, "beta").runCheck(t.Context())
	if !strings.Contains(st.Error, "channel pointer beta.json: GET ") || !strings.Contains(st.Error, "404") || st.Failures != 1 {
		t.Fatalf("check with the pointer gone = %+v, want a failed check", st)
	}
	if n := rel.Requests("/releases/latest"); n != 1 {
		t.Fatalf("asked /releases/latest %d times, want only the check before the pointer", n)
	}
}

// A channel's newest release comes only from a pointer signed for that
// channel by a key allowed to sign it, naming a manifest that agrees with
// it and that the channel accepts.
func TestChannelPointerIsVerified(t *testing.T) {
	nightly12 := pointer("nightly", "v0.9.0-nightly.12", 412)
	for _, tc := range []struct {
		name    string
		channel string
		publish func(*testing.T, *releasetest.Release)
		want    string // the accepted tag, or a substring of the error
	}{
		{"beta", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
		}, "v0.9.0-beta.2"},
		{"beta names a stable", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0", 412), releasetest.Key)
		}, "v0.9.0"},
		{"nightly.json served as beta.json", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", nightly12, releasetest.Key)
		}, `channel pointer beta.json is signed for channel "nightly"`},
		{"stable names a beta", "stable", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "stable", pointer("stable", "v0.9.0-beta.2", 412), releasetest.Key)
		}, "stable.json: v0.9.0-beta.2 is a beta release, which an install on stable does not take"},
		{"version mismatch", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.3", 412), releasetest.Key)
		}, "fetched for v0.9.0-beta.3 describes v0.9.0-beta.2"},
		{"commit mismatch", "beta", func(t *testing.T, rel *releasetest.Release) {
			p := pointer("beta", "v0.9.0-beta.2", 412)
			p.Commit = strings.Repeat("f", 40)
			publishPointer(t, rel, "beta", p, releasetest.Key)
		}, "but its manifest says commit " + pointerCommit},
		{"build mismatch", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 413), releasetest.Key)
		}, "names v0.9.0-beta.2 at build 413, but its manifest says build 412"},
		{"hotfix mismatch", "stable", func(t *testing.T, rel *releasetest.Release) {
			p := pointer("stable", "v0.9.0", 412)
			p.Hotfix = 1
			publishPointer(t, rel, "stable", p, releasetest.Key)
		}, "names v0.9.0 at build 412 hotfix 1, but its manifest says build 412"},
		{"nightly key on beta.json", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.NightlyKey)
		}, "signed by a nightly key but its channel is \"beta\""},
		{"nightly key on stable.json", "stable", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), releasetest.NightlyKey)
		}, "signed by a nightly key but its channel is \"stable\""},
		{"nightly key on nightly.json", "nightly", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "nightly", nightly12, releasetest.NightlyKey)
		}, "v0.9.0-nightly.12"},
		{"nightly key steering nightly to a beta", "nightly", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "nightly", pointer("nightly", "v0.9.0-beta.2", 412), releasetest.NightlyKey)
		}, "signed by a nightly key but names v0.9.0-beta.2, a beta release"},
		{"release key steering nightly to a beta", "nightly", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "nightly", pointer("nightly", "v0.9.0-beta.2", 412), releasetest.Key)
		}, "v0.9.0-beta.2"},
		{"untrusted key", "beta", func(t *testing.T, rel *releasetest.Release) {
			other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
			publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), other)
		}, "not signed by a trusted release key"},
		{"unknown schema", "beta", func(t *testing.T, rel *releasetest.Release) {
			p := pointer("beta", "v0.9.0-beta.2", 412)
			p.Schema = 2
			publishPointer(t, rel, "beta", p, releasetest.Key)
		}, "has schema 2; this aether reads schema 1"},
		{"store schema mismatch", "beta", func(t *testing.T, rel *releasetest.Release) {
			p := pointer("beta", "v0.9.0-beta.2", 412)
			p.StoreSchema++
			publishPointer(t, rel, "beta", p, releasetest.Key)
		}, "but its manifest says store schema"},
		{"repeated key", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishEdited(t, rel, pointer("beta", "v0.9.0-beta.2", 412), `"build": 999999,`)
		}, `key "build" repeats`},
		{"miscased key", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishEdited(t, rel, pointer("beta", "v0.9.0-beta.2", 412), `"Build": 5,`)
		}, `key "Build" is not spelled "build"`},
		{"malformed commit", "beta", func(t *testing.T, rel *releasetest.Release) {
			p := pointer("beta", "v0.9.0-beta.2", 412)
			p.Commit = "abc1234"
			publishPointer(t, rel, "beta", p, releasetest.Key)
		}, `commit "abc1234" is not 40 lowercase hex digits`},
		{"pre-channel release on beta", "beta", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "beta", pointer("beta", "v0.8.7-beta.14", 0), releasetest.Key)
		}, "v0.8.7-beta.14"},
		{"pre-channel release on stable", "stable", func(t *testing.T, rel *releasetest.Release) {
			publishPointer(t, rel, "stable", pointer("stable", "v0.8.7-beta.14", 0), releasetest.Key)
		}, "build 0 with hotfix 0 on stable; only beta names a release from before channels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := channelHost(t)
			tc.publish(t, rel)
			// The version mismatch serves beta.2's manifest under beta.3.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.URL.Path = strings.Replace(r.URL.Path, "/v0.9.0-beta.3/", "/v0.9.0-beta.2/", 1)
				rel.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			st := channelService(t, t.TempDir(), srv.URL, tc.channel).runCheck(t.Context())
			if strings.HasPrefix(tc.want, "v") {
				if _, marked := st.Pointers[tc.channel]; st.Error != "" || st.LatestVersion != tc.want || !marked {
					t.Fatalf("check = %+v, want %s accepted", st, tc.want)
				}
				return
			}
			if !strings.Contains(st.Error, tc.want) || st.LatestVersion != "" || len(st.Pointers) != 0 {
				t.Fatalf("check = %+v, want refused with %q", st, tc.want)
			}
		})
	}
}

// A pointer at the same build with a lower hotfix is a replay too.
func TestChannelPointerBelowAHotfixIsRefused(t *testing.T) {
	rel := channelHost(t)
	rel.SetBuild("v0.9.1", selfupdate.ChannelStable, 412, 1)
	hotfix := pointer("stable", "v0.9.1", 412)
	hotfix.Hotfix = 1
	publishPointer(t, rel, "stable", hotfix, releasetest.Key)
	svc := channelService(t, t.TempDir(), rel.URL, "stable")
	if st := svc.runCheck(t.Context()); st.Error != "" || st.LatestVersion != "v0.9.1" {
		t.Fatalf("check = %+v, want v0.9.1", st)
	}
	publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), releasetest.Key)
	const want = "channel pointer stable.json names build 412, below build 412 hotfix 1 seen before"
	if st := svc.runCheck(t.Context()); st.Error != want {
		t.Fatalf("check = %+v, want %q", st, want)
	}
}

// A check.json that cannot be parsed fails every channel check and is
// never overwritten, so its marks are not silently dropped.
func TestChannelCheckFailsClosedOnAnUnreadableState(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	if st := channelService(t, dir, rel.URL, "beta").runCheck(t.Context()); st.Error != "" {
		t.Fatalf("check = %+v", st)
	}
	path := filepath.Join(dir, "check.json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := good[:len(good)/2]
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.1", 380), releasetest.Key)
	svc := channelService(t, dir, rel.URL, "beta")
	st := svc.runCheck(t.Context())
	if !strings.Contains(st.Error, path) || !strings.Contains(st.Error, "remove it, which forgets the channel marks") || st.LatestVersion != "" {
		t.Fatalf("check = %+v, want it failed naming %s", st, path)
	}
	if _, err := svc.resolveTag(t.Context(), ""); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("update with no version = %v, want it failed naming %s", err, path)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, corrupt) {
		t.Fatalf("check.json was rewritten to %s", got)
	}
	if n := rel.Requests("/releases/download/channels/beta.json"); n != 1 {
		t.Fatalf("fetched beta.json %d times, want only before the corruption", n)
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := svc.runCheck(t.Context()); st.Error != "channel pointer beta.json names build 380, below build 412 seen before" {
		t.Fatalf("check after the file was fixed = %+v, want the restored mark to refuse the replay", st)
	}
}

// server.update with no version on a fresh server records the pointer it
// verified, so the Latest fallback closes there too.
func TestUpdateWithNoVersionRecordsTheChannelMark(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	svc := channelService(t, dir, rel.URL, "beta")
	_, _ = svc.resolveTag(t.Context(), "")
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.1", 380), releasetest.Key)
	if _, err := svc.resolveTag(t.Context(), ""); err == nil || err.Error() != "channel pointer beta.json names build 380, below build 412 seen before" {
		t.Fatalf("second update with no version after a replay = %v, want it refused", err)
	}
	rel.SetPointer("beta", nil, nil)
	st := channelService(t, dir, rel.URL, "beta").runCheck(t.Context())
	if !strings.Contains(st.Error, "404") || rel.Requests("/releases/latest") != 0 {
		t.Fatalf("check after the pointer went = %+v with %d Latest requests, want a failed check and none", st, rel.Requests("/releases/latest"))
	}
}

// A channel mark that cannot be written would be lost on restart, so the
// check that raised it fails and offers nothing.
func TestChannelCheckFailsWhenItsMarkCannotBeSaved(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	svc := channelService(t, dir, rel.URL, "beta")
	if err := os.Mkdir(filepath.Join(dir, "check.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	st := svc.runCheck(t.Context())
	if !strings.Contains(st.Error, "record the release check") || st.Failures != 1 || st.LatestVersion != "" {
		t.Fatalf("check = %+v, want it failed with nothing offered", st)
	}
	if _, err := svc.resolveTag(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "record the release check") || len(svc.check.Pointers) != 0 {
		t.Fatalf("update with no version = %v with marks %v, want the write failure and no mark in memory", err, svc.check.Pointers)
	}
}

// A write that fails when no mark moved is a warning, as on a server with
// no channel.
func TestChannelCheckWithAnUnmovedMarkSurvivesAFailedWrite(t *testing.T) {
	rel := channelHost(t)
	dir := t.TempDir()
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	svc := channelService(t, dir, rel.URL, "beta")
	if st := svc.runCheck(t.Context()); st.Error != "" {
		t.Fatalf("check = %+v", st)
	}
	path := filepath.Join(dir, "check.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := svc.runCheck(t.Context()); st.Error != "" || st.LatestVersion != "v0.9.0-beta.2" {
		t.Fatalf("check = %+v, want it to succeed", st)
	}
}

// server.update with no version never waits on a background check that
// hangs: on no channel it asks the feed itself, and on a channel it gives
// up when its context ends.
func TestUpdateWithNoVersionDoesNotWaitOnAHungCheck(t *testing.T) {
	feed := &firstCallHangs{gate: make(chan struct{}), entered: make(chan struct{})}
	svc := newCheckService(t, t.TempDir(), feed, time.Now)
	checked := make(chan struct{})
	go func() { svc.runCheck(context.Background()); close(checked) }()
	<-feed.entered
	// Registered after the state directory, so it runs first: the hung
	// check finishes writing check.json before the directory is removed.
	t.Cleanup(func() { close(feed.gate); <-checked })
	done := make(chan error, 1)
	go func() { _, err := svc.resolveTag(t.Context(), ""); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUpToDate) {
			t.Fatalf("update with no version = %v, want the feed's answer", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("update with no version waited on the hung check")
	}

	rel := channelHost(t)
	onChannel := channelService(t, t.TempDir(), rel.URL, "beta")
	onChannel.run <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := onChannel.resolveTag(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("update with no version behind a running check = %v, want its deadline", err)
	}
}

// firstCallHangs blocks its first Latest until gate closes and answers
// every later one at once.
type firstCallHangs struct {
	calls   atomic.Int32
	entered chan struct{}
	gate    chan struct{}
}

func (f *firstCallHangs) Latest(context.Context) (selfupdate.Manifest, error) {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		<-f.gate
	}
	return selfupdate.Manifest{}, selfupdate.ErrNoRelease
}

func (f *firstCallHangs) Fetch(context.Context, string) (selfupdate.Manifest, error) {
	return selfupdate.Manifest{}, errors.New("not used")
}
