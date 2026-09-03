package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ErrWindows refuses the in-place swap on Windows: the OS cannot rename
// over a running executable, so the one documented path is a manual
// download rather than a half-supported dance.
var ErrWindows = errors.New("self-update is not supported on Windows; download the release from https://github.com/" + Repo + "/releases")

// Update replaces the running binary - and aether-server beside it on a
// Linux server host - with tag's release assets from baseURL, returning
// the paths it replaced in order. It prints nothing: callers report.
func Update(ctx context.Context, baseURL, tag string) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate this binary: %w", err)
	}
	// A Linux box with aether-server next to the CLI is a server host;
	// update both so the pair never skews. Elsewhere the server asset does
	// not exist and there is nothing to update.
	var companions []string
	if runtime.GOOS == "linux" {
		companions = append(companions, "aether-server")
	}
	return UpdateBinaries(ctx, baseURL, tag, self, "aether", companions...)
}

// UpdateBinaries replaces the binary at self with tag's release asset
// named primary, then each companion asset that already sits beside it,
// and returns the paths it replaced in order. Symlinks are resolved so the
// real file is swapped rather than the link. A failure replaces nothing
// past the point it happened; the binaries already swapped are still
// reported.
func UpdateBinaries(ctx context.Context, baseURL, tag, self, primary string, companions ...string) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrWindows
	}
	self, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil, fmt.Errorf("resolve this binary: %w", err)
	}
	dir := filepath.Dir(self)
	if err := CheckWritable(dir); err != nil {
		return nil, err
	}

	assets := baseURL + "/releases/download/" + tag
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	if err := Apply(ctx, assets, primary+suffix, self); err != nil {
		return nil, err
	}
	replaced := []string{self}

	for _, name := range companions {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := Apply(ctx, assets, name+suffix, path); err != nil {
			return replaced, fmt.Errorf("%s updated but %s failed: %w", primary, name, err)
		}
		replaced = append(replaced, path)
	}
	return replaced, nil
}

// CheckWritable proves dir accepts a new file before anything is
// downloaded: Apply stages there, and a rename that fails after a
// hundred-megabyte download is a worse way to learn the binary lives in a
// root-owned directory. It is also how the server reports whether it can
// update itself at all.
func CheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".aether-update-probe-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			// The wrapped error comes first so the command to copy ends
			// the sentence instead of running into a second colon.
			return fmt.Errorf("%w: %s is not writable by this user; re-run as `sudo aether update`", err, dir)
		}
		return fmt.Errorf("stage an update in %s: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close probe file in %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove probe file in %s: %w", dir, err)
	}
	return nil
}
