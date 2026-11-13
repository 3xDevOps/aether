package guest

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestKernelArgsRoundTrip(t *testing.T) {
	cfg := Config{
		Hostname: "run-abc",
		Network: &Network{
			Address: netip.MustParsePrefix("10.88.0.5/24"),
			Gateway: netip.MustParseAddr("10.88.0.1"),
			DNS:     []netip.Addr{netip.MustParseAddr("10.88.0.1"), netip.MustParseAddr("1.1.1.1")},
		},
		Mounts: []Mount{
			{Volume: VolumeShare, Source: "home", Target: "/root"},
			{Volume: VolumeShare, Source: "repos/my repo,v2", Target: "/repos/my repo,v2", ReadOnly: true},
			{Volume: VolumeData, Source: "tmp", Target: "/tmp"},
		},
		Sockets: []Socket{{Port: 1025, Path: "/run/aether/coord3.sock"}},
	}
	line, err := cfg.KernelArgs("console=ttyS0 root=/dev/vda")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "console=ttyS0 root=/dev/vda ") || !strings.HasSuffix(line, " -- guest") {
		t.Fatalf("command line %q must keep the base first and end with the helper's arguments", line)
	}
	if strings.Contains(line, "my repo") || !strings.Contains(line, "/run/aether/coord3.sock") {
		t.Fatalf("command line %q must escape spaces and commas, and only them", line)
	}
	got, err := ParseCmdline(line)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, cfg)
	}
}

func TestConfigRejects(t *testing.T) {
	for name, cfg := range map[string]Config{
		"relative target":   {Mounts: []Mount{{Volume: VolumeShare, Source: "a", Target: "root"}}},
		"escaping source":   {Mounts: []Mount{{Volume: VolumeData, Source: "../etc", Target: "/etc"}}},
		"unknown volume":    {Mounts: []Mount{{Volume: "disk", Source: "a", Target: "/a"}}},
		"api port":          {Sockets: []Socket{{Port: APIPort, Path: "/run/x.sock"}}},
		"hostname":          {Hostname: "bad host"},
		"network no prefix": {Network: &Network{Gateway: netip.MustParseAddr("10.0.0.1")}},
	} {
		if _, err := cfg.KernelArgs(""); err == nil {
			t.Errorf("%s: accepted %+v", name, cfg)
		}
	}
	long := Config{}
	for range 100 {
		long.Mounts = append(long.Mounts, Mount{Volume: VolumeShare, Source: "some/long/source/path", Target: "/some/long/target/path"})
	}
	if _, err := long.KernelArgs(""); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a command line past the kernel's limit: got %v", err)
	}
	if _, err := ParseCmdline("aether.unknown=1 -- guest"); err == nil {
		t.Error("an unknown aether argument must fail rather than be ignored")
	}
}
