package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestAColdBootAttachesTheDiskBuiltForTheHomesProgram(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "omp"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := sessionSpec("run-a")
	spec.Mounts = []runtime.Mount{{HostPath: home, ContainerPath: "/root"}}
	spec.Programs = []runtime.Program{{Home: home, Target: "/root", Executable: "omp"}, {Home: home, Target: "/root", Executable: "missing"}}
	h.createStarted("run-a", spec)
	h.engine.builds.Wait()
	const mount, disk = "aether.mount=program,0,/root/.local/bin/omp", "program-0.img,image_type=raw,backing_files=off,id=program-0,serial=program-0,readonly=on"
	if args := strings.Join(h.units.last().launch.Args, " "); strings.Contains(args, mount) {
		t.Errorf("the first boot, before any disk was built, mounted one:\n%s", args)
	}

	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(h.units.last().launch.Args, " ")
	for _, want := range []string{mount, disk} {
		if !strings.Contains(args, want) {
			t.Errorf("the boot after the build lacks %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "program-1") {
		t.Errorf("a program missing from the home got a disk:\n%s", args)
	}

	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.engine.path("run-a", "program-0.img")); err == nil {
		t.Error("a VM that powered off still holds its program's disk")
	}
}
