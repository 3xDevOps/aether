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
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// LinkRepo points repo's `aether` git remote at the workspace and saves
// the updated link config. The repo path is made absolute and must be a
// git repository; workspaceID must be a resolved workspace ID (the remote
// URL carries IDs only). It returns the updated config, the remote URL,
// and git's output, which holds the warning GitRemote gives when an
// existing core.sshCommand keeps git from using aether edge-ssh.
func LinkRepo(cfg cli.Config, repo, workspaceID string) (cli.Config, string, string, error) {
	if repo == "" {
		return cfg, "", "", errors.New("localops: repo path is required")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return cfg, "", "", fmt.Errorf("localops: resolve repo path: %w", err)
	}
	if out, gerr := exec.Command("git", "-C", abs, "rev-parse", "--git-dir").CombinedOutput(); gerr != nil {
		return cfg, "", "", fmt.Errorf("localops: %s is not a git repository: %s", abs, strings.TrimSpace(string(out)))
	}
	saved := cfg
	profile := -1
	if cfg.Active != "" {
		saved, err = cli.Load()
		if err != nil {
			return cfg, "", "", fmt.Errorf("localops: load named server %q: %w", cfg.Active, err)
		}
		profile = slices.IndexFunc(saved.Links, func(link cli.NamedLink) bool { return link.Name == cfg.Active })
		if profile < 0 {
			return cfg, "", "", fmt.Errorf("localops: named server %q no longer exists; restart aether gui", cfg.Active)
		}
		current, _ := saved.Named(cfg.Active)
		if current.Addr != cfg.Addr || current.User != cfg.User || current.Key != cfg.Key ||
			current.KnownHosts != cfg.KnownHosts || current.EdgeURL != cfg.EdgeURL || current.ServerID != cfg.ServerID {
			return cfg, "", "", fmt.Errorf("localops: named server %q changed; restart aether gui before linking a repository", cfg.Active)
		}
	}
	url := cli.GitURL(cfg.User, cfg.GitHost(), workspaceID)
	var buf bytes.Buffer
	if err := GitRemote(abs, url, &buf, &buf); err != nil {
		return cfg, "", "", fmt.Errorf("localops: set git remote: %w: %s", err, strings.TrimSpace(buf.String()))
	}
	cfg = KeepLegacyRepo(cfg)
	cfg.Repo = abs
	if profile >= 0 {
		saved.Links[profile].Repo = abs
		for _, r := range cfg.Repos {
			if !slices.ContainsFunc(saved.Repos, func(k cli.LinkedRepo) bool { return k.WorkspaceID == r.WorkspaceID || k.Path == r.Path }) {
				saved.Repos = append(slices.Clip(saved.Repos), r)
			}
		}
		saved = cli.WithRepo(saved, workspaceID, abs)
		cfg.Links, cfg.Repos = saved.Links, saved.Repos
	} else {
		cfg = cli.WithRepo(cfg, workspaceID, abs)
		saved = cfg
	}
	if err := cli.Save(saved); err != nil {
		return cfg, "", "", err
	}
	return cfg, url, buf.String(), nil
}

// LinkedRepos lists the clones cfg links. A config written before Repos
// existed names its one clone in Repo, and that clone belongs to the
// workspace its aether remote names. The error is that clone's alone: the
// recorded entries come back with it.
func LinkedRepos(cfg cli.Config) ([]cli.LinkedRepo, error) {
	repos := cfg.Repos
	if cfg.Repo == "" || slices.ContainsFunc(repos, func(r cli.LinkedRepo) bool { return r.Path == cfg.Repo }) {
		return repos, nil
	}
	workspaceID, err := RemoteWorkspace(cfg.Repo)
	if err != nil {
		return repos, fmt.Errorf("localops: linked repo %s: %w", cfg.Repo, err)
	}
	if workspaceID == "" || slices.ContainsFunc(repos, func(r cli.LinkedRepo) bool { return r.WorkspaceID == workspaceID }) {
		return repos, nil
	}
	return append(slices.Clip(repos), cli.LinkedRepo{WorkspaceID: workspaceID, Path: cfg.Repo}), nil
}

// KeepLegacyRepo records the clone an older config names only in Repo, so
// a write that replaces Repo leaves it linked.
func KeepLegacyRepo(cfg cli.Config) cli.Config {
	if repos, err := LinkedRepos(cfg); err == nil {
		cfg.Repos = repos
	}
	return cfg
}

func RepoFor(cfg cli.Config, workspaceID string) (string, error) {
	repos, err := LinkedRepos(cfg)
	if i := slices.IndexFunc(repos, func(r cli.LinkedRepo) bool { return r.WorkspaceID == workspaceID }); i >= 0 {
		return repos[i].Path, nil
	}
	return "", err
}

func RemoteWorkspace(repo string) (string, error) {
	raw, err := AetherRemoteURL(repo)
	if err != nil || raw == "" {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("localops: aether remote of %s: %w", repo, err)
	}
	return strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), nil
}

