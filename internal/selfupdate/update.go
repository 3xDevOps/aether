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
	if runtime.GOOS == "windows" {
		return nil, ErrWindows
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate this binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return nil, fmt.Errorf("resolve this binary: %w", err)
	}
	dir := filepath.Dir(self)
	if err := checkWritable(dir); err != nil {
		return nil, err
	}

	assets := baseURL + "/releases/download/" + tag
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	if err := Apply(ctx, assets, "aether"+suffix, self); err != nil {
		return nil, err
	}
	replaced := []string{self}

	// A Linux box with aether-server next to the CLI is a server host;
	// update both so the pair never skews. Elsewhere the server asset does
	// not exist and there is nothing to update.
	if runtime.GOOS != "linux" {
		return replaced, nil
	}
	server := filepath.Join(dir, "aether-server")
	if _, err := os.Stat(server); err != nil {
		return replaced, nil
	}
	if err := Apply(ctx, assets, "aether-server"+suffix, server); err != nil {
		return replaced, fmt.Errorf("aether updated but aether-server failed: %w", err)
	}
	return append(replaced, server), nil
}

// checkWritable proves dir accepts a new file before anything is
// downloaded: Apply stages there, and a rename that fails after a
// hundred-megabyte download is a worse way to learn the binary lives in a
// root-owned directory.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".aether-update-probe-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s is not writable by this user, re-run as: sudo aether update: %w", dir, err)
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
