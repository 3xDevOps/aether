package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

func init() {
	register(command{
		name:  "update",
		short: "update this binary (and aether-server beside it) to a release",
		run:   runUpdate,
	})
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	tag := fs.String("version", "", "release tag to install (default: latest)")
	check := fs.Bool("check", false, "only report whether an update is available")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if runtime.GOOS == "windows" {
		// Windows cannot rename over a running executable; keep the one
		// documented path instead of a half-supported dance.
		return fmt.Errorf("self-update is not supported on Windows; download the release from https://github.com/%s/releases", selfupdate.Repo)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	base := "https://github.com/" + selfupdate.Repo
	if *tag == "" {
		latest, err := selfupdate.LatestTag(ctx, base+"/releases/latest")
		if err != nil {
			return err
		}
		*tag = latest
	}

	if version.Version == *tag {
		fmt.Printf("already on %s\n", *tag)
		return nil
	}
	if *check {
		fmt.Printf("update available: %s (running %s)\n", *tag, version.String())
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve this binary: %w", err)
	}

	assets := base + "/releases/download/" + *tag
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH

	fmt.Printf("updating aether %s -> %s\n", version.String(), *tag)
	if err := selfupdate.Apply(ctx, assets, "aether"+suffix, self); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w (binary in %s is not writable; re-run with sudo)", err, filepath.Dir(self))
		}
		return err
	}
	fmt.Printf("updated %s\n", self)

	// A Linux box with aether-server next to the CLI is a server host;
	// update both so the pair never skews. Elsewhere the server asset does
	// not exist and there is nothing to update.
	server := filepath.Join(filepath.Dir(self), "aether-server")
	if runtime.GOOS == "linux" {
		if _, err := os.Stat(server); err == nil {
			if err := selfupdate.Apply(ctx, assets, "aether-server"+suffix, server); err != nil {
				return fmt.Errorf("aether updated but aether-server failed: %w", err)
			}
			fmt.Printf("updated %s\n", server)
			fmt.Println("restart it: sudo systemctl restart aether-server")
		}
	}
	return nil
}
