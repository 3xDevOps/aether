// Package localops holds the client-machine operations behind both the
// aether CLI verbs and the local gateway's /local/v1 surface: linking a
// repository, pulling run branches, scaffolding images, installing the
// sync daemon, and managing live-overlay sync sessions. Everything here
// runs with the user's own repository and SSH key; nothing talks to the
// server directly - callers supply server-derived inputs (workspace IDs,
// pull coordinates, dialed streams).
package localops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
)

// LinkRepo points repo's `aether` git remote at the workspace and saves
// the updated link config. The repo path is made absolute and must be a
// git repository; workspaceID must be a resolved workspace ID (the remote
// URL carries IDs only). It returns the updated config and the remote URL.
func LinkRepo(cfg cli.Config, repo, workspaceID string) (cli.Config, string, error) {
	if repo == "" {
		return cfg, "", errors.New("localops: repo path is required")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return cfg, "", fmt.Errorf("localops: resolve repo path: %w", err)
	}
	if out, gerr := exec.Command("git", "-C", abs, "rev-parse", "--git-dir").CombinedOutput(); gerr != nil {
		return cfg, "", fmt.Errorf("localops: %s is not a git repository: %s", abs, strings.TrimSpace(string(out)))
	}
	url := cli.GitURL(cfg.User, cfg.Addr, workspaceID)
	var buf bytes.Buffer
	if err := GitRemote(abs, url, &buf, &buf); err != nil {
		return cfg, "", fmt.Errorf("localops: set git remote: %w: %s", err, strings.TrimSpace(buf.String()))
	}
	cfg.Repo = abs
	if err := cli.Save(cfg); err != nil {
		return cfg, "", err
	}
	return cfg, url, nil
}

// GitRemote adds the `aether` remote to repo pointing at url, or updates
// it when it already exists. Git's own output goes to stdout/stderr so
// the CLI can stream it and the gateway can capture it.
func GitRemote(repo, url string, stdout, stderr io.Writer) error {
	out, err := exec.Command("git", "-C", repo, "remote").Output()
	if err != nil {
		return fmt.Errorf("git remote: %w", err)
	}
	has := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "aether" {
			has = true
			break
		}
	}
	args := []string{"-C", repo, "remote", "add", "aether", url}
	if has {
		args = []string{"-C", repo, "remote", "set-url", "aether", url}
	}
	cmd := exec.Command("git", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