func CloneHead(path string) (root, branch string, err error) {
	root, err = gitLine(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	if err = gitQuiet(root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return "", "", fmt.Errorf("%s has no commits yet; make your first commit, then try again", root)
	}
	if branch = currentBranch(root); branch == "" {
		return "", "", fmt.Errorf("%s has no branch checked out; check out the branch sessions should start from", root)
	}
	return root, branch, nil
}

// OriginURL reports where repo's `origin` remote points, or "" when the
// repository has no origin. This is the upstream a workspace records so
// that runs push and open pull requests against the same place the
// developer's own clone does.
func OriginURL(repo string) (string, error) {
	names, err := remotes(repo)
	if err != nil {
		return "", fmt.Errorf("localops: read remotes of %s: %w", repo, err)
	}
	if !slices.Contains(names, "origin") {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("localops: git remote get-url origin in %s: %w: %s", repo, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// GitRemote adds the `aether` remote to repo pointing at url, or updates
// it when it already exists. Git's own output goes to stdout/stderr so
// the CLI can stream it and the gateway can capture it. For a url on an
// edge link's logical host it also points core.sshCommand at aether
// edge-ssh, so git typed by hand reaches the server too.
func GitRemote(repo, url string, stdout, stderr io.Writer) error {
	names, err := remotes(repo)
	if err != nil {
		return err
	}
	args := []string{"-C", repo, "remote", "add", "aether", url}
	if slices.Contains(names, "aether") {
		args = []string{"-C", repo, "remote", "set-url", "aether", url}
	}
	cmd := exec.Command("git", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		return err
	}
	env, err := cli.GitSSHEnv(url)
	if err != nil || env == nil {
		return err
	}
	return gitSSHCommand(repo, stdout)
}

// MoveRemote moves only remotes that still name prev's git host, so a clone
// linked to another server stays. It returns the last new URL, or "" when no
// remote moved.
func MoveRemote(prev, cfg cli.Config, stdout, stderr io.Writer) (string, error) {
	repos, err := LinkedRepos(cfg)
	last := ""
	for _, r := range repos {
		url, rerr := AetherRemoteURL(r.Path)
		if rerr != nil {
			err = errors.Join(err, fmt.Errorf("localops: read the aether remote of %s: %w", r.Path, rerr))
			continue
		}
		moved, ok := cfg.MovedGitURL(prev, url)
		if !ok {
			continue
		}
		if rerr = GitRemote(r.Path, moved, stdout, stderr); rerr != nil {
			err = errors.Join(err, fmt.Errorf("localops: move the aether remote of %s: %w", r.Path, rerr))
			continue
		}
		last = moved
	}
	return last, err
}

// gitSSHCommand sets repo's core.sshCommand to aether edge-ssh when no
// git config names one, or when the value is an aether edge-ssh an
// earlier binary wrote, whose path an upgrade or a move may have
// removed. Any other existing value, from any config file, is the user's
// and is never overwritten: stdout gets the exact command that would
// replace it instead.
func gitSSHCommand(repo string, stdout io.Writer) error {
	want, err := cli.EdgeSSHCommand()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", "core.sshCommand").CombinedOutput()
	current := strings.TrimSpace(string(out))
	var exit *exec.ExitError
	switch {
	case err == nil && current == want:
		return nil
	case err == nil && aetherEdgeSSH(current):
	case err == nil:
		_, err = fmt.Fprintf(stdout, "core.sshCommand is already %q; aether left it unchanged.\n"+
			"git typed by hand reaches the edge link only through aether edge-ssh, which runs ssh unchanged for every other host:\n"+
			"  git -C %s config core.sshCommand %s\n", current, shellquote.Quote(repo), shellquote.Quote(want))
		return err
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		// Exit status 1 is git's answer for an unset key.
	default:
		return fmt.Errorf("git config --get core.sshCommand: %w: %s", err, current)
	}
	if out, err = exec.CommandContext(ctx, "git", "-C", repo, "config", "--local", "core.sshCommand", want).CombinedOutput(); err != nil {
		return fmt.Errorf("git config core.sshCommand: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, err = fmt.Fprintf(stdout, "git core.sshCommand -> %s\n", want)
	return err
}

// aetherEdgeSSH reports whether command is exactly what EdgeSSHCommand
// returns for some binary named aether.
func aetherEdgeSSH(command string) bool {
	exe, ok := strings.CutSuffix(command, " edge-ssh")
	if !ok {
		return false
	}
	if len(exe) >= 2 && strings.HasPrefix(exe, "'") && strings.HasSuffix(exe, "'") {
		exe = strings.ReplaceAll(exe[1:len(exe)-1], `'\''`, "'")
	}
	name := filepath.Base(exe)
	return (name == "aether" || name == "aether.exe") && shellquote.Quote(exe)+" edge-ssh" == command
}
