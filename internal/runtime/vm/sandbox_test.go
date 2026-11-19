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

	dataDir := filepath.Dir(h.engine.root)
	for _, props := range [][]string{session, offline} {
		for _, want := range []string{
			"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "ProtectKernelTunables=yes",
			"ProtectControlGroups=yes", "DevicePolicy=closed", "DeviceAllow=/dev/kvm rw",
			"RestrictAddressFamilies=AF_UNIX AF_INET", "TemporaryFileSystem=" + dataDir + ":ro",
		} {
			if !slices.Contains(props, want) {
				t.Errorf("unit properties %v lack %s", props, want)
			}
		}
	}
	// Writable binds reveal the VM's directory, its state disk and its
	// writable shares; the read-only login is revealed read-only; the data
	// dir at large stays hidden.
	writable := property(session, "BindPaths")
	for _, want := range []string{`"-` + h.engine.dir("run-a") + `"`, `"-` + filepath.Join(state, stateDiskFile) + `"`, `"-` + home + `"`} {
		if !slices.Contains(writable, want) {
			t.Errorf("a session's unit writes %v, want it to include %s", writable, want)
		}
	}
	// The login, the record and launch the server trusts, and the helper
	// disk's base link are revealed read-only inside the writable directory.
	readonly := property(session, "BindReadOnlyPaths")
	for _, want := range []string{
		`"-` + login + `"`,
		`"-` + h.engine.path("run-a", recordFile) + `"`,
		`"-` + h.engine.path("run-a", launchFile) + `"`,
		`"-` + h.engine.path("run-a", helperImage) + `"`,
	} {
		if !slices.Contains(readonly, want) {
			t.Errorf("a session's read-only binds %v lack %s", readonly, want)
		}
		if slices.Contains(writable, want) {
			t.Errorf("%s is writable to the unit: %v", want, writable)
		}
	}
	if got := property(offline, "BindPaths"); !slices.Contains(got, `"-`+h.engine.dir("check-a")+`"`) {
		t.Errorf("an offline VM writes %v, want its directory", got)
	}
	if !slices.Contains(session, "ProtectHome=yes") {
		t.Errorf("a VM whose data directory is outside the home directories lacks ProtectHome=yes: %v", session)
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
}

// TestVMUnitDropsProtectHomeWhenItWouldHideWhatTheVMNeeds checks that a data
// directory or a revealed path under a home directory, where ProtectHome
// would hide it past every bind, drops ProtectHome while the data-directory
// tmpfs and the binds stay.
func TestVMUnitDropsProtectHomeWhenItWouldHideWhatTheVMNeeds(t *testing.T) {
	rec := &record{Net: offline}
	under := sandbox("/home/runner/work/data", "/usr/local/bin/aether-server", rec, launch{Reveal: map[string]bool{"/home/runner/work/data/vm/run-a": true}})
	if slices.Contains(under, "ProtectHome=yes") {
		t.Errorf("a data directory under /home kept ProtectHome=yes: %v", under)
	}
	for _, want := range []string{"TemporaryFileSystem=/home/runner/work/data:ro", `BindPaths="-/home/runner/work/data/vm/run-a"`} {
		if !slices.Contains(under, want) {
			t.Errorf("unit %v lacks %s; the tmpfs and binds must stay", under, want)
		}
	}
	out := sandbox("/var/lib/aether", "/usr/local/bin/aether-server", rec, launch{Reveal: map[string]bool{"/root/.ssh": false}})
	if slices.Contains(out, "ProtectHome=yes") {
		t.Errorf("a revealed path under /root kept ProtectHome=yes: %v", out)
	}
	home := sandbox("/var/lib/aether", "/home/me/aether-server", rec, launch{Reveal: map[string]bool{"/var/lib/aether/vm/run-a": true}})
	if slices.Contains(home, "ProtectHome=yes") {
		t.Errorf("a monitor binary under /home kept ProtectHome=yes: %v", home)
	}
	ok := sandbox("/var/lib/aether", "/usr/local/bin/aether-server", rec, launch{Reveal: map[string]bool{"/var/lib/aether/vm/run-a": true}})
	if !slices.Contains(ok, "ProtectHome=yes") {
		t.Errorf("a data directory, monitor and reveals outside the home directories lack ProtectHome=yes: %v", ok)
	}
}
