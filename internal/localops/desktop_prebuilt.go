package localops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// ErrNoPrebuiltDesktop reports a release that publishes no prebuilt desktop
// app for this machine, which is every release before the zip existed.
var ErrNoPrebuiltDesktop = errors.New("no prebuilt desktop app")

// maxDesktopTree bounds what a desktop zip may expand to. The unpacked
// Windows app is around 250 MB.
const maxDesktopTree = 1 << 30

// desktopExe is the executable a Windows desktop app carries at its root,
// named by electron-builder.yml's win.executableName.
const desktopExe = "aether-desktop.exe"

// desktopDownloadPattern names a desktop app download in the build
// directory.
const desktopDownloadPattern = ".aether-desktop-*.zip"

// DownloadDesktop fetches tag's signed manifest from baseURL and downloads
// the prebuilt Windows desktop app for goarch into dir, verified against
// the size and sha256 the manifest lists. The caller holds
// LockDesktopBuild on dir and removes the returned zip. A release whose
// manifest lists no such app is ErrNoPrebuiltDesktop.
func DownloadDesktop(ctx context.Context, baseURL, tag, goarch, dir string, out io.Writer) (string, error) {
	m, err := selfupdate.FetchManifestFrom(ctx, baseURL, tag)
	if err != nil {
		return "", fmt.Errorf("localops: %w", err)
	}
	name := selfupdate.DesktopAssetName(goarch)
	want, ok := m.Assets[name]
	if !ok {
		return "", fmt.Errorf("%w: release %s does not publish %s", ErrNoPrebuiltDesktop, tag, name)
	}
	// A build killed mid-download never ran its cleanup. Under the lock no
	// other download is in flight, so every such zip is a leftover.
	stale, _ := filepath.Glob(filepath.Join(dir, desktopDownloadPattern))
	for _, old := range stale {
		if err = os.Remove(old); err != nil {
			return "", fmt.Errorf("localops: remove an interrupted download: %w", err)
		}
	}
	f, err := os.CreateTemp(dir, desktopDownloadPattern)
	if err != nil {
		return "", fmt.Errorf("localops: stage the desktop app download in %s: %w", dir, err)
	}
	_, _ = fmt.Fprintf(out, "downloading %s%s\n", m.AssetURL(name), megabytes(want.Size))
	_, err = m.Download(ctx, name, f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("localops: %w", err)
	}
	_, _ = fmt.Fprintf(out, "  verified against the signed %s\n", selfupdate.ManifestName)
	return f.Name(), nil
}

// DesktopFromZip extracts a prebuilt Windows desktop app: electron-builder's
// win-unpacked directory, zipped with its contents at the root. A zip
// without aether-desktop.exe at its root is refused.
func DesktopFromZip(archive string) DesktopSource {
	return func(dir string) error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("localops: create %s: %w", dir, err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return fmt.Errorf("localops: %w", err)
		}
		defer func() { _ = root.Close() }()
		if err = extractZip(archive, root, maxDesktopTree); err != nil {
			return fmt.Errorf("localops: extract %s: %w", archive, err)
		}
		if info, err := root.Lstat(desktopExe); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("localops: %s is not a Windows desktop app: it has no %s at its root", archive, desktopExe)
		}
		return nil
	}
}
