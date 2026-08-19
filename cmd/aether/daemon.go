package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	cliprofile "github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/syncd"
)

func daemonCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: aether daemon <run|install> [flags]")
	}
	switch args[0] {
	case "run":
		return daemonRun(args[1:])
	case "install":
		return daemonInstall(args[1:])
	default:
		return fmt.Errorf("unknown daemon command %q (want run or install)", args[0])
	}
}

// daemonFlags declares the flags shared by `daemon run` and
// `daemon install` on fs and returns the bound Config.
func daemonFlags(fs *flag.FlagSet) (*syncd.Config, *bool) {
	cfg := &syncd.Config{}
	fs.StringVar(&cfg.Server, "server", "", "aether-server SSH address, host:port (required)")
	fs.StringVar(&cfg.KeyPath, "key", "", "SSH private key (default ~/.ssh/id_ed25519)")
	fs.StringVar(&cfg.KnownHostsPath, "known-hosts", "", "known_hosts file for host key verification (default ~/.ssh/known_hosts)")
	fs.StringVar(&cfg.User, "user", "aether", "SSH username")
	fs.StringVar(&cfg.RepoPath, "repo", ".", "local git repository to sync")
	fs.StringVar(&cfg.Remote, "remote", "aether", "git remote name for the server")
	fs.StringVar(&cfg.BaseBranch, "base", "main", "local base branch pushed to the server")
	fs.StringVar(&cfg.WorkspaceID, "workspace", "", "only react to branch events of this workspace (default all)")
	noProfileSync := fs.Bool("no-profile-sync", false, "disable automatic profile discovery, watching, and reconnect catch-up")
	return cfg, noProfileSync
}

func daemonRun(args []string) error {
	fs := flag.NewFlagSet("daemon run", flag.ExitOnError)
	cfg, noProfileSync := daemonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := filepath.Abs(cfg.RepoPath)
	if err != nil {
		return err
	}
	cfg.RepoPath = repo
	d, err := syncd.New(*cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(os.Stderr, "aether daemon: syncing %s with %s (remote %q)\n", cfg.RepoPath, cfg.Server, cfg.Remote)
	go func() {
		_ = cliprofile.RunDaemon(ctx, cliprofile.DaemonConfig{
			Server:         cfg.Server,
			KeyPath:        cfg.KeyPath,
			KnownHostsPath: cfg.KnownHostsPath,
			User:           cfg.User,
			NoProfileSync:  *noProfileSync,
		})
	}()
	err = d.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func daemonInstall(args []string) error {
	fs := flag.NewFlagSet("daemon install", flag.ExitOnError)
	cfg, noProfileSync := daemonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Server == "" {
		return errors.New("daemon install: --server is required")
	}
	repo, err := filepath.Abs(cfg.RepoPath)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve aether binary path: %w", err)
	}

	runArgs := []string{"daemon", "run", "--server", cfg.Server, "--repo", repo}
	if cfg.KeyPath != "" {
		runArgs = append(runArgs, "--key", cfg.KeyPath)
	}
	if cfg.KnownHostsPath != "" {
		runArgs = append(runArgs, "--known-hosts", cfg.KnownHostsPath)
	}
	if cfg.User != "aether" {
		runArgs = append(runArgs, "--user", cfg.User)
	}
	if cfg.Remote != "aether" {
		runArgs = append(runArgs, "--remote", cfg.Remote)
	}
	if cfg.BaseBranch != "main" {
		runArgs = append(runArgs, "--base", cfg.BaseBranch)
	}
	if cfg.WorkspaceID != "" {
		runArgs = append(runArgs, "--workspace", cfg.WorkspaceID)
	}
	if *noProfileSync {
		runArgs = append(runArgs, "--no-profile-sync")
	}

	unit, err := syncd.ServiceUnit(runtime.GOOS, exe, runArgs)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, filepath.FromSlash(unit.Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(unit.Content), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nactivate it with:\n  %s\n", path, unit.Activate)
	return nil
}
