package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f *fakeRelease) setPointer(t *testing.T, tag string, build int) {
	t.Helper()
	data, err := json.Marshal(Pointer{
		Schema: PointerSchema, Channel: ChannelBeta, Tag: tag,
		Commit: "0123456789abcdef0123456789abcdef01234567", Build: build, StoreSchema: 1,
		PublishedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(testKey, data)))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pointers = map[string][2][]byte{ChannelBeta: {data, sig}}
}

// A client checker on beta falls back to /releases/latest only until a
// pointer verifies, keeps the pointer's mark in its state file, and a new
// checker reading that file refuses an older pointer and a missing one.
func TestChannelCheckerKeepsItsMarkInTheStateFile(t *testing.T) {
	setVersion(t, "v0.9.0-beta.1", "abc1234")
	f, srv := newFakeRelease(t, "v0.9.0-beta.1", testAssets)
	f.builds = map[string]Release{
		"v0.9.0-beta.1": {Channel: ChannelBeta, Build: 380},
		"v0.9.0-beta.2": {Channel: ChannelBeta, Build: 412},
	}
	state := filepath.Join(t.TempDir(), "aether", "update.json")
	c := NewChannelChecker(srv.URL, time.Hour, ChannelBeta, state)

	if got, err := c.CheckFresh(t.Context()); err != nil || got.Latest != "v0.9.0-beta.1" || f.hits.Load() != 1 {
		t.Fatalf("check without a pointer = %+v, %v; want Latest's v0.9.0-beta.1", got, err)
	}
	f.setPointer(t, "v0.9.0-beta.2", 412)
	if got, err := c.CheckFresh(t.Context()); err != nil || got.Latest != "v0.9.0-beta.2" || !got.UpdateAvailable {
		t.Fatalf("check = %+v, %v; want v0.9.0-beta.2 offered", got, err)
	}
	data, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(data), `"beta": {`) || !strings.Contains(string(data), `"build": 412`) {
		t.Fatalf("state file = %s, %v; want beta's mark at build 412", data, err)
	}

	f.setPointer(t, "v0.9.0-beta.1", 380)
	restarted := NewChannelChecker(srv.URL, time.Hour, ChannelBeta, state)
	got, err := restarted.CheckFresh(t.Context())
	if want := "channel pointer beta.json names build 380, below build 412 seen before"; err == nil || err.Error() != want || got.Latest != "" || got.UpdateAvailable {
		t.Fatalf("check after replay = %+v, %v; want nothing offered and %q", got, err, want)
	}
	f.mu.Lock()
	f.pointers = nil
	f.mu.Unlock()
	if _, err := NewChannelChecker(srv.URL, time.Hour, ChannelBeta, state).CheckFresh(t.Context()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("check with the pointer gone = %v, want a failed check", err)
	}
	if n := f.hits.Load(); n != 1 {
		t.Fatalf("asked /releases/latest %d times, want only the check before the pointer", n)
	}
}
