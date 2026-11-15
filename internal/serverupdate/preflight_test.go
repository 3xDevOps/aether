package serverupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

func (e *env) updateNow(t *testing.T, p protocol.ServerUpdateParams) error {
	t.Helper()
	p.When = protocol.ServerUpdateNow
	if p.Version == "" {
		p.Version = releaseTagUnderTest
	}
	_, _, err := e.svc.Update(t.Context(), "mem_admin", p)
	return err
}

// untouched asserts that a refused update replaced nothing and downloaded
// no server binary.
func (e *env) untouched(t *testing.T) {
	t.Helper()
	for _, path := range []string{e.self, e.sibling} {
		if got := e.read(t, path); got != "the old binary" {
			t.Fatalf("%s = %q, want it untouched", path, got)
		}
	}
	asset := "/releases/download/" + releaseTagUnderTest + "/aether-server-" + runtime.GOOS + "-" + runtime.GOARCH
	if n := e.rel.Requests(asset); n != 0 {
		t.Fatalf("%s was requested %d times before the preflight passed", asset, n)
	}
}

func TestPreflightRefusesAClientAssetThatIsNotDownloadable(t *testing.T) {
	for name, breakIt := range map[string]func(e *env) string{
		"404": func(e *env) string {
			e.setMissing("aether-android.apk")
			return "aether-android.apk (" + e.release.URL + "/releases/download/v0.2.0/aether-android.apk): HEAD answered 404 Not Found"
		},
		"size": func(e *env) string {
			e.rel.Tamper("aether-darwin-arm64", []byte("short"))
			return "aether-darwin-arm64 (" + e.release.URL + "/releases/download/v0.2.0/aether-darwin-arm64): Content-Length is 5, the manifest says"
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, false)
			want := breakIt(e)
			err := e.updateNow(t, protocol.ServerUpdateParams{})
			if !errors.Is(err, ErrPreflight) || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want ErrPreflight naming %q", err, want)
			}
			e.untouched(t)
			e.phase(t) // preflight
			if p := e.phase(t); p.Phase != events.ServerUpdateFailed || p.Detail != err.Error() {
				t.Fatalf("phase = %+v, want failed with the error", p)
			}
		})
	}
}

func TestPreflightRefusesAReleaseThatDoesNotPublishAClient(t *testing.T) {
	e := newEnv(t, false)
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	partial := releasetest.Serve(t, releaseTagUnderTest, map[string][]byte{
		"aether-server" + suffix: []byte(newServerBinary),
		"aether" + suffix:        []byte(newCLIBinary),
	})
	e.svc.cfg.Checker = selfupdate.NewChecker(partial.URL, time.Hour)
	err := e.updateNow(t, protocol.ServerUpdateParams{})
	if !errors.Is(err, ErrPreflight) || !strings.Contains(err.Error(), "v0.2.0 does not publish aether-") {
		t.Fatalf("err = %v, want the missing client named", err)
	}
	e.untouched(t)
}

func TestPreflightRefusesAReleaseWithoutASignedManifest(t *testing.T) {
	e := newEnv(t, false)
	e.rel.Unsign()
	err := e.updateNow(t, protocol.ServerUpdateParams{})
	if !errors.Is(err, ErrPreflight) || !strings.Contains(err.Error(), "aether-release.json") {
		t.Fatalf("err = %v, want the missing manifest named", err)
	}
	e.untouched(t)
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func TestDowngradeIsRefusedUnlessAllowed(t *testing.T) {
	setVersion(t, "v0.3.0-2-gabc1234")
	e := newEnv(t, false)
	err := e.updateNow(t, protocol.ServerUpdateParams{})
	if !errors.Is(err, ErrDowngrade) || !strings.Contains(err.Error(), "v0.2.0 is older than the running v0.3.0") {
		t.Fatalf("err = %v, want a downgrade refusal against the release core", err)
	}
	e.untouched(t)
	if _, _, err := e.svc.Update(t.Context(), "mem_admin", protocol.ServerUpdateParams{
		Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle, AllowDowngrade: true,
	}); !errors.Is(err, ErrIdleDowngrade) {
		t.Fatalf("idle downgrade = %v, want ErrIdleDowngrade", err)
	}
	if err := e.updateNow(t, protocol.ServerUpdateParams{AllowDowngrade: true}); err != nil {
		t.Fatalf("allowed downgrade: %v", err)
	}
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the downgraded release", got)
	}
}

