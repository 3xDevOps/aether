package vm

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/home"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func listen(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

func TestPlanMountsRoutesEachMountAndForwardsSockets(t *testing.T) {
	dir, err := os.MkdirTemp("", "vmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	checkout, memberHome, coord, signing := filepath.Join(dir, "checkout"), filepath.Join(dir, "home"), filepath.Join(dir, "coord"), filepath.Join(dir, "sign")
	for _, d := range []string{checkout, memberHome, coord, signing} {
		if err = os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "aether-server")
	if err = os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(coord, "next-command"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(coord, "coord3.sock"))
	listen(t, filepath.Join(signing, "agent.sock"))

	checkouts := []runtime.Mount{{HostPath: checkout, ContainerPath: "/workspace"}}
	mounts := []runtime.Mount{
		{HostPath: memberHome, ContainerPath: "/root", Overlay: filepath.Join(dir, "overlay")},
		{HostPath: memberHome, Subpath: ".claude/.credentials.json", ContainerPath: "/root/.claude/.credentials.json"},
		{HostPath: bin, ContainerPath: "/opt/aether/aether-server", ReadOnly: true, Kind: runtime.MountServerBinary},
		{HostPath: coord, ContainerPath: "/run/aether", ReadOnly: true},
		{HostPath: signing, ContainerPath: "/run/aether-signing", ReadOnly: true},
		{HostPath: filepath.Join(dir, "cache"), ContainerPath: "/aether-cache", Kind: runtime.MountCache},
		{HostPath: filepath.Join(dir, "tmp"), ContainerPath: "/tmp", Kind: runtime.MountScratch},
	}
	trusted := func(d string) bool { return d == coord || d == signing }
	p, err := planMounts(checkouts, mounts, true, dir, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if want := []share{{Source: checkout}}; !slices.Equal(p.shares, want) {
		t.Errorf("virtio-fs shares = %+v", p.shares)
	}
	wantHome := []home.Share{
		{Source: memberHome, ReadOnly: true}, {Source: memberHome, Subpath: ".claude/.credentials.json"}, {Source: coord, ReadOnly: true},
	}
	if !slices.Equal(p.home, wantHome) {
		t.Errorf("file service shares = %+v", p.home)
	}
	wantMounts := []guest.Mount{
		{Volume: guest.VolumeShare, Source: "0", Target: "/workspace"},
		{Volume: guest.VolumeHome, Source: "0", Target: "/root", Overlay: true},
		{Volume: guest.VolumeHome, Source: "1", Target: "/root/.claude/.credentials.json"},
		{Volume: guest.VolumeHelper, Source: ".", Target: "/opt/aether/aether-server", ReadOnly: true},
		{Volume: guest.VolumeHome, Source: "2", Target: "/run/aether", ReadOnly: true},
		{Volume: guest.VolumeCache, Source: ".", Target: "/aether-cache"},
		{Volume: guest.VolumeData, Source: "tmp", Target: "/tmp"},
		{Volume: guest.VolumeData, Source: "docker", Target: "/var/lib/docker"},
	}
	if !slices.Equal(p.mounts, wantMounts) {
		t.Errorf("mounts = %+v", p.mounts)
	}
	wantSockets := []guest.Socket{{Port: 1025, Path: "/run/aether/coord3.sock"}, {Port: 1026, Path: "/run/aether-signing/agent.sock"}}
	if !slices.Equal(p.sockets, wantSockets) {
		t.Errorf("sockets = %+v", p.sockets)
	}
	if cfg := p.homeConfig("1000:1001"); cfg == nil || cfg.UID != 1000 || cfg.GID != 1001 || len(cfg.Shares) != 3 {
		t.Errorf("file service config = %+v", cfg)
	}
	if err = linkSockets(dir, p.links); err != nil {
		t.Fatal(err)
	}
	vsock := filepath.Join(dir, vsockSocket)
	for port, want := range map[string]string{"1026": filepath.Join(signing, "agent.sock"), "1023": filepath.Join(dir, homeSocket)} {
		if target, linkErr := os.Readlink(vsock + "_" + port); linkErr != nil || target != want {
			t.Errorf("link for port %s = %q, %v", port, target, linkErr)
		}
	}
	if err = linkSockets(dir, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(vsock + "_1026"); !os.IsNotExist(err) {
		t.Errorf("stale link kept: %v", err)
	}
	listen(t, filepath.Join(memberHome, "agent.sock"))
	untrusted, err := planMounts(nil, mounts[:1], false, dir, trusted)
	if err != nil || len(untrusted.sockets) != 0 || len(untrusted.home) != 1 {
		t.Errorf("a socket in a directory a guest can write was forwarded: %+v, %v", untrusted, err)
	}
	if _, err := planMounts(nil, []runtime.Mount{{HostPath: filepath.Join(dir, "gone"), ContainerPath: "/x"}}, false, dir, trusted); err == nil {
		t.Error("a missing mount source was planned")
	}
}
