//go:build integration

package server

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
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

// A server on nightly build 412 moves to beta at build 380: it waits on its
// build and says so, installs nothing lower, and updates once beta's
// pointer reaches build 412, the same code under the beta label. Only a
// super admin changes the channel, every member reads it, and a pointer
// replayed after switching away and back is still refused.
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

	// A new server with no stable.json chooses beta at its first check.
	var st protocol.ServerUpdateStatusResult
	for st = srv.status(t); st.CheckedAt == "" || st.Channel != selfupdate.ChannelBeta || st.LatestVersion == ""; st = srv.status(t) {
		if ctx.Err() != nil {
			t.Fatalf("the first check never chose a channel: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
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

	publishChannel(t, release, selfupdate.ChannelBeta, "v0.9.0-beta.2", 412)
	h.skew.Store(int64(time.Minute))
	var fresh protocol.ServerUpdateStatusResult
	if err := srv.ctrl.Call(protocol.MethodServerUpdateStatus, protocol.ServerUpdateStatusParams{Refresh: true}, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.LatestVersion != "v0.9.0-beta.2" || fresh.LatestBuild != 412 || !fresh.UpdateAvailable || fresh.ChannelWait {
		t.Fatalf("status once beta reached the build = %+v, want beta.2 available", fresh)
	}

	// The beta mark survives switching to nightly and back.
	srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelNightly})
	publishChannel(t, release, selfupdate.ChannelBeta, "v0.9.0-beta.1", 380)
	srv.setChannel(t, protocol.ServerReleaseChannelSetParams{Channel: selfupdate.ChannelBeta})
	const replayed = "channel pointer beta.json names build 380, below build 412 seen before"
	if st = srv.status(t); st.CheckError != replayed || st.UpdateAvailable {
		t.Fatalf("status with beta.json replayed = %+v, want %q", st, replayed)
	}
	wantCode(t, srv.update(protocol.ServerUpdateParams{}), protocol.CodeInternal, replayed)

	publishChannel(t, release, selfupdate.ChannelBeta, "v0.9.0-beta.2", 412)
	if err := srv.update(protocol.ServerUpdateParams{}); err != nil {
		t.Fatalf("update to beta.2: %v", err)
	}
	if readBinary(t, h.serverBin) != updatedServerBinary {
		t.Fatal("the update once beta reached the build did not install beta.2")
	}
}
