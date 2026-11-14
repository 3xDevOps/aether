package vm

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// launchDisks returns each --disk value of a launch by its serial.
func launchDisks(t *testing.T, args []string) map[string]map[string]string {
	t.Helper()
	i := slices.Index(args, "--disk")
	if i < 0 {
		t.Fatalf("no --disk in %v", args)
	}
	disks := map[string]map[string]string{}
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

func TestEveryDiskStatesItsFormatAndOnlyServerOverlaysBack(t *testing.T) {
	h := newHarness(t)
	h.engine.cfg.CheckoutDisks = func(_ context.Context, path, _ string, _, _ int) (string, guest.Checkout, bool, error) {
		return filepath.Join(path, "disk.qcow2"), guest.Checkout{Key: "run-a"}, true, nil
	}
	h.browserSession()
	h.engine.cfg.BrowserImage = testBrowserImage
	spec := sessionSpec("run-b")
	spec.User = "1000:1000"
	spec.WorktreeHostPath, spec.WorktreeMountPath = t.TempDir(), "/workspace"
	spec.Mounts = []runtime.Mount{
		{HostPath: t.TempDir(), ContainerPath: "/aether-cache", Kind: runtime.MountCache},
		{HostPath: t.TempDir(), Subpath: "0", ContainerPath: "/root/.codex/vm-sqlite", Kind: runtime.MountState},
	}
	if _, err := h.engine.create(t.Context(), "run-b", spec, options{Browser: true}); err != nil {
		t.Fatal(err)
	}
	rec, err := h.engine.load("run-b")
	if err != nil {
		t.Fatal(err)
	}
	l, err := h.engine.bootLaunch(t.Context(), rec)
	if err != nil {
		t.Fatal(err)
	}
	disks := launchDisks(t, l.Args)
	overlays := []string{"root", "repo0", "browser"}
	for _, serial := range []string{"helper", "root", "data", "cache", "state", "repo0", "browser"} {
		d, ok := disks[serial]
		if !ok {
			t.Errorf("no %s disk in %v", serial, disks)
			continue
		}
		want := map[string]string{"image_type": "raw", "backing_files": "off"}
		if slices.Contains(overlays, serial) {
			want = map[string]string{"image_type": "qcow2", "backing_files": "on"}
		}
		for key, value := range want {
			if d[key] != value {
				t.Errorf("%s disk has %s=%q, want %q: %v", serial, key, d[key], value, d)
			}
		}
	}
	if len(disks) != 7 {
		t.Errorf("disks = %v", disks)
	}

	rec.Data.Format = ""
	if _, err = h.engine.bootLaunch(t.Context(), rec); err == nil || !strings.Contains(err.Error(), "no known format") {
		t.Fatalf("launch of a VM with a disk of no format = %v", err)
	}
}
