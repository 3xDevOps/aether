package serversetup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
)

func asset(body []byte) selfupdate.Asset {
	sum := sha256.Sum256(body)
	return selfupdate.Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

// InstallAll replaces files only once every download matches its pin, so a
// bad one, even of the right length, leaves every installed file, and
// nothing else, in place.
func TestInstallAllReplacesNothingUnlessEveryFileMatches(t *testing.T) {
	served := map[string][]byte{"/ch": []byte("cloud-hypervisor v2"), "/kernel": []byte("vmlinux v2")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(served[r.URL.Path]) }))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "bin")
	files := []Pinned{
		{Name: "ch", URL: srv.URL + "/ch", Path: filepath.Join(dir, "cloud-hypervisor"), Mode: 0o755, Asset: asset(served["/ch"])},
		{Name: "kernel", URL: srv.URL + "/kernel", Path: filepath.Join(dir, "vmlinux"), Mode: 0o644, Asset: asset(served["/kernel"]), Release: "v1.0.0"},
	}
	if err := InstallAll(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if ok, err := Installed(f.Path, f.Asset); !ok || err != nil {
			t.Fatalf("%s after install: %v, %v", f.Name, ok, err)
		}
	}
	if info, err := os.Stat(files[0].Path); err != nil || info.Mode().Perm() != 0o755 || ReleaseOf(files[1].Path) != "v1.0.0" {
		t.Fatalf("installed mode %v, %v; kernel release %q", info, err, ReleaseOf(files[1].Path))
	}

	served = map[string][]byte{"/ch": []byte("cloud-hypervisor v3"), "/kernel": []byte("vmlinux v3, tampered")}
	files[0].Asset = asset(served["/ch"])
	files[1].Asset, files[1].Release = asset([]byte("vmlinux v3, expected")), "v1.0.1"
	err := InstallAll(t.Context(), files)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("install with one download that does not match its pin = %v", err)
	}
	for path, want := range map[string]string{files[0].Path: "cloud-hypervisor v2", files[1].Path: "vmlinux v2", files[1].Path + ".release": "v1.0.0\n"} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("a failed install changed %s to %q", path, got)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Errorf("a failed install left %d files in %s, want the 3 it found", len(entries), dir)
	}
}

func TestReleaseKernelComesFromTheSignedManifest(t *testing.T) {
	kernel := []byte("vmlinux")
	rel := releasetest.Serve(t, "v1.2.3", map[string][]byte{KernelAsset: kernel})
	path := filepath.Join(t.TempDir(), "lib", "vmlinux")
	k, err := ReleaseKernel(t.Context(), rel.URL, "v1.2.3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err = InstallAll(t.Context(), []Pinned{k}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, kernel) || ReleaseOf(path) != "v1.2.3" {
		t.Errorf("installed kernel = %q from release %q", got, ReleaseOf(path))
	}

	without := releasetest.Serve(t, "v1.2.3", map[string][]byte{"aether-server-linux-amd64": []byte("server")})
	if _, err = ReleaseKernel(t.Context(), without.URL, "v1.2.3", path); err == nil || !strings.Contains(err.Error(), "does not publish "+KernelAsset) {
		t.Errorf("release without a kernel = %v", err)
	}
}

func TestCheckKVMShowsTheRealError(t *testing.T) {
	err := CheckKVM(filepath.Join(t.TempDir(), "kvm"))
	if err == nil || !strings.Contains(err.Error(), "no such file or directory") || !strings.Contains(err.Error(), "docs/install.md#hosts-for-vm-sessions") {
		t.Errorf("CheckKVM of a missing device = %v", err)
	}
}
