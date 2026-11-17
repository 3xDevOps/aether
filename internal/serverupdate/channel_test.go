package serverupdate

import (
	"context"
	"crypto/ed25519"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/selfupdate/releasetest"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

// A new server chooses its first channel from the pointers: stable only
// for a stable.json signed by a release key, beta once two checks in a row
// found none, and
// no choice at all, following beta meanwhile, for one that does not verify.
// A channel already chosen is never replaced.
func TestNewServerChoosesItsChannelFromTheStablePointer(t *testing.T) {
	_, forger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		key     ed25519.PrivateKey
		set     string
		want    string
		pending bool
	}{
		"verified":      {key: releasetest.Key, want: selfupdate.ChannelStable},
		"none":          {want: selfupdate.ChannelBeta},
		"forged":        {key: forger, want: selfupdate.ChannelBeta, pending: true},
		"nightly key":   {key: releasetest.NightlyKey, want: selfupdate.ChannelBeta, pending: true},
		"already chose": {key: releasetest.Key, set: selfupdate.ChannelNightly, want: selfupdate.ChannelNightly},
	} {
		t.Run(name, func(t *testing.T) {
			rel := channelHost(t)
			if tc.key != nil {
				publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), tc.key)
			}
			db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if tc.set != "" {
				if err = db.SetReleaseChannel(t.Context(), tc.set); err != nil {
					t.Fatal(err)
				}
			}
			svc, err := New(Config{Store: db, Bus: newTestBus(t), StateDir: t.TempDir(), Now: time.Now, Checker: selfupdate.NewChecker(rel.URL, time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if tc.key == nil {
				// One 404 may be stable.json being replaced: the first check
				// follows beta without choosing it.
				svc.runCheck(t.Context())
				if stored, _ := db.ReleaseChannel(t.Context()); stored != "" {
					t.Fatalf("one 404 chose %q", stored)
				}
			}
			if st := svc.runCheck(t.Context()); st.Channel != tc.want {
				t.Fatalf("check followed %q (%+v), want %q", st.Channel, st, tc.want)
			}
			stored, err := db.ReleaseChannel(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if wantStored := map[bool]string{false: tc.want, true: ""}[tc.pending]; stored != wantStored {
				t.Fatalf("stored channel = %q, want %q", stored, wantStored)
			}
			status, err := svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
			if err != nil {
				t.Fatal(err)
			}
			if status.Channel != tc.want || status.ChannelPending != tc.pending {
				t.Fatalf("status channel = %q pending %v, want %q pending %v", status.Channel, status.ChannelPending, tc.want, tc.pending)
			}
		})
	}
}

// A channel change retires a pending update the new channel does not take,
// a dry run names it, and a super admin's explicit update afterwards still
// installs it now; one for an idle moment is refused. A pending update is
// refused at the idle moment when its channel changed on the host
// meanwhile.
func TestChannelChangeRetiresAPendingUpdateItDoesNotTake(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	dry, err := e.svc.SetChannel(t.Context(), "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelStable, DryRun: true})
	if err != nil || dry.Retired != "v0.9.0-nightly.14" || dry.Saved {
		t.Fatalf("dry run = %+v, %v; want it to name nightly.14", dry, err)
	}
	e.wantPending(t, "v0.9.0-nightly.14", "mem_a")
	set, err := e.svc.SetChannel(t.Context(), "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelStable})
	if err != nil || set.Retired != "v0.9.0-nightly.14" {
		t.Fatalf("switch = %+v, %v", set, err)
	}
	e.wantPending(t, "", "")
	e.busy, e.underSystemd = domain.ServerBusy{}, true
	e.svc.Tick(t.Context())
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatal("a retired update was installed")
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); !errors.Is(err, ErrPreflight) {
		t.Fatalf("idle update of a release stable does not take = %v, want refused", err)
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateNow}); err != nil {
		t.Fatalf("a super admin's explicit update after the switch: %v", err)
	}

	host := autoEnv(t, selfupdate.ChannelNightly)
	host.busy = domain.ServerBusy{Runs: 1}
	host.svc.checkAndSchedule(t.Context())
	host.wantPending(t, "v0.9.0-nightly.13", ServerActor)
	if err := host.db.SetReleaseChannel(t.Context(), selfupdate.ChannelStable); err != nil {
		t.Fatal(err)
	}
	host.busy, host.underSystemd = domain.ServerBusy{}, true
	host.svc.Tick(t.Context())
	if got := host.read(t, host.self); got != "the old binary" {
		t.Fatal("the server installed its own update after its channel moved to stable")
	}
	host.wantPending(t, "", "")
}

// A beta server running the stable label of its build keeps it: beta takes
// stable, so beta.2 at the same build is no update.
func TestAcceptedLabelIsNotRelabelled(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelBeta)
	setRunning(t, "v0.9.0", 412, 0)
	e.rel.SetBuild("v0.9.0", selfupdate.ChannelStable, 412, 0)
	publishPointer(t, e.rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	e.svc.checkAndSchedule(t.Context())
	e.wantPending(t, "", "")
	st, err := e.svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil || st.UpdateAvailable || st.LatestVersion != "v0.9.0-beta.2" {
		t.Fatalf("status = %+v, %v; want beta.2 found and not offered", st, err)
	}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{When: protocol.ServerUpdateNow}); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("update with no version = %v, want up to date", err)
	}
}