// A release that does not know this database's schema could not open it,
// so no flag installs it.
func TestOlderStoreSchemaIsAlwaysRefused(t *testing.T) {
	e := newEnv(t, false)
	e.rel.SetStoreSchema(store.SchemaVersion() - 1)
	err := e.updateNow(t, protocol.ServerUpdateParams{AllowDowngrade: true})
	if !errors.Is(err, ErrDowngrade) || !strings.Contains(err.Error(), "this database is at schema") {
		t.Fatalf("err = %v, want the schema refusal", err)
	}
	e.untouched(t)
}

// A release that migrates the database gets a copy of it right before the
// restart, which a rollback restores.
func TestAMigratingReleaseSnapshotsTheDatabase(t *testing.T) {
	e := newEnv(t, false)
	e.underSystemd = true
	e.rel.SetStoreSchema(store.SchemaVersion() + 1)
	_, restart, err := e.svc.Update(t.Context(), "mem_admin",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateNow})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err = os.Stat(snapshotPath(e.state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot before the restart: %v, want none yet", err)
	}
	restart()
	rec, err := loadRecord(e.state)
	if err != nil || rec == nil {
		t.Fatalf("swap record = %v, %v", rec, err)
	}
	if rec.Snapshot != snapshotPath(e.state) || rec.SchemaBefore != store.SchemaVersion() {
		t.Fatalf("record = %+v, want the snapshot at schema %d", rec, store.SchemaVersion())
	}
	if got, err := store.ReadSchemaVersion(rec.Snapshot); err != nil || got != store.SchemaVersion() {
		t.Fatalf("snapshot schema = %d, %v", got, err)
	}
}

// The update stays pending until the swap, so a server that stops
// mid-download finds it again on the next start.
func TestAnUpdateIsPendingWhileItDownloads(t *testing.T) {
	e := newEnv(t, false)
	e.stalled, e.resume = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.updateNow(t, protocol.ServerUpdateParams{}) }()
	<-e.stalled
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Pending.Version != releaseTagUnderTest {
		t.Fatalf("pending mid-download = %+v, want v0.2.0", state.Pending)
	}
	status, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != protocol.ServerUpdatePhaseApplying || status.ToVersion != releaseTagUnderTest {
		t.Fatalf("status mid-download = %+v, want applying v0.2.0", status)
	}
	close(e.resume)
	if err = <-done; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if state, err = e.db.GetServerUpdate(t.Context()); err != nil || state.Pending != nil {
		t.Fatalf("pending after the swap = %+v, %v; want none", state.Pending, err)
	}
}

// A scheduled update that cannot reach the release stays pending and
// lands on a later idle poll; it is recorded failed after maxRetries.
func TestAFailedScheduledDownloadIsRetriedFromPending(t *testing.T) {
	e := newEnv(t, false)
	e.underSystemd = true
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.poisonCompanion()
	e.svc.Tick(t.Context())
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Last != nil {
		t.Fatalf("after a failed download: %+v, want it still pending with no outcome", state)
	}
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched", got)
	}
	// A manifest that cannot be fetched at the idle moment is retried too.
	e.heal()
	e.setMissing(selfupdate.ManifestName)
	e.svc.Tick(t.Context())
	if state, err = e.db.GetServerUpdate(t.Context()); err != nil || state.Pending == nil || state.Last != nil {
		t.Fatalf("after an unreachable manifest: %+v, %v; want it still pending", state, err)
	}
	e.heal()
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the retried update applied", got)
	}

	e = newEnv(t, false)
	if _, _, err = e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.poisonCompanion()
	for range maxRetries {
		e.svc.Tick(t.Context())
	}
	if state, err = e.db.GetServerUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.Last == nil || state.Last.Outcome != store.ServerUpdateFailed || !strings.Contains(state.Last.Detail, "checksum mismatch") {
		t.Fatalf("after %d failed downloads: %+v, want failed with the real error", maxRetries, state)
	}
	if _, err := os.Stat(e.state); err == nil {
		if rec, _ := loadRecord(e.state); rec != nil {
			t.Fatalf("a failed download left a swap record: %+v", rec)
		}
	}
}

