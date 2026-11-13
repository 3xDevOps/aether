package serversetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// KernelAsset is the release asset holding the guest kernel session VMs boot.
const KernelAsset = "aether-vmlinux-x86_64"

// Pinned is a file fetched from a fixed URL and checked against its digest.
type Pinned struct {
	Name  string
	URL   string
	Path  string
	Mode  fs.FileMode
	Asset selfupdate.Asset
}

// CloudHypervisor is the release session VMs are tested on; the kvm CI job
// pins the same one. /usr/local/bin precedes /usr/bin on systemd's PATH, so
// it wins over a distribution package.
var CloudHypervisor = Pinned{
	Name: "Cloud Hypervisor v54.1",
	URL:  "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.1/cloud-hypervisor-static",
	Path: "/usr/local/bin/cloud-hypervisor",
	Mode: 0o755,
	Asset: selfupdate.Asset{
		SHA256: "f9ffb9646783233d1f0fb09e77db1fca66499d1041e63bc011b90683c0d49933",
		Size:   7344872,
	},
}

// VMTool is a program session VMs need and the Debian package that ships it.
type VMTool struct {
	Program string
	Package string
}

// VMTools are the host programs the VM engine and image store run.
var VMTools = []VMTool{
	{Program: "qemu-img", Package: "qemu-utils"},
	{Program: "mkfs.ext4", Package: "e2fsprogs"},
	{Program: "/usr/libexec/virtiofsd", Package: "virtiofsd"},
	{Program: "iptables", Package: "iptables"},
}

// CheckKVM opens device read-write, as Cloud Hypervisor does.
func CheckKVM(device string) error {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("session VMs need KVM: %w; run the server on bare metal or on a cloud VM with nested virtualization (docs/install.md#hosts-for-vm-sessions)", err)
	}
	return f.Close()
}

// MissingVMTools returns the VMTools this host cannot run.
func MissingVMTools() []VMTool {
	var missing []VMTool
	for _, tool := range VMTools {
		if _, err := exec.LookPath(tool.Program); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

// InstallPackages installs Debian packages with apt-get. The error carries
// apt-get's output.
func InstallPackages(ctx context.Context, packages []string) error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return fmt.Errorf("install %s with your package manager: this host has no apt-get", strings.Join(packages, " "))
	}
	for _, args := range [][]string{
		{"update", "-q"},
		append([]string{"install", "-y", "-q", "--no-install-recommends"}, packages...),
	} {
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "apt-get", args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("apt-get %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
		}
	}
	return nil
}

// Installed reports whether path holds exactly want.
func Installed(path string, want selfupdate.Asset) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return n == want.Size && hex.EncodeToString(h.Sum(nil)) == want.SHA256, nil
}

// Install downloads p beside its path and renames it into place once it
// matches its digest, so a running VM keeps the file it started with and
// the next one gets the new file.
func (p Pinned) Install(ctx context.Context) error {
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(p.Path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if err = selfupdate.DownloadPinned(ctx, p.URL, f, p.Asset, nil); err != nil {
		return err
	}
	if err = f.Chmod(p.Mode); err == nil {
		err = f.Close()
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), p.Path)
}

// ReleaseKernel is tag's guest kernel, from the release's signed manifest,
// to be installed at path.
func ReleaseKernel(ctx context.Context, baseURL, tag, path string) (Pinned, error) {
	m, err := selfupdate.FetchManifestFrom(ctx, baseURL, tag)
	if err != nil {
		return Pinned{}, err
	}
	want, ok := m.Assets[KernelAsset]
	if !ok {
		return Pinned{}, fmt.Errorf("release %s does not publish %s", tag, KernelAsset)
	}
	return Pinned{Name: "the " + tag + " guest kernel", URL: m.AssetURL(KernelAsset), Path: path, Mode: 0o644, Asset: want}, nil
}