// A channel change whose request ends while a check holds the slot saves
// the channel, reports the wait, and records no failed check.
func TestChannelChangeDoesNotRecordACancelledCheck(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.svc.run <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	set, err := e.svc.SetChannel(ctx, "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelBeta})
	<-e.svc.run
	if err != nil || !set.Saved || !strings.Contains(set.TargetError, context.DeadlineExceeded.Error()) {
		t.Fatalf("switch = %+v, %v; want saved with the deadline", set, err)
	}
	if e.svc.check.Failures != 0 || e.svc.check.Error != "" {
		t.Fatalf("check = %+v, want no failure recorded", e.svc.check)
	}
	e.svc.checkRuns.Store(0)
	ctx2, cancel2 := context.WithCancel(t.Context())
	cancel2()
	if st := e.svc.runCheck(ctx2); st.Failures != 0 || st.Error != "" {
		t.Fatalf("a cancelled check = %+v, want nothing recorded", st)
	}
}

// Status shows each channel's verified pointer with its published_at, for
// display only: a forged pointer for another channel is left out and
// changes nothing the server follows.
func TestStatusShowsEveryChannelsVerifiedPointer(t *testing.T) {
	rel := channelHost(t)
	publishPointer(t, rel, "nightly", pointer("nightly", "v0.9.0-nightly.12", 412), releasetest.NightlyKey)
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), releasetest.Key)
	svc := channelService(t, t.TempDir(), rel.URL, "beta")
	svc.checkAndSchedule(t.Context())
	if svc.check.Error != "" {
		t.Fatal(svc.check.Error)
	}
	got, err := svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]protocol.ChannelPointer{
		"nightly": {Version: "v0.9.0-nightly.12", Build: 412, PublishedAt: "2026-11-20T10:00:00Z"},
		"beta":    {Version: "v0.9.0-beta.2", Build: 412, PublishedAt: "2026-11-20T10:00:00Z"},
		"stable":  {Version: "v0.9.0", Build: 412, PublishedAt: "2026-11-20T10:00:00Z"},
	}
	if !maps.Equal(got.ChannelPointers, want) {
		t.Fatalf("channel_pointers = %+v, want %+v", got.ChannelPointers, want)
	}

	_, forger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 999), forger)
	before := svc.check
	svc.checkAndSchedule(t.Context())
	st := svc.check
	if _, ok := svc.pointers["stable"]; ok || st.Error != "" || len(svc.pointers) != 2 {
		t.Fatalf("check with a forged stable.json = %+v, pointers %+v, want stable left out and no error", st, svc.pointers)
	}
	if !maps.Equal(st.Pointers, before.Pointers) || st.Channel != "beta" || st.LatestVersion != before.LatestVersion {
		t.Fatalf("a forged display pointer changed the check: %+v, was %+v", st, before)
	}
}

// A stable.json whose signature is missing is not a missing stable.json:
// the server stays unchosen however many checks find it so.
func TestMissingStableSignatureChoosesNothing(t *testing.T) {
	rel := channelHost(t)
	data, _ := releasetest.SignPointer(t, pointer("stable", "v0.9.0", 412), releasetest.Key)
	rel.SetPointer("stable", data, nil)
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := New(Config{Store: db, Bus: newTestBus(t), StateDir: t.TempDir(), Now: time.Now, Checker: selfupdate.NewChecker(rel.URL, time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for range stableMissingChecks + 1 {
		svc.runCheck(t.Context())
	}
	if stored, err := db.ReleaseChannel(t.Context()); err != nil || stored != "" {
		t.Fatalf("stored channel = %q, %v; want none chosen", stored, err)
	}
}

// After a restart with its next check hours away, the server shows the
// channel pointers again without waiting for that check.
func TestPointersReturnAfterARestart(t *testing.T) {
	rel := channelHost(t)
	publishPointer(t, rel, "beta", pointer("beta", "v0.9.0-beta.2", 412), releasetest.Key)
	publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), releasetest.Key)
	dir := t.TempDir()
	state := `{"channel":"beta","checked_at":"2026-11-15T10:00:00Z","next_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "check.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	setVersion(t, "v0.9.0-beta.1")
	svc := channelService(t, dir, rel.URL, "beta")
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
		if err != nil {
			t.Fatal(err)
		}
		if len(st.ChannelPointers) == 2 && st.ChannelPointers["beta"].Version == "v0.9.0-beta.2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("channel_pointers after a restart = %+v", st.ChannelPointers)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A channel change that cannot fetch the pending update's manifest keeps
// the update and says so, with the fetch error, in a dry run and when
// saved.
func TestChannelChangeReportsAPendingUpdateItKept(t *testing.T) {
	e := autoEnv(t, selfupdate.ChannelNightly)
	e.busy = domain.ServerBusy{Runs: 1}
	if _, _, err := e.svc.Update(t.Context(), "mem_a", protocol.ServerUpdateParams{Version: "v0.9.0-nightly.14", When: protocol.ServerUpdateIdle}); err != nil {
		t.Fatal(err)
	}
	e.rel.Unsign()
	for _, dry := range []bool{true, false} {
		set, err := e.svc.SetChannel(t.Context(), "mem_a", protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelStable, DryRun: dry})
		if err != nil || set.Kept != "v0.9.0-nightly.14" || set.KeptError == "" || set.Retired != "" || set.Saved == dry {
			t.Fatalf("dry run %v = %+v, %v; want nightly.14 kept with the fetch error", dry, set, err)
		}
		e.wantPending(t, "v0.9.0-nightly.14", "mem_a")
	}
}
