// Command release-hotfix derives the build and hotfix count of a hotfix
// tag vX.Y.Z (Z at least 1) from the signed, published releases of the
// repository at -repo, never from inputs:
//
//	release-hotfix -tag v0.9.2 -repo https://github.com/3xDevOps/cloudfield
//
// vX.Y.(Z-1) must be a stable release with a build. Its hotfix count h
// names the line's base, vX.Y.(Z-1-h), which must be the stable release on
// the same build with hotfix 0: the release the line's release/X.Y branch
// is cut from. The hotfix signs that build and hotfix h+1. It also verifies
// the current beta.json and stable.json, so a hotfix whose pointers cannot
// be moved is refused before anything builds; the signer decides which
// pointers move.
//
// It prints build, hotfix, base-tag, base-commit and previous-commit as
// name=value lines. scripts/release-hotfix.sh runs it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

var hotfixTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.([1-9][0-9]*)$`)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-hotfix:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 4 || args[0] != "-tag" || args[2] != "-repo" {
		return errors.New("usage: release-hotfix -tag <vX.Y.Z> -repo <https://github.com/owner/repository>")
	}
	tag, repo := args[1], args[3]
	parts := hotfixTag.FindStringSubmatch(tag)
	if parts == nil {
		return fmt.Errorf("%s is not a hotfix tag vX.Y.Z with Z at least 1", tag)
	}
	z, err := strconv.Atoi(parts[3])
	if err != nil || z > 99 {
		return fmt.Errorf("the patch number of %s is above 99", tag)
	}
	line := "v" + parts[1] + "." + parts[2] + "."

	previousTag := line + strconv.Itoa(z-1)
	previous, err := selfupdate.FetchManifestFrom(ctx, repo, previousTag)
	if err != nil {
		return err
	}
	if previous.Channel != selfupdate.ChannelStable {
		return fmt.Errorf("%s is %s; a hotfix fixes a stable release with a build", previousTag, describe(previous))
	}
	if previous.Hotfix > z-1 {
		return fmt.Errorf("%s is %s, more hotfixes than its patch number allows", previousTag, describe(previous))
	}
	base, baseTag := previous, line+strconv.Itoa(z-1-previous.Hotfix)
	if previous.Hotfix > 0 {
		if base, err = selfupdate.FetchManifestFrom(ctx, repo, baseTag); err != nil {
			return err
		}
		if base.Channel != selfupdate.ChannelStable || base.Build != previous.Build || base.Hotfix != 0 {
			return fmt.Errorf("%s is %s, so %s at build %d, hotfix %d does not follow it", baseTag, describe(base), previousTag, previous.Build, previous.Hotfix)
		}
	}
	hotfix := previous.Hotfix + 1

	for _, channel := range []string{selfupdate.ChannelBeta, selfupdate.ChannelStable} {
		if _, err = selfupdate.FetchPointer(ctx, repo, channel); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(stdout, "build=%d\nhotfix=%d\nbase-tag=%s\nbase-commit=%s\nprevious-commit=%s\n",
		base.Build, hotfix, baseTag, base.Commit, previous.Commit)
	return err
}

func describe(m selfupdate.Manifest) string {
	if m.Channel == "" {
		return "a release from before channels, with no build"
	}
	return fmt.Sprintf("a %s release at build %d, hotfix %d", m.Channel, m.Build, m.Hotfix)
}
