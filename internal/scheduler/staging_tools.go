package scheduler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// toolMountRoots are the container paths a staging tree can be mounted at
// (harness.HomeDir has exactly these two homes). Vendor installers create
// absolute symlinks under them (~/.local/bin/claude ->
// /root/.local/share/claude/versions/<v>); those targets are meaningless on
// the host and break the moment a run uses the other home.
var toolMountRoots = []string{"/root/.local", "/home/aether/.local"}

// normalizeStagedSymlinks rewrites every symlink in staging whose absolute
// target lies under a tool mount root into a relative link to the same file
// within the tree. The result resolves identically inside any run container
// and also on the host, where verification and promotion stat the tree.
// Symlinks that are already relative, or point outside ~/.local, are left
// alone; they are never followed host-side.
func normalizeStagedSymlinks(staging string) error {
	return filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		slash := path.Clean(filepath.ToSlash(target))
		if !path.IsAbs(slash) {
			return nil
		}
		var inTree string
		for _, root := range toolMountRoots {
			if strings.HasPrefix(slash, root+"/") {
				inTree = strings.TrimPrefix(slash, root+"/")
				break
			}
		}
		if inTree == "" {
			return nil
		}
		relative, err := filepath.Rel(filepath.Dir(p), filepath.Join(staging, filepath.FromSlash(inTree)))
		if err != nil {
			return err
		}
		if removeErr := os.Remove(p); removeErr != nil {
			return removeErr
		}
		return os.Symlink(relative, p)
	})
}

// verifyStagedExecutable checks that bin/<name> in the staging tree is an
// executable regular file. Resolution is confined to the tree via os.Root, so
// a symlink chain that escapes staging fails here instead of statting host
// files, and the error names the container path the member can act on, never
// a server host path.
func verifyStagedExecutable(staging, name string) error {
	fail := func(cause string) error {
		return fmt.Errorf("~/.local/bin/%s %s; install the agent into ~/.local/bin before exiting (nothing was saved)", name, cause)
	}
	root, err := os.OpenRoot(staging)
	if err != nil {
		return fmt.Errorf("scheduler: open staging: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(filepath.Join("bin", name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fail("was not found in the staged tools")
	case err != nil:
		return fail("could not be resolved within the staged tools")
	case info.IsDir():
		return fail("is a directory, not an executable")
	case !info.Mode().IsRegular():
		return fail("is not a regular file")
	case info.Mode().Perm()&0o111 == 0:
		return fail("is not executable")
	}
	return nil
}

// stagedExecutable is the name agent-setup verification checks and the
// install script targets: what the profile actually launches, which for an
// admin or shipped definition can differ from the harness name.
func stagedExecutable(req domain.WorkspaceShellRequest, profile harness.Profile) string {
	if len(profile.TUIArgs) > 0 && profile.TUIArgs[0] != "" {
		return profile.TUIArgs[0]
	}
	return req.Harness
}

// agentSetupCommand wraps the interactive shell so a shipped agent installs
// itself before the member gets the prompt. The install is skipped when the
// executable is already staged (a resumed or reseeded session) and a failure
// falls back to the manual shell with the vendor pointer, so the automatic
// path is never worse than the empty-script one.
func agentSetupCommand(profile string, installScript, executable string) []string {
	if installScript == "" || executable == "" {
		return []string{"/bin/sh", "-i"}
	}
	script := fmt.Sprintf(
		`if [ ! -x "$HOME/.local/bin/%[1]s" ]; then `+
			`echo "aether: installing %[2]s..."; `+
			`if (%[3]s); then echo "aether: %[2]s installed."; `+
			`else echo "aether: automatic install failed; install %[2]s into ~/.local/bin manually (see the vendor's docs), then exit."; fi; `+
			`fi; exec /bin/sh -i`,
		executable, profile, installScript)
	return []string{"/bin/sh", "-c", script}
}
