//go:build kvm

package vm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
// that the member's next session, whose cache is a qcow2 disk over that
// session-written base, gets those bytes back as data. The next session
// writes another header through its own qcow2 cache, and the session after
// it reads that back from the base promotion flattened. Deleting the last VM
// over the older base frees it at once, and a VM started again while a
// promotion reads its cache disk gets one of its own. It then saves an
// environment from a root disk over an image and again from one over a saved
// image, timing both.
func TestKVMGuestWrittenQcow2HeadersStayData(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-disks")
	secret, other := filepath.Join(src, "secret"), filepath.Join(src, "other")
	if err := os.MkdirAll(filepath.Join(src, "pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	for _, f := range []string{secret, other} {
		if err := os.WriteFile(f, []byte(strings.Repeat("host secret ", 6000)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pool := runtime.Mount{HostPath: filepath.Join(src, "pool"), ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	poolDir := filepath.Join(e.root, imagesDir, "caches", cachePool(pool))
	t.Cleanup(func() { _ = os.RemoveAll(poolDir) })
	newest := func() string {
		name, _ := os.Readlink(filepath.Join(poolDir, "current"))
		return name
	}
	// promote stops key and waits until its cache is the pool's newest and
	// a format probe would read it as qcow2 over backing.
	promote := func(key, backing string) {
		t.Helper()
		before, start := newest(), time.Now()
		if err := e.stop(t.Context(), key, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		t.Logf("stop of %s took %s", key, time.Since(start))
		for newest() == before {
			if time.Since(start) > 2*time.Minute {
				t.Fatalf("the cache of %s never became the pool's newest", key)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("the cache of %s was promoted %s after the stop began", key, time.Since(start))
		out, err := exec.Command("qemu-img", "info", "-U", "--output=json", filepath.Join(poolDir, newest())).Output()
		var info struct {
			Backing string `json:"full-backing-filename"`
		}
		if err != nil || json.Unmarshal(out, &info) != nil || info.Backing != backing {
			t.Fatalf("qemu-img info of the promoted cache = %s, %v; want a qcow2 header over %s", out, err, backing)
		}
	}
	// layered boots a session over the pool's newest cache and checks it
	// reads the header h back as data through a qcow2 disk over that base.
	layered := func(key string, h []byte) {
		t.Helper()
		took := rootSession(t, e, key, "docker.io/library/ubuntu:24.04", pool)
		t.Logf("create and start of %s over the promoted cache took %s", key, took)
		if rec, err := e.load(key); err != nil || rec.Cache.Format != "qcow2" {
			t.Errorf("%s has cache disk %+v, %v; want a qcow2 disk over the pool's newest", key, rec.Cache, err)
		}
		if got := readHeader(t, e, key, "cache"); got != fmt.Sprintf("%x", h) {
			t.Errorf("the cache of %s starts %.64s..., want the header the last session wrote", key, got)
		}
		if out, _ := vmRun(t, e, key, "cat /aether-cache/marker"); !strings.Contains(out, "kept") {
			t.Errorf("the cache of %s lost what the last session wrote: %q", key, out)
		}
	}

	took := rootSession(t, e, "run-vmdisk-a", "docker.io/library/ubuntu:24.04", pool)
	t.Logf("create and start with an empty cache took %s", took)
	vmRun(t, e, "run-vmdisk-a", "echo kept > /aether-cache/marker")
	writeHeader(t, e, "run-vmdisk-a", "cache", qcow2Header(secret))
	promote("run-vmdisk-a", secret)
	first := filepath.Join(poolDir, newest())
	layered("run-vmdisk-b", qcow2Header(secret))

	r := &Runtime{vms: e}
	const fromImage, fromSaved = "aether/vmdisk-saved:1", "aether/vmdisk-saved:2"
	t.Cleanup(func() {
		_ = e.cfg.Images.Untag(fromImage)
		_ = e.cfg.Images.Untag(fromSaved)
	})
	vmRun(t, e, "run-vmdisk-b", "echo first > /opt/saved")
	start := time.Now()
	if err := r.Commit(t.Context(), runtime.ID(idPrefix+"run-vmdisk-b"), fromImage, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("save from a root disk over an image took %s", time.Since(start))
	writeHeader(t, e, "run-vmdisk-b", "cache", qcow2Header(other))
	promote("run-vmdisk-b", other)
	layered("run-vmdisk-e", qcow2Header(other))
	if err := e.destroy(t.Context(), "run-vmdisk-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("the older cache only a removed VM read through is still on disk: %v", err)
	}
	// A VM that starts again while a promotion still reads its cache disk,
	// as the link held here does, gets a cache disk of its own that holds
	// what it wrote, and never writes the one the promotion reads.
	vmRun(t, e, "run-vmdisk-e", "echo again > /aether-cache/again")
	if err := e.stop(t.Context(), "run-vmdisk-e", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	rec, err := e.load("run-vmdisk-e")
	if err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(src, "promotion-cache")
	if err = os.Link(rec.Cache.Path, held); err != nil {
		t.Fatal(err)
	}
	stopped, err := os.ReadFile(held)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.start(t.Context(), "run-vmdisk-e"); err != nil {
		t.Fatal(err)
	}
	if out, _ := vmRun(t, e, "run-vmdisk-e", "cat /aether-cache/again && echo later > /aether-cache/later && sync"); out != "again\n" {
		t.Errorf("a VM started again during its cache's promotion lost what it wrote: %q", out)
	}
	var st syscall.Stat_t
	if err = syscall.Stat(rec.Cache.Path, &st); err != nil || st.Nlink != 1 {
		t.Errorf("a VM started again shares its cache disk with a promotion: %d links, %v", st.Nlink, err)
	}
	if got, _ := os.ReadFile(held); !bytes.Equal(got, stopped) {
		t.Error("a VM started again wrote the cache disk a promotion reads")
	}

	took = rootSession(t, e, "run-vmdisk-c", fromImage)
	t.Logf("create and start from the saved image took %s", took)
	if rec, loadErr := e.load("run-vmdisk-c"); loadErr != nil || rec.Root.Format != "qcow2" {
		t.Errorf("a session from a saved image has root disk %+v, %v; want a qcow2 disk over it", rec.Root, loadErr)
	}
	vmRun(t, e, "run-vmdisk-c", "echo second >> /opt/saved")
	start = time.Now()
	if err := r.Commit(t.Context(), runtime.ID(idPrefix+"run-vmdisk-c"), fromSaved, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("save from a root disk over a saved image took %s", time.Since(start))
	rootSession(t, e, "run-vmdisk-d", fromSaved)
	if out, _ := vmRun(t, e, "run-vmdisk-d", "cat /opt/saved"); out != "first\nsecond\n" {
		t.Errorf("an image saved from a root disk over a saved image holds %q", out)
	}
}