func TestTheRunningReleaseIsNeverReapplied(t *testing.T) {
	setVersion(t, releaseTagUnderTest)
	e := newEnv(t, false)
	err := e.updateNow(t, protocol.ServerUpdateParams{AllowDowngrade: true})
	if !errors.Is(err, ErrPreflight) || !strings.Contains(err.Error(), "this server already runs v0.2.0") {
		t.Fatalf("err = %v, want the re-apply refused", err)
	}
	e.untouched(t)
}

// A failure of the network or the release host leaves a scheduled update
// pending for a later idle poll instead of dropping it.
func TestATransientPreflightFailureKeepsTheUpdatePending(t *testing.T) {
	old := probeRetryDelay
	probeRetryDelay = 0
	t.Cleanup(func() { probeRetryDelay = old })
	e := newEnv(t, false)
	if _, _, err := e.svc.Update(t.Context(), "mem_a",
		protocol.ServerUpdateParams{Version: releaseTagUnderTest, When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	e.setUnavailable("aether-android.apk")
	e.svc.Tick(t.Context())
	state, err := e.db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Last != nil {
		t.Fatalf("after a 503: %+v, want it still pending with no outcome", state)
	}
	if n := e.unavailableHits("aether-android.apk"); n != 2 {
		t.Fatalf("the unavailable asset was asked %d times, want 2", n)
	}
	e.untouched(t)
	e.heal()
	e.underSystemd = true
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the retried update applied", got)
	}
}

// The probe asks for the uncompressed body: a compressed answer has no
// usable length.
func TestProbeAssetAsksForTheUncompressedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "Accept-Encoding = "+r.Header.Get("Accept-Encoding"), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("12345"))
	}))
	t.Cleanup(srv.Close)
	if err := probeAsset(t.Context(), srv.URL, 5); err != nil {
		t.Fatal(err)
	}
}

func setRunning(t *testing.T, tag string, build, hotfix int) {
	t.Helper()
	setVersion(t, tag)
	oldBuild, oldHotfix := version.Build, version.Hotfix
	version.Build, version.Hotfix = strconv.Itoa(build), strconv.Itoa(hotfix)
	t.Cleanup(func() { version.Build, version.Hotfix = oldBuild, oldHotfix })
}

