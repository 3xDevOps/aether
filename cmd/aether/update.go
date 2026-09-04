package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	jsonOut := fs.Bool("json", false, "with --check, print the check as one JSON line")
	noApp := fs.Bool("no-app", false, "skip rebuilding the installed desktop app")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *jsonOut && !*check {
		return errors.New("--json only reports a check; re-run as: aether update --check --json")
	}
	if *check && *tag != "" {
		return errors.New("--version installs a tag and --check installs nothing; pick one")
	}
	if *check && *noApp {
		return errors.New("--no-app skips part of an install and --check installs nothing; pick one")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if *check {
		return reportCheck(ctx, *jsonOut)
	}

	if runtime.GOOS == "windows" {
		// Refuse before the network: selfupdate.Update refuses too, but
		// there is no reason to resolve a tag that cannot be installed.
		return selfupdate.ErrWindows
	}
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

	fmt.Printf("updating aether %s -> %s\n", version.String(), *tag)
	replaced, err := selfupdate.Update(ctx, base, *tag)
	for _, path := range replaced {
		fmt.Printf("updated %s\n", path)
	}
	if err != nil {
		return err
	}
	if len(replaced) > 1 {
		fmt.Println("restart it: sudo systemctl restart aether-server")
	}
	if *noApp {
		return nil
	}
	// replaced[0] is this CLI's own path with its symlinks resolved, which
	// is where the new binary now sits.
	return rebuildDesktopApp(replaced[0])
}

// reportCheck prints the release check and exits 0 whatever it says: a
// caller polling for an update wants an answer, not an exit status.
func reportCheck(ctx context.Context, asJSON bool) error {
	got, err := selfupdate.DefaultChecker().Check(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		line, err := json.Marshal(got)
		if err != nil {
			return err
		}
		fmt.Println(string(line))
		return nil
	}
	switch {
	case got.Disabled:
		fmt.Printf("release checks are off: %s is set (running %s)\n", selfupdate.OptOutEnv, version.String())
	case got.Dev:
		fmt.Printf("dev build (%s); releases are not checked\n", version.String())
	case got.UpdateAvailable:
		fmt.Printf("update available: %s (running %s); run: aether update\n", got.Latest, version.String())
	case got.Latest == version.Version:
		fmt.Printf("already on the latest release %s\n", got.Latest)
	default:
		// Ahead of the newest release, or a version neither side can
		// order. Claiming "already on the latest" would be a lie in both
		// cases, so both tags are printed and the reader decides.
		fmt.Printf("running %s; the latest release is %s\n", version.String(), got.Latest)
	}
	return nil
}
