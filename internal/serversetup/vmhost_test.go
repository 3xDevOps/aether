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

// Install replaces the file only with bytes that match the pin, so a bad
// download leaves the installed file, and nothing else, in the directory.
func TestPinnedInstallReplacesOnlyWithMatchingBytes(t *testing.T) {
	served := []byte("cloud-hypervisor v2")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(served) }))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "bin")
	p := Pinned{Name: "ch", URL: srv.URL, Path: filepath.Join(dir, "cloud-hypervisor"), Mode: 0o755, Asset: asset(served)}

	if ok, err := Installed(p.Path, p.Asset); ok || err != nil {
		t.Fatalf("Installed before install = %v, %v", ok, err)
	}
	if err := p.Install(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ok, err := Installed(p.Path, p.Asset); !ok || err != nil {
		t.Fatalf("Installed after install = %v, %v", ok, err)
	}
	if info, err := os.Stat(p.Path); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed file: %v, %v", info, err)
	}

	served = []byte("cloud-hypervisor v3, tampered")
	p.Asset = asset([]byte("cloud-hypervisor v3"))
	err := p.Install(t.Context())
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("install of bytes that do not match the pin = %v", err)
	}
	if got, _ := os.ReadFile(p.Path); !bytes.Equal(got, []byte("cloud-hypervisor v2")) {
		t.Errorf("a failed install changed the file to %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a failed install left %d files in %s", len(entries), dir)
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
	if err = k.Install(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, kernel) {
		t.Errorf("installed kernel = %q", got)
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
