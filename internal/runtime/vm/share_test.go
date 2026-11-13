package vm

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest"
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

func TestPlanMountsSharesFilesAndForwardsSockets(t *testing.T) {
	dir, err := os.MkdirTemp("", "vmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	home, coord, signing := filepath.Join(dir, "home"), filepath.Join(dir, "coord"), filepath.Join(dir, "sign")
	for _, d := range []string{home, coord, signing} {
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

	mounts := []runtime.Mount{
		{HostPath: home, ContainerPath: "/root"},
		{HostPath: bin, ContainerPath: "/opt/aether/aether-server", ReadOnly: true},
		{HostPath: coord, ContainerPath: "/run/aether", ReadOnly: true},
		{HostPath: signing, ContainerPath: "/run/aether-signing", ReadOnly: true},
		{HostPath: home, Subpath: ".claude", ContainerPath: "/root/.claude", ReadOnly: true},
	}
	vsock := filepath.Join(dir, vsockSocket)
	p, err := planMounts(mounts, true, vsock)
	if err != nil {
		t.Fatal(err)
	}
	wantShares := []share{{Source: home}, {Source: bin, ReadOnly: true}, {Source: coord, ReadOnly: true}, {Source: home, Subpath: ".claude", ReadOnly: true}}
	if !slices.Equal(p.shares, wantShares) {
		t.Errorf("shares = %+v", p.shares)
	}
	wantMounts := []guest.Mount{
		{Volume: guest.VolumeShare, Source: "0", Target: "/root"},
		{Volume: guest.VolumeShare, Source: "1", Target: "/opt/aether/aether-server", ReadOnly: true},
		{Volume: guest.VolumeShare, Source: "2", Target: "/run/aether", ReadOnly: true},
		{Volume: guest.VolumeShare, Source: "3", Target: "/root/.claude", ReadOnly: true},
		{Volume: guest.VolumeData, Source: "docker", Target: "/var/lib/docker"},
	}
	if !slices.Equal(p.mounts, wantMounts) {
		t.Errorf("mounts = %+v", p.mounts)
	}
	wantSockets := []guest.Socket{{Port: 1025, Path: "/run/aether/coord3.sock"}, {Port: 1026, Path: "/run/aether-signing/agent.sock"}}
	if !slices.Equal(p.sockets, wantSockets) {
		t.Errorf("sockets = %+v", p.sockets)
	}
	if err := linkSockets(dir, p.links); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(vsock + "_1026"); err != nil || target != filepath.Join(signing, "agent.sock") {
		t.Errorf("link = %q, %v", target, err)
	}
	if err := linkSockets(dir, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(vsock + "_1026"); !os.IsNotExist(err) {
		t.Errorf("stale link kept: %v", err)
	}
	if _, err := planMounts([]runtime.Mount{{HostPath: filepath.Join(dir, "gone"), ContainerPath: "/x"}}, false, vsock); err == nil {
		t.Error("a missing mount source was planned")
	}
}
