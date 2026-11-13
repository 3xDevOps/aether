package vmimage

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s: %v", tool, err)
		}
	}
}

// layer builds an image layer from tar entries owned by the test's user.
func layer(t *testing.T, entries ...entry) v1.Layer {
	t.Helper()
	for i := range entries {
		entries[i].uid, entries[i].gid = os.Geteuid(), os.Getegid()
	}
	data, err := io.ReadAll(tarOf(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func testImage(t *testing.T, arch string, layers ...v1.Layer) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.ConfigFile(img, &v1.ConfigFile{
		OS: "linux", Architecture: arch,
		Config: v1.Config{
			User: "1000:1000", Env: []string{"PATH=/usr/bin", "LANG=C.UTF-8"}, WorkingDir: "/workspace",
			Labels: map[string]string{"org.example": "base", "kept": "yes"},
		},
		RootFS: v1.RootFS{Type: "layers", DiffIDs: diffIDs(t, layers)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func diffIDs(t *testing.T, layers []v1.Layer) []v1.Hash {
	var ids []v1.Hash
	for _, l := range layers {
		id, err := l.DiffID()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// standardLayers is a two-layer image whose upper layer deletes a file,
// replaces a directory and hard-links across layers.
func standardLayers(t *testing.T) []v1.Layer {
	return []v1.Layer{
		layer(t,
			entry{name: "etc", typ: tar.TypeDir, mode: 0o755},
			entry{name: "etc/hostname", body: "lower"},
			entry{name: "etc/gone", body: "deleted above"},
			entry{name: "opq", typ: tar.TypeDir, mode: 0o755},
			entry{name: "opq/old", body: "hidden above"},
			entry{name: "usr/bin/tool", body: "tool", mode: 0o4755},
		),
		layer(t,
			entry{name: "etc/.wh.gone"},
			entry{name: "etc/hostname", body: "upper"},
			entry{name: "opq/.wh..wh..opq"},
			entry{name: "opq/new", body: "fresh"},
			entry{name: "usr/bin/tool-alias", typ: tar.TypeLink, link: "usr/bin/tool"},
		),
	}
}

type countingRegistry struct {
	handler http.Handler
	mu      sync.Mutex
	gets    map[string]int
}

func (c *countingRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		c.mu.Lock()
		c.gets[r.URL.Path]++
		c.mu.Unlock()
	}
	c.handler.ServeHTTP(w, r)
}

func (c *countingRegistry) blobGets(digest v1.Hash) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets["/v2/test/image/blobs/"+digest.String()]
}

// push serves img from a fresh registry and returns its reference and a
// function that stops the registry.
func push(t *testing.T, img v1.Image) (string, *countingRegistry, func()) {
	t.Helper()
	reg := &countingRegistry{handler: registry.New(registry.Logger(log.New(io.Discard, "", 0))), gets: map[string]int{}}
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	ref := strings.TrimPrefix(srv.URL, "http://") + "/test/image:v1"
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(parsed, img); err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	clear(reg.gets)
	reg.mu.Unlock()
	return ref, reg, srv.Close
}

func openStore(t *testing.T, docker DockerImages) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "images"), "/nonexistent/vmlinux", docker)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// debugfs runs one debugfs request against an ext4 image file.
func debugfs(t *testing.T, disk, request string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command("debugfs", "-R", request, disk)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("debugfs %q: %v: %s", request, err, stderr.String())
	}
	// A failed lookup goes to stderr, after the version banner.
	_, rest, _ := strings.Cut(stderr.String(), "\n")
	return string(out) + rest
}

func label(t *testing.T, disk string) string {
	t.Helper()
	for _, line := range strings.Split(debugfs(t, disk, "stats"), "\n") {
		if v, ok := strings.CutPrefix(line, "Filesystem volume name:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("no volume name in debugfs stats of %s", disk)
	return ""
}

func nlink(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Nlink
}

func TestStoreBaseRootSaveSweep(t *testing.T) {
	needTools(t, "mkfs.ext4", "debugfs", "qemu-img", "qemu-io")
	ctx := context.Background()
	layers := standardLayers(t)
	ref, reg, stopRegistry := push(t, testImage(t, goruntime.GOARCH, layers...))
	s := openStore(t, nil)

	var wg sync.WaitGroup
	bases := make([]Base, 4)
	errs := make([]error, 4)
	for i := range bases {
		wg.Go(func() { bases[i], errs[i] = s.BaseDisk(ctx, ref) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("BaseDisk #%d: %v", i, err)
		}
		if bases[i].ID != bases[0].ID || bases[i].Path != bases[0].Path {
			t.Fatalf("BaseDisk #%d = %+v, want %+v", i, bases[i], bases[0])
		}
	}
	base := bases[0]
	for _, l := range layers {
		digest, err := l.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if got := reg.blobGets(digest); got != 1 {
			t.Errorf("four concurrent first uses fetched layer %s %d times, want once", digest, got)
		}
	}
	img := testImage(t, goruntime.GOARCH, layers...)
	digest, _ := img.ConfigName()
	if base.ID != digest.String() {
		t.Errorf("ID = %s, want the config digest %s", base.ID, digest)
	}
	wantCfg := ImageConfig{User: "1000:1000", Env: []string{"PATH=/usr/bin", "LANG=C.UTF-8"}, WorkingDir: "/workspace",
		Labels: map[string]string{"org.example": "base", "kept": "yes"}}
	if b, w := mustJSON(t, base.Config), mustJSON(t, wantCfg); b != w {
		t.Errorf("Config = %s, want %s", b, w)
	}

	if got := label(t, base.Path); got != "aether-root" {
		t.Errorf("base label = %q", got)
	}
	if info, _ := os.Stat(base.Path); info.Size() != rootSize || info.Mode().Perm() != 0o444 {
		t.Errorf("base disk is %d bytes, mode %v; want %d, read-only", info.Size(), info.Mode(), int64(rootSize))
	}
	if got := debugfs(t, base.Path, "cat /etc/hostname"); got != "upper" {
		t.Errorf("/etc/hostname = %q, want the upper layer's", got)
	}
	for _, gone := range []string{"/etc/gone", "/opq/old"} {
		if out := debugfs(t, base.Path, "stat "+gone); !strings.Contains(out, "not found") {
			t.Errorf("%s survived a whiteout: %s", gone, out)
		}
	}
	if got := debugfs(t, base.Path, "cat /opq/new"); got != "fresh" {
		t.Errorf("/opq/new = %q", got)
	}
	tool := debugfs(t, base.Path, "stat /usr/bin/tool")
	if !strings.Contains(tool, "Mode:  04755") || !strings.Contains(tool, "Links: 2") {
		t.Errorf("/usr/bin/tool lost its mode or hard link:\n%s", tool)
	}

	// A tag resolves locally from now on, even without the registry.
	stopRegistry()
	if again, err := s.BaseDisk(ctx, ref); err != nil || again.Path != base.Path {
		t.Fatalf("BaseDisk again = %+v, %v", again, err)
	}

	runDir := t.TempDir()
	rootPath := filepath.Join(runDir, "root.qcow2")
	root, err := s.NewRootDisk(ctx, base, rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if root != (Disk{Path: rootPath, Format: FormatQcow2}) {
		t.Errorf("root disk = %+v", root)
	}
	var info struct {
		Format      string `json:"format"`
		VirtualSize int64  `json:"virtual-size"`
		Backing     string `json:"full-backing-filename"`
	}
	out, err := exec.Command("qemu-img", "info", "--output=json", rootPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out, &info); err != nil {
		t.Fatal(err)
	}
	if info.Format != "qcow2" || info.VirtualSize != rootSize || info.Backing != rootPath+".base" {
		t.Errorf("qemu-img info = %+v", info)
	}
	if got := nlink(t, base.Path); got != 2 {
		t.Errorf("base disk has %d links, want 2 (store and root disk)", got)
	}
	// A cold start rebuilds the root disk in place.
	if _, err = s.NewRootDisk(ctx, base, rootPath); err != nil {
		t.Fatalf("NewRootDisk over an existing disk: %v", err)
	}
	if got := nlink(t, base.Path); got != 2 {
		t.Errorf("after a rebuild the base disk has %d links, want 2", got)
	}

	// Writes to the root disk reach the saved base; ext4 leaves its first
	// 1024 bytes unused.
	if out, ioErr := exec.Command("qemu-io", "-c", "write -P 0xab 0 512", rootPath).CombinedOutput(); ioErr != nil {
		t.Fatalf("qemu-io: %v: %s", ioErr, out)
	}
	saved, err := s.SaveBase(ctx, rootPath, "aether/member-x:1", map[string]string{"kept": "replaced", "aether.saved": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.ID, "local:") || saved.Path == base.Path {
		t.Errorf("saved base = %+v", saved)
	}
	if got := saved.Config.Labels; got["kept"] != "replaced" || got["aether.saved"] != "1" || got["org.example"] != "base" {
		t.Errorf("saved labels = %v", got)
	}
	if base.Config.Labels["kept"] != "yes" {
		t.Errorf("SaveBase changed the parent's labels: %v", base.Config.Labels)
	}
	if saved.Config.User != "1000:1000" || saved.Config.WorkingDir != "/workspace" {
		t.Errorf("saved config = %+v", saved.Config)
	}
	head := make([]byte, 512)
	f, err := os.Open(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadFull(f, head)
	_ = f.Close()
	if err != nil || !bytes.Equal(head, bytes.Repeat([]byte{0xab}, 512)) {
		t.Errorf("saved base does not carry the root disk's write: %x, %v", head[:8], err)
	}
	if got := debugfs(t, saved.Path, "cat /etc/hostname"); got != "upper" {
		t.Errorf("saved /etc/hostname = %q", got)
	}
	if looked, err := s.LookupBase("aether/member-x:1"); err != nil || looked.ID != saved.ID {
		t.Errorf("LookupBase(saved) = %+v, %v", looked, err)
	}

	if err := s.Tag("aether/member-x:1", "aether/member-x:2"); err != nil {
		t.Fatal(err)
	}
	if tags, err := s.Tags("aether/member-x"); err != nil || !slices.Equal(tags, []string{"aether/member-x:1", "aether/member-x:2"}) {
		t.Errorf("Tags = %v, %v", tags, err)
	}
	if err := s.Tag("aether/missing:1", "aether/x:1"); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("Tag of a missing source = %v, want ErrNotFound", err)
	}

	// Untagged bases go, unless a root disk still links them.
	for _, tag := range []string{ref, "aether/member-x:1", "aether/member-x:2", "aether/never-tagged:1"} {
		if err := s.Untag(tag); err != nil {
			t.Fatalf("Untag(%s): %v", tag, err)
		}
	}
	if _, err := os.Stat(saved.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("untagged, unlinked saved base survived: %v", err)
	}
	if _, err := os.Stat(base.Path); err != nil {
		t.Errorf("base disk a root disk links was swept: %v", err)
	}
	if _, err := s.LookupBase(ref); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("LookupBase after Untag = %v, want ErrNotFound", err)
	}
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(base.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("base disk outlived its last root disk and tag: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fakeDocker struct {
	images map[string]v1.Image
	saves  atomic.Int64
}

func (d *fakeDocker) SaveImage(_ context.Context, ref string) (io.ReadCloser, error) {
	img, ok := d.images[ref]
	if !ok {
		return nil, errors.New("no such image: " + ref)
	}
	d.saves.Add(1)
	tag, err := name.ParseReference(ref)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarball.Write(tag, img, pw)) }()
	return pr, nil
}

func TestStoreLocalImagesComeFromDocker(t *testing.T) {
	needTools(t, "mkfs.ext4", "debugfs")
	ctx := context.Background()
	docker := &fakeDocker{images: map[string]v1.Image{
		"aether/member-a:1":   testImage(t, goruntime.GOARCH, standardLayers(t)...),
		"aether/other-arch:1": testImage(t, "riscv64", standardLayers(t)...),
	}}
	s := openStore(t, docker)
	base, err := s.BaseDisk(ctx, "aether/member-a:1")
	if err != nil {
		t.Fatal(err)
	}
	if got := debugfs(t, base.Path, "cat /etc/hostname"); got != "upper" {
		t.Errorf("/etc/hostname = %q", got)
	}
	if entries, _ := os.ReadDir(s.tmpDir()); len(entries) != 0 {
		t.Errorf("tmp holds %d leftovers after a build", len(entries))
	}
	if _, err := s.BaseDisk(ctx, "aether/member-a:1"); err != nil || docker.saves.Load() != 1 {
		t.Errorf("second BaseDisk: %v, %d docker saves, want 1", err, docker.saves.Load())
	}
	if _, err := s.BaseDisk(ctx, "aether/other-arch:1"); err == nil || !strings.Contains(err.Error(), "image is for linux/riscv64") {
		t.Errorf("BaseDisk of another architecture = %v", err)
	}
	if _, err := s.BaseDisk(ctx, "aether/missing:1"); err == nil || !strings.Contains(err.Error(), "no such image") {
		t.Errorf("BaseDisk of an image Docker lacks = %v", err)
	}
	if _, err := openStore(t, nil).BaseDisk(ctx, "aether/member-a:1"); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("BaseDisk of a local image without Docker = %v, want ErrNotFound", err)
	}
}

func TestStoreDataDisk(t *testing.T) {
	needTools(t, "mkfs.ext4", "debugfs")
	s := openStore(t, nil)
	path := filepath.Join(t.TempDir(), "data.raw")
	disk, err := s.NewDataDisk(context.Background(), path, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	if disk != (Disk{Path: path, Format: FormatRaw}) {
		t.Errorf("data disk = %+v", disk)
	}
	if got := label(t, path); got != "aether-data" {
		t.Errorf("data label = %q", got)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	if st.Size != 2<<30 || st.Blocks*512 > 128<<20 {
		t.Errorf("data disk is %d bytes using %d on the host, want 2 GiB sparse", st.Size, st.Blocks*512)
	}
	if _, err := s.NewDataDisk(context.Background(), path, 2<<30); err == nil {
		t.Error("NewDataDisk replaced an existing data disk")
	}
}

func TestStoreHelperDisk(t *testing.T) {
	needTools(t, "mkfs.ext4", "debugfs")
	ctx := context.Background()
	s := openStore(t, nil)
	bin := filepath.Join(t.TempDir(), "aether-internal")
	if err := os.WriteFile(bin, []byte("helper v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	a, err := s.HelperDisk(ctx, bin, filepath.Join(runs, "a.helper"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.HelperDisk(ctx, bin, filepath.Join(runs, "b.helper"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Format != FormatRaw || nlink(t, a.Path) != 3 {
		t.Errorf("helper disks %+v, %+v: want one raw disk with three links", a, b)
	}
	if got := debugfs(t, a.Path, "cat /init"); got != "helper v1" {
		t.Errorf("/init = %q", got)
	}
	if out := debugfs(t, a.Path, "stat /init"); !strings.Contains(out, "Mode:  0755") {
		t.Errorf("/init is not executable:\n%s", out)
	}
	for _, dir := range []string{"/dev", "/proc", "/sys", "/mnt"} {
		if out := debugfs(t, a.Path, "stat "+dir); !strings.Contains(out, "Type: directory") {
			t.Errorf("helper disk lacks %s:\n%s", dir, out)
		}
	}
	if err = os.WriteFile(bin, []byte("helper v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := s.HelperDisk(ctx, bin, filepath.Join(runs, "c.helper"))
	if err != nil {
		t.Fatal(err)
	}
	if got := debugfs(t, c.Path, "cat /init"); got != "helper v2" {
		t.Errorf("new helper /init = %q", got)
	}
	if err := os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(s.helpersDir()); len(entries) != 2 {
		t.Errorf("sweep left %d helper disks, want both still linked", len(entries))
	}
	if err := errors.Join(os.Remove(b.Path), os.Remove(c.Path), s.Sweep()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(s.helpersDir()); len(entries) != 0 {
		t.Errorf("sweep left %d unlinked helper disks", len(entries))
	}
}

func TestStoreKernel(t *testing.T) {
	dir := t.TempDir()
	kernel := filepath.Join(dir, "vmlinux")
	s, err := Open(filepath.Join(dir, "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Kernel(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Kernel() without a kernel = %v", err)
	}
	if err := os.WriteFile(kernel, []byte("ELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Kernel(); err != nil || got != kernel {
		t.Errorf("Kernel() = %q, %v", got, err)
	}
}
