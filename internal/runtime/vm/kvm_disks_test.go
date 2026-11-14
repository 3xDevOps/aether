//go:build kvm

package vm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// qcow2Header is the first 512 bytes of a qcow2 image of 512-byte clusters
// whose backing file is backing and whose clusters are all unallocated, so
// that whatever opens it as qcow2 reads backing. Its tables sit in the
// second 512 bytes, and ext4 leaves the first 1024 bytes of a disk unused.
func qcow2Header(backing string) []byte {
	h := make([]byte, 512)
	copy(h, "QFI\xfb")
	binary.BigEndian.PutUint32(h[4:], 2)
	binary.BigEndian.PutUint64(h[8:], 72)
	binary.BigEndian.PutUint32(h[16:], uint32(len(backing)))
	binary.BigEndian.PutUint32(h[20:], 9)
	binary.BigEndian.PutUint64(h[24:], 64<<10)
	binary.BigEndian.PutUint32(h[36:], 2)
	binary.BigEndian.PutUint64(h[40:], 512)
	binary.BigEndian.PutUint64(h[48:], 512)
	binary.BigEndian.PutUint32(h[56:], 1)
	copy(h[72:], backing)
	return h
}

// writeHeader unmounts the guest's disk with serial, which the guest
// kernel refuses to write while it is mounted, and writes h over its start.
func writeHeader(t *testing.T, e *engine, key, serial string, h []byte) {
	t.Helper()
	var escaped strings.Builder
	for _, b := range h {
		fmt.Fprintf(&escaped, "\\%03o", b)
	}
	script := fmt.Sprintf(`for f in /sys/block/vd*/serial; do [ "$(cat $f)" = %s ] && d=/dev/$(basename $(dirname $f)); done
findmnt -rn -S $d -o TARGET | xargs -r -n1 umount && printf '%s' | dd of=$d bs=512 count=1 conv=notrunc,fsync status=none && echo wrote $d`, serial, escaped.String())
	if out, code := vmRun(t, e, key, script); code != 0 || !strings.Contains(out, "wrote /dev/vd") {
		t.Fatalf("write a qcow2 header to %s's %s disk: %d %q", key, serial, code, out)
	}
}

// readHeader is the first 512 bytes of the guest's disk with serial.
func readHeader(t *testing.T, e *engine, key, serial string) string {
	t.Helper()
	out, code := vmRun(t, e, key, fmt.Sprintf(`for f in /sys/block/vd*/serial; do [ "$(cat $f)" = %s ] && d=/dev/$(basename $(dirname $f)); done
dd if=$d bs=512 count=1 status=none | od -An -v -tx1 | tr -d ' \n'`, serial))
	if code != 0 {
		t.Fatalf("read %s's %s disk: %d %q", key, serial, code, out)
	}
	return out
}

func rootSession(t *testing.T, e *engine, key, image string, mounts ...runtime.Mount) time.Duration {
	t.Helper()
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: image, CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20, Mounts: mounts,
	}
	start := time.Now()
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// TestKVMGuestWrittenQcow2HeadersStayData has a session write a qcow2
// header that names a host file over the start of its cache disk, and checks
// that the member's next session gets those bytes back as data, through
// cache promotion. It then saves an environment from a qcow2 root disk and
// again from the raw root disk a saved image gives, timing both.
func TestKVMGuestWrittenQcow2HeadersStayData(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-disks")
	secret := filepath.Join(src, "secret")
	if err := os.MkdirAll(filepath.Join(src, "pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	if err := os.WriteFile(secret, []byte(strings.Repeat("host secret ", 6000)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := qcow2Header(secret)
	want := fmt.Sprintf("%x", h)
	pool := runtime.Mount{HostPath: filepath.Join(src, "pool"), ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	poolDir := filepath.Join(e.root, imagesDir, "caches", cachePool(pool))
	t.Cleanup(func() { _ = os.RemoveAll(poolDir) })
	newest := func() string {
		name, _ := os.Readlink(filepath.Join(poolDir, "current"))
		return name
	}

	took := rootSession(t, e, "run-vmdisk-a", "docker.io/library/ubuntu:24.04", pool)
	t.Logf("create and start with an empty cache took %s", took)
	vmRun(t, e, "run-vmdisk-a", "echo kept > /aether-cache/marker")
	writeHeader(t, e, "run-vmdisk-a", "cache", h)
	start := time.Now()
	if err := e.stop(t.Context(), "run-vmdisk-a", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop, with the cache copied for promotion, took %s", time.Since(start))
	for newest() == "" {
		if time.Since(start) > 2*time.Minute {
			t.Fatal("the stopped session's cache never became the pool's newest")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("the cache was promoted %s after the stop began", time.Since(start))
	out, err := exec.Command("qemu-img", "info", "-U", "--output=json", filepath.Join(poolDir, newest())).Output()
	var info struct {
		Backing string `json:"full-backing-filename"`
	}
	if err != nil || json.Unmarshal(out, &info) != nil || info.Backing != secret {
		t.Fatalf("qemu-img info of the promoted cache = %s, %v; want a qcow2 header over %s", out, err, secret)
	}
	t.Logf("a format probe would read the promoted cache as qcow2 over %s", secret)

	took = rootSession(t, e, "run-vmdisk-b", "docker.io/library/ubuntu:24.04", pool)
	t.Logf("create and start over the promoted cache took %s", took)
	if got := readHeader(t, e, "run-vmdisk-b", "cache"); got != want {
		t.Errorf("the next session's cache starts %.64s..., want the header the last one wrote", got)
	}
	if out, _ := vmRun(t, e, "run-vmdisk-b", "cat /aether-cache/marker"); !strings.Contains(out, "kept") {
		t.Errorf("the next session's cache lost what the last one wrote: %q", out)
	}

	r := &Runtime{vms: e}
	const fromQcow2, fromRaw = "aether/vmdisk-saved:1", "aether/vmdisk-saved:2"
	t.Cleanup(func() {
		_ = e.cfg.Images.Untag(fromQcow2)
		_ = e.cfg.Images.Untag(fromRaw)
	})
	vmRun(t, e, "run-vmdisk-b", "echo first > /opt/saved")
	start = time.Now()
	if err = r.Commit(t.Context(), runtime.ID(idPrefix+"run-vmdisk-b"), fromQcow2, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("save from a qcow2 root disk took %s", time.Since(start))
	took = rootSession(t, e, "run-vmdisk-c", fromQcow2)
	t.Logf("create and start from the saved image took %s", took)
	if rec, loadErr := e.load("run-vmdisk-c"); loadErr != nil || rec.Root.Format != "raw" {
		t.Errorf("a session from a saved image has root disk %+v, %v; want a raw copy", rec.Root, loadErr)
	}
	vmRun(t, e, "run-vmdisk-c", "echo second >> /opt/saved")
	start = time.Now()
	if err = r.Commit(t.Context(), runtime.ID(idPrefix+"run-vmdisk-c"), fromRaw, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("save from a raw root disk took %s", time.Since(start))
	rootSession(t, e, "run-vmdisk-d", fromRaw)
	if out, _ := vmRun(t, e, "run-vmdisk-d", "cat /opt/saved"); out != "first\nsecond\n" {
		t.Errorf("an image saved from a raw root disk holds %q", out)
	}
}
