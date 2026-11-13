//go:build kvm

package vmimage

import (
	"bufio"
	"context"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary be the VM's /init and the nested
// container's process.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "vminit-") {
		vminitChild(os.Args[1])
		return
	}
	if os.Getpid() == 1 {
		vminit()
		return
	}
	os.Exit(m.Run())
}

// TestKVMStandardImageRunsDocker boots an image as a Cloud Hypervisor VM from
// the store's disks and kernel, with this test binary standing in for the
// in-VM helper (vminit), and checks that Docker runs a container and a port mapping
// inside. It needs root, /dev/kvm, cloud-hypervisor and qemu-img, the kernel
// in AETHER_TEST_KERNEL and the image in AETHER_TEST_IMAGE (docs/testing.md).
func TestKVMStandardImageRunsDocker(t *testing.T) {
	kernel, image := os.Getenv("AETHER_TEST_KERNEL"), os.Getenv("AETHER_TEST_IMAGE")
	if kernel == "" || image == "" {
		t.Skip("set AETHER_TEST_KERNEL and AETHER_TEST_IMAGE")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	needTools(t, "cloud-hypervisor", "mkfs.ext4", "qemu-img")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()

	// The binary runs on the helper disk, which has no libraries.
	initBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := elf.Open(initBin)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(bin.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP }) {
		t.Skip("build the test binary with CGO_ENABLED=0")
	}
	_ = bin.Close()
	s, err := Open(filepath.Join(dir, "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	base, err := s.BaseDisk(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("base disk of %s built in %v, %d MiB on the host", image, time.Since(start).Round(time.Millisecond), allocated(t, base.Path)>>20)

	run := filepath.Join(dir, "run")
	if err = os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	root, err := s.NewRootDisk(ctx, base, filepath.Join(run, "root.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("root disk created in %v", time.Since(start).Round(time.Microsecond))
	data, err := s.NewDataDisk(ctx, filepath.Join(run, "data.raw"), 16<<30)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := s.HelperDisk(ctx, initBin, filepath.Join(run, "helper.raw"))
	if err != nil {
		t.Fatal(err)
	}
	kernelPath, err := s.Kernel()
	if err != nil {
		t.Fatal(err)
	}

	vm := exec.CommandContext(ctx, "cloud-hypervisor", "--kernel", kernelPath,
		"--disk", diskArg(helper, "helper"), diskArg(root, "root"), diskArg(data, "data"),
		"--cmdline", "console=ttyS0 root=/dev/vda ro rootfstype=ext4 init=/init",
		"--cpus", "boot=2", "--memory", "size=2G", "--serial", "tty", "--console", "off")
	console, err := vm.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	vm.Stderr = vm.Stdout
	start = time.Now()
	if err = vm.Start(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	pass := false
	scanner := bufio.NewScanner(console)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if !strings.Contains(line, "VMINIT:") {
			continue
		}
		t.Log(strings.TrimSpace(line))
		pass = pass || strings.Contains(line, "VMINIT: PASS")
		if strings.Contains(line, "VMINIT: dockerd ready") {
			if _, saveErr := s.SaveBase(ctx, root.Path, "aether/kvm-test:running", nil); saveErr == nil {
				t.Error("SaveBase saved the root disk of a running VM")
			} else {
				t.Logf("SaveBase while the VM runs: %v", saveErr)
			}
		}
	}
	if err = vm.Wait(); err != nil {
		t.Errorf("cloud-hypervisor: %v", err)
	}
	t.Logf("VM ran for %v; root disk now uses %d MiB on the host", time.Since(start).Round(time.Millisecond), allocated(t, root.Path)>>20)
	if !pass {
		t.Fatalf("the VM did not pass; console:\n%s", strings.Join(lines, "\n"))
	}

	start = time.Now()
	saved, err := s.SaveBase(ctx, root.Path, "aether/kvm-test:1", map[string]string{"aether.test": "1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("saved the stopped VM's root disk as a base in %v, %d MiB on the host", time.Since(start).Round(time.Millisecond), allocated(t, saved.Path)>>20)
}

func diskArg(d Disk, serial string) string {
	arg := fmt.Sprintf("path=%s,image_type=%s,serial=%s", d.Path, d.Format, serial)
	switch {
	case d.Format == FormatQcow2:
		arg += ",backing_files=on"
	case serial == "helper":
		arg += ",readonly=on"
	}
	return arg
}

func allocated(t *testing.T, path string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Blocks * 512
}
