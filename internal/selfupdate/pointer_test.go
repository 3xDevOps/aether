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

// A state file that cannot be parsed fails the check and is left as it is.
func TestChannelCheckerFailsClosedOnACorruptStateFile(t *testing.T) {
	setVersion(t, "v0.9.0-beta.1", "abc1234")
	f, srv := newFakeRelease(t, "v0.9.0-beta.1", testAssets)
	f.builds = map[string]Release{"v0.9.0-beta.2": {Channel: ChannelBeta, Build: 412}}
	f.setPointer(t, "v0.9.0-beta.2", 412)
	state := filepath.Join(t.TempDir(), "update.json")
	corrupt := []byte(`{"pointers": {"beta": {"build": 4`)
	if err := os.WriteFile(state, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewChannelChecker(srv.URL, time.Hour, ChannelBeta, state).CheckFresh(t.Context())
	if err == nil || !strings.Contains(err.Error(), state) || got.Latest != "" {
		t.Fatalf("check = %+v, %v; want it failed naming %s", got, err, state)
	}
	if data, _ := os.ReadFile(state); string(data) != string(corrupt) {
		t.Fatalf("state file rewritten to %s", data)
	}
	if f.hits.Load() != 0 {
		t.Fatal("asked /releases/latest with a corrupt state file")
	}
}

// Another process may accept a higher pointer between this check's read
// and its save; the save then refuses to lower the mark.
func TestChannelCheckerNeverLowersAMarkAnotherProcessRaised(t *testing.T) {
	state := filepath.Join(t.TempDir(), "update.json")
	raised := []byte(`{"pointers": {"beta": {"build": 500, "hotfix": 0}, "stable": {"build": 400, "hotfix": 0}}}`)
	if err := os.WriteFile(state, raised, 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewChannelChecker("http://unused", time.Hour, ChannelBeta, state)
	if err := c.raiseMark(Mark{Build: 412}); err == nil || err.Error() != "channel pointer beta.json names build 412, below build 500 seen before" {
		t.Fatalf("raise below another process's mark = %v", err)
	}
	if err := c.raiseMark(Mark{Build: 501}); err != nil {
		t.Fatal(err)
	}
	st, err := loadChannelState(state)
	if err != nil || st.Pointers[ChannelBeta] != (Mark{Build: 501}) || st.Pointers[ChannelStable] != (Mark{Build: 400}) {
		t.Fatalf("state = %+v, %v; want beta raised to 501 and stable kept", st, err)
	}
}

// A pointer equal to the mark read before the fetch is still checked under
// the lock: another process may have raised the mark meanwhile.
func TestChannelCheckerRefusesAReplayAnotherProcessOvertook(t *testing.T) {
	setVersion(t, "v0.9.0-beta.1", "abc1234")
	f, srv := newFakeRelease(t, "v0.9.0-beta.1", testAssets)
	f.builds = map[string]Release{"v0.9.0-beta.1": {Channel: ChannelBeta, Build: 400}}
	f.setPointer(t, "v0.9.0-beta.1", 400)
	state := filepath.Join(t.TempDir(), "update.json")
	if err := os.WriteFile(state, []byte(`{"pointers": {"beta": {"build": 400, "hotfix": 0}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.onPointer = func() {
		_ = os.WriteFile(state, []byte(`{"pointers": {"beta": {"build": 412, "hotfix": 0}}}`), 0o600)
	}
	got, err := NewChannelChecker(srv.URL, time.Hour, ChannelBeta, state).CheckFresh(t.Context())
	if want := "channel pointer beta.json names build 400, below build 412 seen before"; err == nil || err.Error() != want || got.Latest != "" {
		t.Fatalf("check = %+v, %v; want %q", got, err, want)
	}
}
