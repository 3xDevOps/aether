package localops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/3xDevOps/Aether/internal/syncd"
)

// InstallDaemonUnit renders the user-service definition for the sync
// daemon on this OS and writes it under the user home. cfg carries the
// `daemon run` flags (zero-valued fields at their flag defaults are
// omitted from the rendered argv, exactly like `aether daemon install`);
// noProfileSync appends --no-profile-sync. It returns the written file
// path and the shell command that activates the service.
func InstallDaemonUnit(cfg syncd.Config, noProfileSync bool) (path, activate string, err error) {
	if cfg.Server == "" {
		return "", "", errors.New("localops: daemon install requires a server address")
	}
	repo, err := filepath.Abs(cfg.RepoPath)
	if err != nil {
		return "", "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve aether binary path: %w", err)
	}

	runArgs := []string{"daemon", "run", "--server", cfg.Server, "--repo", repo}
	if cfg.KeyPath != "" {
		runArgs = append(runArgs, "--key", cfg.KeyPath)
	}
	if cfg.KnownHostsPath != "" {
		runArgs = append(runArgs, "--known-hosts", cfg.KnownHostsPath)
	}
	if cfg.User != "aether" && cfg.User != "" {
		runArgs = append(runArgs, "--user", cfg.User)
	}
	if cfg.Remote != "aether" && cfg.Remote != "" {
		runArgs = append(runArgs, "--remote", cfg.Remote)
	}
	if cfg.BaseBranch != "main" && cfg.BaseBranch != "" {
		runArgs = append(runArgs, "--base", cfg.BaseBranch)
	}
	if cfg.WorkspaceID != "" {
		runArgs = append(runArgs, "--workspace", cfg.WorkspaceID)
	}
	if noProfileSync {
		runArgs = append(runArgs, "--no-profile-sync")
	}

	unit, err := syncd.ServiceUnit(runtime.GOOS, exe, runArgs)
	if err != nil {
		return "", "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(home, filepath.FromSlash(unit.Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(unit.Content), 0o644); err != nil {
		return "", "", err
	}
	return path, unit.Activate, nil
}

// InstallDaemon is the /local/v1 daemon.install core: it installs the
// sync-daemon service unit for server and repo with every other option at
// its default and returns the unit path plus the activation note.
func InstallDaemon(server, repo string) (unitPath, note string, err error) {
	if repo == "" {
		repo = "."
	}
	path, activate, err := InstallDaemonUnit(syncd.Config{
		Server:     server,
		RepoPath:   repo,
		User:       "aether",
		Remote:     "aether",
		BaseBranch: "main",
	}, false)
	if err != nil {
		return "", "", err
	}
	return path, "activate it with: " + activate, nil
}

// DaemonStatus reports whether the sync-daemon service unit for this OS
// is installed and where it lives (whether installed or not).
func DaemonStatus() (installed bool, unitPath string, err error) {
	// ServiceUnit's rendered path does not depend on the argv; a
	// placeholder binary path keeps this a pure path computation.
	unit, err := syncd.ServiceUnit(runtime.GOOS, "aether", nil)
	if err != nil {
		return false, "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "", err
	}
	unitPath = filepath.Join(home, filepath.FromSlash(unit.Path))
	if _, err := os.Stat(unitPath); err != nil {
		if os.IsNotExist(err) {
			return false, unitPath, nil
		}
		return false, unitPath, fmt.Errorf("check %s: %w", unitPath, err)
	}
	return true, unitPath, nil
}
