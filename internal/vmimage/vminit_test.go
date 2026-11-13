//go:build kvm

package vmimage

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// vminit stands in for the in-VM helper: the test binary boots as /init on
// the helper disk, switches into the root disk, starts dockerd on the data
// disk, runs a container and a port mapping, and powers off. Its lines start
// with VMINIT so the test can find them on the serial console.
func vminit() {
	if err := vminitRun(); err != nil {
		fmt.Println("VMINIT: FAIL", err)
	} else {
		fmt.Println("VMINIT: PASS")
	}
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

// vminitChild runs inside the nested container.
func vminitChild(mode string) {
	switch mode {
	case "vminit-hello":
		cg, _ := os.ReadFile("/proc/self/cgroup")
		fmt.Printf("hello from pid %d in cgroup %s", os.Getpid(), cg)
	case "vminit-serve":
		_ = http.ListenAndServe(":80", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "served")
		}))
	}
}

func vmStep(name string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Printf("VMINIT: %s at %s\n", name, vmUptime())
	return nil
}

func vmUptime() string {
	b, _ := os.ReadFile("/proc/uptime")
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "?"
	}
	return f[0] + "s"
}

func vminitRun() error {
	var err error
	for _, m := range []struct{ src, dst, fs string }{{"proc", "/proc", "proc"}, {"sysfs", "/sys", "sysfs"}} {
		if err = unix.Mount(m.src, m.dst, m.fs, 0, ""); err != nil {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}
	root, err := vmDiskBySerial("root")
	if err != nil {
		return err
	}
	data, err := vmDiskBySerial("data")
	if err != nil {
		return err
	}
	if err = unix.Mount(root, "/mnt", "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount root disk: %w", err)
	}
	for _, d := range []string{"/dev", "/proc", "/sys"} {
		if err = os.MkdirAll("/mnt"+d, 0o755); err != nil {
			return err
		}
		if err = unix.Mount(d, "/mnt"+d, "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move %s: %w", d, err)
		}
	}
	if err = unix.Chdir("/mnt"); err != nil {
		return err
	}
	if err = unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err = unix.Chroot("."); err != nil {
		return err
	}
	if err = vmStep("switched into the root disk", unix.Chdir("/")); err != nil {
		return err
	}
	if info, statErr := os.Stat("/usr/bin/sudo"); statErr == nil {
		fmt.Printf("VMINIT: /usr/bin/sudo is %v\n", info.Mode())
	}
	mounts := []struct{ src, dst, fs string }{
		{"devpts", "/dev/pts", "devpts"}, {"tmpfs", "/dev/shm", "tmpfs"}, {"tmpfs", "/run", "tmpfs"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2"}, {data, "/var/lib/docker", "ext4"},
	}
	for _, m := range mounts {
		if err = os.MkdirAll(m.dst, 0o755); err != nil {
			return err
		}
		if err = unix.Mount(m.src, m.dst, m.fs, 0, ""); err != nil {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}
	if err = vmLoUp(); err != nil {
		return err
	}
	if err = os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"); err != nil {
		return err
	}
	log, err := os.Create("/run/dockerd.log")
	if err != nil {
		return err
	}
	dockerd := exec.Command("dockerd")
	dockerd.Stdout, dockerd.Stderr = log, log
	if err = dockerd.Start(); err != nil {
		return fmt.Errorf("start dockerd: %w", err)
	}
	if err = vmStep("dockerd ready", vmWaitDocker()); err != nil {
		return err
	}
	if err = vmImportSelf(); err != nil {
		return err
	}
	out, err := exec.Command("docker", "run", "--rm", "aether/vminit:test", "/vminit", "vminit-hello").CombinedOutput()
	if err = vmStep("docker run: "+strings.TrimSpace(string(out)), err); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	if out, runErr := exec.Command("docker", "run", "-d", "-p", "8080:80", "aether/vminit:test", "/vminit", "vminit-serve").CombinedOutput(); runErr != nil {
		return fmt.Errorf("docker run -p: %w: %s", runErr, out)
	}
	body, err := vmGet("http://127.0.0.1:8080/")
	if err = vmStep("port mapping answered "+body, err); err != nil {
		return err
	}
	nat, err := exec.Command("iptables", "-t", "nat", "-S", "DOCKER").CombinedOutput()
	if err = vmStep("iptables nat DOCKER chain has "+fmt.Sprint(strings.Count(string(nat), "\n"))+" rules", err); err != nil {
		return fmt.Errorf("%w: %s", err, nat)
	}
	info, err := exec.Command("docker", "info", "--format", "cgroup v{{.CgroupVersion}}, storage {{.Driver}}").CombinedOutput()
	return vmStep("docker info: "+strings.TrimSpace(string(info)), err)
}

func vmDiskBySerial(serial string) (string, error) {
	paths, _ := filepath.Glob("/sys/block/vd*/serial")
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil && strings.TrimSpace(string(b)) == serial {
			return "/dev/" + filepath.Base(filepath.Dir(p)), nil
		}
	}
	return "", fmt.Errorf("no disk with serial %s", serial)
}

func vmLoUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	ifr.SetUint16(unix.IFF_UP | unix.IFF_LOOPBACK | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func vmWaitDocker() error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := exec.Command("docker", "info").Run()
		if err == nil || time.Now().After(deadline) {
			if err != nil {
				log, _ := os.ReadFile("/run/dockerd.log")
				return fmt.Errorf("%w: %s", err, log)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// importSelf makes an image holding only this static binary.
func vmImportSelf() error {
	self, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err = tw.WriteHeader(&tar.Header{Name: "vminit", Mode: 0o755, Size: int64(len(self))}); err != nil {
		return err
	}
	if _, err = tw.Write(self); err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	cmd := exec.Command("docker", "import", "-", "aether/vminit:test")
	cmd.Stdin = &buf
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker import: %w: %s", err, out)
	}
	return nil
}

func vmGet(url string) (string, error) {
	var err error
	for range 50 {
		var resp *http.Response
		if resp, err = http.Get(url); err == nil {
			b, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return string(b), readErr
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", err
}