// The preflight orders releases by their signed (build, hotfix), not by the
// tag: a beta promoted from a later nightly sorts below that nightly by
// semver, and the same code may install under another channel's label but
// never twice under one.
func TestPreflightOrdersReleasesByBuild(t *testing.T) {
	type release struct {
		tag, channel  string
		build, hotfix int
	}
	for _, tc := range []struct {
		name            string
		running, target release
		refused         string
	}{
		{name: "beta from a later nightly",
			running: release{"v0.9.0-nightly.10", "nightly", 400, 0}, target: release{"v0.9.0-beta.2", "beta", 412, 0}},
		{name: "earlier nightly over a later beta",
			running: release{"v0.9.0-beta.2", "beta", 412, 0}, target: release{"v0.9.0-nightly.10", "nightly", 400, 0},
			refused: "v0.9.0-nightly.10 (build 400) is older than the running v0.9.0-beta.2 (build 412)"},
		{name: "relabel onto another channel",
			running: release{"v0.9.0-nightly.12", "nightly", 412, 0}, target: release{"v0.9.0-beta.2", "beta", 412, 0}},
		{name: "relabel on the same channel",
			running: release{"v0.9.0-beta.2", "beta", 412, 0}, target: release{"v0.9.0-beta.3", "beta", 412, 0},
			refused: "v0.9.0-beta.3 (build 412) is the same build as the running v0.9.0-beta.2 (build 412)"},
		{name: "hotfix above its stable",
			running: release{"v0.9.0", "stable", 412, 0}, target: release{"v0.9.1", "stable", 412, 1}},
		{name: "stable below its hotfix",
			running: release{"v0.9.1", "stable", 412, 1}, target: release{"v0.9.0", "stable", 412, 0},
			refused: "v0.9.0 (build 412) is older than the running v0.9.1 (build 412, hotfix 1)"},
		{name: "a release from before channels",
			running: release{"v0.9.0-beta.2", "beta", 412, 0}, target: release{"v0.8.7-beta.14", "", 0, 0},
			refused: "v0.8.7-beta.14 is older than the running v0.9.0-beta.2 (build 412)"},
		{name: "the first release with a build",
			running: release{"v0.8.7-beta.14", "", 0, 0}, target: release{"v0.8.7", "stable", 412, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRunning(t, tc.running.tag, tc.running.build, tc.running.hotfix)
			e := newEnv(t, false)
			e.rel.SetBuild(tc.target.tag, tc.target.channel, tc.target.build, tc.target.hotfix)
			err := e.updateNow(t, protocol.ServerUpdateParams{Version: tc.target.tag})
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("update to %s: %v", tc.target.tag, err)
				}
				if got := e.read(t, e.self); got != newServerBinary {
					t.Fatalf("aether-server = %q, want %s installed", got, tc.target.tag)
				}
				return
			}
			if !errors.Is(err, ErrDowngrade) || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("err = %v, want a downgrade refusal naming %q", err, tc.refused)
			}
			if got := e.read(t, e.self); got != "the old binary" {
				t.Fatalf("aether-server = %q, want it untouched", got)
			}
			if err := e.updateNow(t, protocol.ServerUpdateParams{Version: tc.target.tag, AllowDowngrade: true}); err != nil {
				t.Fatalf("allowed downgrade: %v", err)
			}
		})
	}
}

// allow_downgrade lowers the build but never the store schema.
func TestALowerBuildWithAnOlderStoreSchemaIsAlwaysRefused(t *testing.T) {
	setRunning(t, "v0.9.0-beta.2", 412, 0)
	e := newEnv(t, false)
	e.rel.SetBuild("v0.9.0-nightly.10", "nightly", 400, 0)
	e.rel.SetStoreSchema(store.SchemaVersion() - 1)
	err := e.updateNow(t, protocol.ServerUpdateParams{Version: "v0.9.0-nightly.10", AllowDowngrade: true})
	if !errors.Is(err, ErrDowngrade) || !strings.Contains(err.Error(), "this database is at schema") {
		t.Fatalf("err = %v, want the schema refusal", err)
	}
}

// The background check reports an update by build order.
func TestStatusReportsAnUpdateByBuild(t *testing.T) {
	setRunning(t, "v0.9.0-nightly.10", 400, 0)
	feed := &fakeFeed{}
	feed.set(selfupdate.Manifest{Version: "v0.9.0-beta.2", Channel: "beta", Build: 412}, nil)
	svc := newCheckService(t, t.TempDir(), feed, time.Now)
	got, err := svc.Status(t.Context(), protocol.ServerUpdateStatusParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdateAvailable {
		t.Fatalf("status = %+v, want v0.9.0-beta.2 (build 412) offered over v0.9.0-nightly.10 (build 400)", got)
	}
}
