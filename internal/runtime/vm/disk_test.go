package vm

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// launchDisks returns each --disk value of a launch by its serial.
func launchDisks(t *testing.T, args []string) map[string]map[string]string {
	t.Helper()
	disks := map[string]map[string]string{}
	i := slices.Index(args, "--disk")
	if i < 0 {
		return disks
	}
	for _, arg := range args[i+1:] {
		if strings.HasPrefix(arg, "--") {
			break
		}
		opts := map[string]string{}
		for opt := range strings.SplitSeq(arg, ",") {
			key, value, _ := strings.Cut(opt, "=")
			opts[key] = value
		}
		disks[opts["serial"]] = opts
	}
	return disks
}

// TestEveryDiskStatesItsFormatAndOnlyServerOverlaysBack launches a VM of
// every kind and inspects each disk the engine hands Cloud Hypervisor: each
// names its format, every disk but the server's qcow2 overlays is raw with
// backing files off, and a restore takes its disks from the snapshot alone.
func TestEveryDiskStatesItsFormatAndOnlyServerOverlaysBack(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.browserSession()
	h.engine.cfg.CheckoutDisks = func(_ context.Context, path, _ string, _, _ int) (string, guest.Checkout, bool, error) {
		return filepath.Join(path, "disk.qcow2"), guest.Checkout{Key: "run-b"}, true, nil
	}
	spec := sessionSpec("run-b")
	spec.User = "1000:1000"
	spec.WorktreeHostPath, spec.WorktreeMountPath = t.TempDir(), "/workspace"
	spec.Mounts = []runtime.Mount{
		{HostPath: t.TempDir(), ContainerPath: "/aether-cache", Kind: runtime.MountCache},
		{HostPath: t.TempDir(), Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState},
	}
	if _, err := h.engine.create(ctx, "run-b", spec, options{Browser: true}); err != nil {
		t.Fatal(err)
	}
	rec, err := h.engine.load("run-b")
	if err != nil {
		t.Fatal(err)
	}
	// The checkout's git service cannot be faked, so this VM is not booted.
	full, err := h.engine.bootLaunch(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(launchDisks(t, full.Args)); got != 7 {
		t.Errorf("a session with every kind of disk has %d disks: %v", got, full.Args)
	}
	r := &Runtime{vms: h.engine}
	for _, key := range []string{"terminal:m1", "assistant:m1", "assistant-install:m1", "harness-update-m1-codex"} {
		id, err := r.Create(ctx, runtime.Spec{Image: "aether/standard:1", Command: []string{"/bin/sh"}, CreationKey: key, MemoryLimitBytes: 1 << 30})
		if err == nil {
			err = r.Start(ctx, id)
		}
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if err = r.CheckImage(ctx, "aether/member-m1:a", "1000:1000"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.engine.create(ctx, dictationVM("m1"), runtime.Spec{Image: "aether/dictation:1", Command: []string{"/engine"}, MemoryLimitBytes: 1 << 30}, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err = h.engine.start(ctx, dictationVM("m1")); err != nil {
		t.Fatal(err)
	}
	if err = h.engine.save(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if err = h.engine.restore(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if err = h.engine.stop(ctx, "run-a", time.Second); err != nil {
		t.Fatal(err)
	}

	overlays := []string{"root", "repo0", "browser"}
	launches := map[string][]string{"run-b": full.Args}
	restores := 0
	for i, s := range h.units.started {
		if slices.Contains(s.launch.Args, "--restore") {
			restores++
			if slices.Contains(s.launch.Args, "--disk") {
				t.Errorf("%s restores with disks of its own: %v", s.name, s.launch.Args)
			}
			continue
		}
		launches[fmt.Sprintf("%s#%d", s.name, i)] = s.launch.Args
	}
	if restores != 1 || len(launches) != 8 {
		t.Errorf("%d restores and boots %v; want one restore and a boot of every kind", restores, slices.Sorted(maps.Keys(launches)))
	}
	for name, args := range launches {
		disks := launchDisks(t, args)
		for _, serial := range []string{"helper", "root", "data"} {
			if disks[serial] == nil {
				t.Errorf("%s has no %s disk: %v", name, serial, args)
			}
		}
		for serial, d := range disks {
			want := map[string]string{"image_type": "raw", "backing_files": "off"}
			if d["image_type"] == "qcow2" && slices.Contains(overlays, serial) {
				want = map[string]string{"image_type": "qcow2", "backing_files": "on"}
			}
			for key, value := range want {
				if d[key] != value {
					t.Errorf("%s: the %s disk has %s=%q, want %q: %v", name, serial, key, d[key], value, d)
				}
			}
		}
	}

	rec.Data.Format = ""
	if _, err = h.engine.bootLaunch(ctx, rec); err == nil || !strings.Contains(err.Error(), "no known format") {
		t.Fatalf("launch of a VM with a disk of no format = %v", err)
	}
}
