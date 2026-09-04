package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Method is how the running binary can be replaced from a GUI session.
type Method string

const (
	// MethodDirect: the binary's directory is writable by this user, and
	// UpdateWithAuthorization swaps it without asking anyone.
	MethodDirect Method = "direct"
	// MethodAdminPrompt: the directory is not writable by this user and
	// only root can write it, this is macOS, and there is a GUI session:
	// UpdateWithAuthorization installs through the administrator dialog.
	MethodAdminPrompt Method = "admin-prompt"
	// MethodManual: the binary cannot be replaced from here, and the
	// user runs the documented command - `sudo aether update` on Linux,
	// a download on Windows.
	MethodManual Method = "manual"
)

// Access is where the running binary lives and how it can be replaced.
type Access struct {
	// Path is the binary with its symlinks resolved: the file an update
	// replaces.
	Path string
	// Method is how UpdateWithAuthorization would replace it right now.
	Method Method
}

// Probe resolves the running binary and probes its directory, so the
// dashboard can say before the click whether an update will just happen,
// ask for an administrator password, or need a terminal. It uses the
// same predicate UpdateWithAuthorization does, so the promise and the
// behavior cannot drift apart. It is cheap - one temp file created and
// removed - and is not cached: a reinstall or a chown must show on the
// next check.
func Probe() (Access, error) {
	self, err := os.Executable()
	if err != nil {
		return Access{}, fmt.Errorf("locate this binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return Access{}, fmt.Errorf("resolve this binary: %w", err)
	}
	return access(self)
}

// access classifies one resolved binary path.
func access(self string) (Access, error) {
	out := Access{Path: self, Method: MethodManual}
	if runtime.GOOS == "windows" {
		return out, nil
	}
	err := CheckWritable(filepath.Dir(self))
	switch {
	case err == nil:
		out.Method = MethodDirect
	case !errors.Is(err, os.ErrPermission):
		return out, err
	case canAuthorize(filepath.Dir(self)):
		out.Method = MethodAdminPrompt
	}
	return out, nil
}
