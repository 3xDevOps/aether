package vm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func property(props []string, key string) []string {
	var values []string
	for _, p := range props {
		if v, ok := strings.CutPrefix(p, key+"="); ok {
			values = append(values, v)
		}
	}
	return values
}

func TestVMUnitsWriteOnlyTheirOwnPathsAndKeepFewCapabilities(t *testing.T) {
	h := newHarness(t)
	spec := sessionSpec("run-a")
	spec.WorktreeHostPath, spec.WorktreeMountPath = t.TempDir(), "/workspace"
	home, login, state := t.TempDir(), filepath.Join(t.TempDir(), "login.json"), t.TempDir()
	if err := os.WriteFile(login, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	spec.Mounts = []runtime.Mount{
		{HostPath: home, ContainerPath: "/root"},
		{HostPath: login, ContainerPath: "/root/.login.json", ReadOnly: true},
		{HostPath: state, Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState},
	}
	h.createStarted("run-a", spec)
	session := h.units.last().properties
	if _, err := h.engine.create(t.Context(), "check-a", runtime.Spec{Image: "aether/standard:1", Command: []string{"/bin/sh"}, MemoryLimitBytes: 1 << 30}, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "check-a"); err != nil {
		t.Fatal(err)
	}
	offline := h.units.last().properties

	for _, props := range [][]string{session, offline} {
		for _, want := range []string{"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes"} {
			if !slices.Contains(props, want) {
				t.Errorf("unit properties %v lack %s", props, want)
			}
		}
	}
	quoted := func(paths ...string) []string {
		var out []string
		for _, p := range paths {
			out = append(out, `"-`+p+`"`)
		}
		return out
	}
	want := quoted(h.engine.dir("run-a"), filepath.Join(state, stateDiskFile), spec.WorktreeHostPath, home)
	if got := property(session, "ReadWritePaths"); !slices.Equal(got, want) {
		t.Errorf("a session's unit writes %v, want %v: its own directory, state disk and writable shares", got, want)
	}
	if got := property(offline, "ReadWritePaths"); !slices.Equal(got, quoted(h.engine.dir("check-a"))) {
		t.Errorf("an offline VM without shares writes %v, want only its directory", got)
	}
	caps := func(props []string) []string {
		return strings.Fields(strings.Join(property(props, "CapabilityBoundingSet"), " "))
	}
	if got, want := caps(session), slices.Concat(fileCaps, shareCaps, []string{"CAP_NET_ADMIN"}); !slices.Equal(got, want) {
		t.Errorf("a session with a share and a network keeps %v, want %v", got, want)
	}
	if got := caps(offline); !slices.Equal(got, fileCaps) {
		t.Errorf("an offline VM without shares keeps %v, want %v", got, fileCaps)
	}

	cfg := Config{Docker: &runtime.Docker{}, Images: h.images, Helper: "/usr/local/bin/aether-server"}
	for _, c := range []struct{ dataDir, monitor string }{{"/root/aether", "/usr/local/bin/aether-server"}, {"/var/lib/aether", "/home/me/aether-server"}} {
		cfg.DataDir, cfg.Monitor = c.dataDir, []string{c.monitor, MonitorCommand}
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "ProtectHome=yes") {
			t.Errorf("New with data directory %s and monitor %s = %v, want the hidden path refused", c.dataDir, c.monitor, err)
		}
	}
}
