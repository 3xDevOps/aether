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
	"time"

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
	// Release is the Aether release a guest kernel comes from.
	Release string
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
	{Program: "nft", Package: "nftables"},
	{Program: "ip", Package: "iproute2"},
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

// InstallAll downloads every file beside its path and checks it against its
// digest, then renames them into place one after another, so a failed
// download replaces none of them. A running VM keeps the files it started
// with; the next one gets the new files. A kernel's release is recorded
// beside it, which ReleaseOf reads.
func InstallAll(ctx context.Context, files []Pinned) error {
	var staged []string
	defer func() {
		for _, tmp := range staged {
			_ = os.Remove(tmp)
		}
	}()
	for _, p := range files {
		tmp, err := p.download(ctx)
		if err != nil {
			return err
		}
		staged = append(staged, tmp)
	}
	for i, p := range files {
		if err := os.Rename(staged[i], p.Path); err != nil {
			return err
		}
		if err := p.Record(); err != nil {
			return err
		}
	}
	return nil
}

// download writes p to a new file beside its path and returns that file's
// name once its bytes match the pin. Files an interrupted download left
// there are removed first; one written to in the last ten minutes may be
// another install's, still running.
func (p Pinned) download(ctx context.Context) (string, error) {
	dir, base := filepath.Split(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stale, err := filepath.Glob(filepath.Join(dir, "."+base+".*"))
	if err != nil {
		return "", err
	}
	for _, name := range stale {
		if info, serr := os.Stat(name); serr == nil && time.Since(info.ModTime()) > 10*time.Minute {
			if err = os.Remove(name); err != nil {
				return "", err
			}
		}
	}
	f, err := os.CreateTemp(dir, "."+base+".*")
	if err != nil {
		return "", err
	}
	err = selfupdate.DownloadPinned(ctx, p.URL, f, p.Asset, nil)
	if err == nil {
		err = f.Chmod(p.Mode)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Record notes beside a release kernel which release it came from.
func (p Pinned) Record() error {
	if p.Release == "" {
		return nil
	}
	return os.WriteFile(p.Path+".release", []byte(p.Release+"\n"), 0o644)
}

// ReleaseOf is the release whose kernel Record noted at path, or "".
func ReleaseOf(path string) string {
	data, _ := os.ReadFile(path + ".release")
	return strings.TrimSpace(string(data))
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
	return Pinned{Name: "the " + tag + " guest kernel", URL: m.AssetURL(KernelAsset), Path: path, Mode: 0o644, Asset: want, Release: tag}, nil
}
