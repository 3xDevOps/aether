//go:build integration

package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/selfupdate/releasetest"
	"github.com/3xDevOps/cloudfield/internal/serverupdate"
	"github.com/3xDevOps/cloudfield/internal/store"
	"github.com/3xDevOps/cloudfield/internal/version"
)

func publishChannel(t *testing.T, rel *releasetest.Release, channel, tag string, build int) {
	t.Helper()
	key := releasetest.Key
	if channel == selfupdate.ChannelNightly {
		key = releasetest.NightlyKey
	}
	data, sig := releasetest.SignPointer(t, selfupdate.Pointer{
		Schema: selfupdate.PointerSchema, Channel: channel, Tag: tag,
		Commit: "0123456789abcdef0123456789abcdef01234567", Build: build,
		StoreSchema: store.SchemaVersion(), PublishedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}, key)
	rel.SetPointer(channel, data, sig)
}

func (r *running) setChannel(t *testing.T, p protocol.ServerReleaseChannelSetParams) protocol.ServerReleaseChannelSetResult {
	t.Helper()
	var out protocol.ServerReleaseChannelSetResult
	if err := r.ctrl.Call(protocol.MethodServerReleaseChannelSet, p, &out); err != nil {
		t.Fatalf("server.release_channel_set %+v: %v", p, err)
	}
	return out
}

// A new server with no stable.json settles on beta after two checks. On
// nightly build 412 it moves to beta at build 380: it waits on its
// build and says so, installs nothing lower, and updates once beta's
// pointer reaches build 412, the same code under the beta label, which it
// then takes by itself. Only a super admin changes the channel, every
// member reads it, and a pointer replayed after switching away and back is
// still refused.
func TestIntegrationReleaseChannelWaitsForTheLowerChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release := stubRelease(t, "v0.8.7-beta.14")
	release.SetBuild("v0.9.0-nightly.12", selfupdate.ChannelNightly, 412, 0)
	release.SetBuild("v0.9.0-beta.1", selfupdate.ChannelBeta, 380, 0)
	release.SetBuild("v0.9.0-beta.2", selfupdate.ChannelBeta, 412, 0)
	publishChannel(t, release, selfupdate.ChannelNightly, "v0.9.0-nightly.12", 412)
	publishChannel(t, release, selfupdate.ChannelBeta, "v0.9.0-beta.1", 380)
	oldBuild := version.Build
	version.Build = "412"
	t.Cleanup(func() { version.Build = oldBuild })
	h := newUpdateHarness(t, release)
	srv := h.start(t, ctx, "v0.9.0-nightly.12")

	// A new server with no stable.json follows beta from its first check
	// and chooses it at the second that finds stable.json missing.
	var st protocol.ServerUpdateStatusResult
	for st = srv.status(t); st.CheckedAt == "" || st.Channel != selfupdate.ChannelBeta || st.LatestVersion == ""; st = srv.status(t) {
		if ctx.Err() != nil {
			t.Fatalf("the first check never ran: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !st.ChannelPending {
		t.Fatalf("one 404 of stable.json chose the channel: %+v", st)
	}
	h.skew.Store(int64(time.Minute))
	st = protocol.ServerUpdateStatusResult{}
	if err := srv.ctrl.Call(protocol.MethodServerUpdateStatus, protocol.ServerUpdateStatusParams{Refresh: true}, &st); err != nil {
		t.Fatal(err)
	}
	if st.ChannelPending || st.ServerBuild != 412 || st.LatestVersion != "v0.9.0-beta.1" || st.LatestBuild != 380 || !st.ChannelWait || st.UpdateAvailable {
		t.Fatalf("first status = %+v, want beta chosen, waiting above beta.1 (380)", st)
	}

	addr := waitSSHAddr(t, srv.srv)
	for _, role := range []domain.Role{domain.RoleAdmin, domain.RoleCollaborator, domain.RoleViewer} {
		_, signer := writeClientKey(t)
		m := &domain.Member{DisplayName: string(role), PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#3cb44b", Role: role}
		if err := srv.srv.Store().CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
		ctrl := openControl(t, dialSSH(t, addr, signer))
		err := ctrl.Call(protocol.MethodServerReleaseChannelSet, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly}, nil)
		wantCode(t, err, protocol.CodeDenied, "server.release_channel_set: permission denied: this requires the super_admin role")
		var read protocol.ServerUpdateStatusResult
		if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &read); err != nil || read.Channel != selfupdate.ChannelBeta {
			t.Fatalf("%s reads channel %q, %v; want beta", role, read.Channel, err)
		}
	}

	set := srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly})
	if !set.Saved || set.Previous != selfupdate.ChannelBeta || set.Target == nil || set.Target.Build != 412 || set.Wait {
		t.Fatalf("switch to nightly = %+v", set)
	}
	if st = srv.status(t); st.Channel != selfupdate.ChannelNightly || st.UpdateAvailable || st.ChannelWait {
		t.Fatalf("status on nightly = %+v, want up to date", st)
	}

	dry := srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelBeta, DryRun: true})
	want := protocol.ServerReleaseChannelSetResult{
		Channel: selfupdate.ChannelBeta, Previous: selfupdate.ChannelNightly, Wait: true,
		Running: protocol.ReleaseBuild{Version: "v0.9.0-nightly.12", Channel: selfupdate.ChannelNightly, Build: 412},
		Target:  &protocol.ReleaseBuild{Version: "v0.9.0-beta.1", Channel: selfupdate.ChannelBeta, Build: 380},
	}
	if dry.Target == nil || *dry.Target != *want.Target || dry.Running != want.Running || dry.Wait != want.Wait || dry.Saved || dry.Previous != want.Previous {
		t.Fatalf("dry run = %+v, want %+v", dry, want)
	}
	if st = srv.status(t); st.Channel != selfupdate.ChannelNightly {
		t.Fatalf("a dry run saved the channel: %+v", st)
	}

	if set = srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelBeta}); !set.Saved || !set.Wait {
		t.Fatalf("switch to beta = %+v", set)
	}
	if st = srv.status(t); st.Channel != selfupdate.ChannelBeta || !st.ChannelWait || st.UpdateAvailable {
		t.Fatalf("status after leaving nightly = %+v, want waiting", st)
	}
	wantCode(t, srv.update(protocol.ServerUpdateParams{}), protocol.CodeInvalidState, "not older than the latest release v0.9.0-beta.1")
	if readBinary(t, h.serverBin) != "the installed binary" {
		t.Fatal("leaving nightly installed a lower build")
	}

	// The nightly mark survives switching to beta and back: a replayed
	// nightly.json below it is refused.
	release.SetBuild("v0.9.0-nightly.11", selfupdate.ChannelNightly, 411, 0)
	publishChannel(t, release, selfupdate.ChannelNightly, "v0.9.0-nightly.11", 411)
	srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly})
	const replayed = "channel pointer nightly.json names build 411, below build 412 seen before"
	if st = srv.status(t); st.CheckError != replayed || st.UpdateAvailable || st.Pending != nil {
		t.Fatalf("status with nightly.json replayed = %+v, want %q", st, replayed)
	}
	wantCode(t, srv.update(protocol.ServerUpdateParams{}), protocol.CodeInternal, replayed)

	// Once beta reaches build 412 the server takes beta.2, the same code
	// under the beta label, by itself at the first idle moment.
	srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelBeta})
	publishChannel(t, release, selfupdate.ChannelBeta, "v0.9.0-beta.2", 412)
	h.skew.Store(int64(2 * time.Minute))
	if err := srv.ctrl.Call(protocol.MethodServerUpdateStatus, protocol.ServerUpdateStatusParams{Refresh: true}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.drained:
	case <-ctx.Done():
		t.Fatal("the server did not update once beta reached its build")
	}
	if readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatal("the update once beta reached the build did not install beta.2")
	}
}

