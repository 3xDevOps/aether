package vmimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAProgramDiskIsBuiltPerVersionAndKeptWhileAVMUsesIt(t *testing.T) {
	needTools(t, "mkfs.ext4", "debugfs")
	s := openStore(t, nil)
	home := t.TempDir()
	const executable, unit = "agent", ".local/lib/node_modules/@scope/agent"
	write := func(data string) {
		p := filepath.Join(home, unit, "bin", "agent.js")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("v1")
	if err := os.Symlink("bin/agent.js", filepath.Join(home, unit, "main")); err != nil {
		t.Fatal(err)
	}
	vms := t.TempDir()
	disk := func(vm string) (Disk, bool) {
		t.Helper()
		d, ok, err := s.ProgramDisk(home, executable, unit, filepath.Join(vms, vm))
		if err != nil {
			t.Fatal(err)
		}
		return d, ok
	}
	build := func() {
		t.Helper()
		if err := s.BuildProgram(home, executable, unit); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := disk("a"); ok {
		t.Fatal("a disk was found before any was built")
	}
	build()
	a, ok := disk("a")
	if !ok {
		t.Fatal("no disk after the build")
	}
	if got := debugfs(t, a.Path, "cat /program/bin/agent.js"); got != "v1" {
		t.Errorf("the disk's agent.js = %q, want v1", got)
	}
	if got := debugfs(t, a.Path, "stat /program/bin/agent.js"); !strings.Contains(got, "Mode:  0755") {
		t.Errorf("a 0700 file is not readable and executable by every user on the disk:\n%s", got)
	}
	if got := debugfs(t, a.Path, "stat /program/main"); !strings.Contains(got, `Fast link dest: "bin/agent.js"`) {
		t.Errorf("the package's symlink is not kept:\n%s", got)
	}

	write("v2")
	if _, found := disk("b"); found {
		t.Fatal("the disk of v1 was found for v2")
	}
	build()
	b, ok := disk("b")
	if !ok || debugfs(t, b.Path, "cat /program/bin/agent.js") != "v2" {
		t.Fatalf("no disk of v2 after its build: %v", ok)
	}

	pool := filepath.Join(s.programsDir(), programPool(home, executable))
	count := func() int {
		t.Helper()
		if err := s.Sweep(); err != nil {
			t.Fatal(err)
		}
		raw, _ := filepath.Glob(filepath.Join(pool, "*.raw"))
		return len(raw)
	}
	if n := count(); n != 2 {
		t.Errorf("the sweep left %d disks while a VM uses each of two, want 2", n)
	}
	if err := os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("the sweep left %d disks after the VM on v1 let go of it, want only v2's", n)
	}
	if err := os.Remove(b.Path); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("the sweep left %d disks once no VM used the newest, want it kept for 7 days", n)
	}
}

func TestAProgramOverTheLimitGetsNoDisk(t *testing.T) {
	s := openStore(t, nil)
	home := t.TempDir()
	big := filepath.Join(home, ".local", "bin", "big")
	if err := os.MkdirAll(filepath.Dir(big), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, programLimit+1); err != nil {
		t.Fatal(err)
	}
	if err := s.BuildProgram(home, "big", ".local/bin/big"); err == nil || !strings.Contains(err.Error(), "over 2 GiB") {
		t.Errorf("building a program over the limit = %v", err)
	}
	if staged, _ := os.ReadDir(s.tmpDir()); len(staged) != 0 {
		t.Errorf("a refused build left %d entries in the store's tmp", len(staged))
	}

	deep := filepath.Join(home, ".local", "lib", "node_modules", "deep")
	if err := os.MkdirAll(filepath.Join(deep, strings.Repeat("d/", programDepth+1)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.BuildProgram(home, "deep", ".local/lib/node_modules/deep"); err == nil || !strings.Contains(err.Error(), "directories or") {
		t.Errorf("building a program nested past the limit = %v", err)
	}
}