// A server whose check finds a newer build on its channel schedules it
// itself and applies it at the first idle moment, as the server.
func TestIntegrationServerAppliesANewerBuildByItself(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release := stubRelease(t, "v0.8.7-beta.14")
	release.SetBuild("v0.9.0-nightly.12", selfupdate.ChannelNightly, 412, 0)
	release.SetBuild("v0.9.0-nightly.13", selfupdate.ChannelNightly, 413, 0)
	publishChannel(t, release, selfupdate.ChannelNightly, "v0.9.0-nightly.13", 413)
	oldBuild := version.Build
	version.Build = "412"
	t.Cleanup(func() { version.Build = oldBuild })
	h := newUpdateHarness(t, release)
	srv := h.start(t, ctx, "v0.9.0-nightly.12")

	srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly})
	select {
	case <-h.drained:
	case <-ctx.Done():
		t.Fatalf("the server never applied nightly.13 by itself: %+v", srv.status(t))
	}
	if readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatal("the server's own update did not install nightly.13")
	}
	data, err := os.ReadFile(filepath.Join(UpdateStateDir(h.dataDir), "swap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		ToVersion   string `json:"to_version"`
		RequestedBy string `json:"requested_by"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.ToVersion != "v0.9.0-nightly.13" || rec.RequestedBy != string(serverupdate.ServerActor) {
		t.Fatalf("swap record = %+v, want nightly.13 requested by the server", rec)
	}
}
