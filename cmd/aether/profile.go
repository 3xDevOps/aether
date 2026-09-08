package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	cliprofile "github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:  "profile",
		short: "push, status, or rollback an agent profile",
		run:   runProfile,
	})
}

func runProfile(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether profile <push|status|rollback> [flags]")
	}
	switch args[0] {
	case "push":
		return profilePush(args[1:])
	case "status":
		return profileStatus(args[1:])
	case "rollback":
		return profileRollback(args[1:])
	default:
		return fmt.Errorf("unknown profile command %q", args[0])
	}
}

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func profilePush(args []string) error {
	fs := flag.NewFlagSet("profile push", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	workspace := fs.String("workspace", "", "optional workspace ID for --allow-secret audit")
	var allow, skip stringList
	fs.Var(&allow, "allow-secret", "send this file even though the scanner flagged it (repeatable)")
	fs.Var(&skip, "skip-secret", "leave this flagged file out and push the rest (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" {
		return fmt.Errorf("usage: aether profile push --agent <harness> [--skip-secret <file> ...] [--allow-secret <file> ...]")
	}
	if len(allow) > 0 && *workspace == "" {
		return fmt.Errorf("profile push: --allow-secret requires --workspace")
	}
	root, _, err := cliprofile.LocalDir(*agent)
	if err != nil {
		return err
	}
	files, skipped, err := cliprofile.DiscoverFiles(context.Background(), *agent, allow)
	if err != nil {
		return err
	}
	// A file left out for its size would otherwise land on the server, so
	// say which ones did not and why, before the snapshot line. This runs
	// before the refusal below, so a member answering a finding of their
	// own still sees every other file the walk dropped, and the command
	// that carries a plugin's own file.
	for _, s := range skipped {
		fmt.Printf("skipped %s: %s\n", s.Path, s.Detail)
		// docs/harnesses.md promises --allow-secret still carries a file
		// the plugin rule dropped. The path is long and carries a plugin
		// version, so print the command rather than leave it to be
		// retyped.
		if s.Reason == cliprofile.ExcludeVendoredSecret {
			fmt.Printf("  to send it anyway: aether profile push --agent %s --allow-secret %s --workspace <workspace>\n",
				*agent, shellquote.Path(s.Path))
		}
	}
	if flagged := cliprofile.UnacknowledgedSecrets(root, skipped, skip); len(flagged) > 0 {
		return secretRefusal(*agent, flagged)
	}
	return withControl(func(c *protocol.Client) error {
		snap, err := cliprofile.Push(c, *agent, files, allow, *workspace)
		if err != nil {
			return err
		}
		fmt.Printf("snapshot %s digest %s\n", snap.ID, snap.Digest)
		return nil
	})
}

func profileStatus(args []string) error {
	fs := flag.NewFlagSet("profile status", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" {
		return fmt.Errorf("usage: aether profile status --agent <harness>")
	}
	return withControl(func(c *protocol.Client) error {
		res, err := cliprofile.Status(c, *agent)
		if err != nil {
			return err
		}
		fmt.Print(cliprofile.FormatStatus(res))
		return nil
	})
}

func profileRollback(args []string) error {
	fs := flag.NewFlagSet("profile rollback", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" || fs.NArg() < 1 {
		return fmt.Errorf("usage: aether profile rollback --agent <harness> <snapshot-id>")
	}
	return withControl(func(c *protocol.Client) error {
		snap, err := cliprofile.Rollback(c, *agent, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Printf("snapshot %s digest %s\n", snap.ID, snap.Digest)
		return nil
	})
}

// secretRefusal is the message for scanner findings in the member's own
// files. The push leaves those files out on its own, but a terminal has
// no screen to show them on before the upload, so the member picks per
// file: remove the secret and push again, drop the file, or send it. The
// dashboard makes the same choice by showing the finding on the harness
// row before the import button.
func secretRefusal(harnessName string, flagged []cliprofile.Exclusion) error {
	var b strings.Builder
	head := fmt.Sprintf("profile push: the secret scanner flagged %d files you wrote. "+
		"Remove the secrets, or say what to do with each file:", len(flagged))
	if len(flagged) == 1 {
		head = "profile push: the secret scanner flagged a file you wrote. " +
			"Remove the secret, or say what to do with it:"
	}
	b.WriteString(head)
	for _, f := range flagged {
		fmt.Fprintf(&b, "\n  %s: %s", f.Path, f.Detail)
		fmt.Fprintf(&b, "\n    leave it out:   aether profile push --agent %s --skip-secret %s", harnessName, shellquote.Path(f.Path))
		fmt.Fprintf(&b, "\n    send it anyway: aether profile push --agent %s --allow-secret %s --workspace <workspace>",
			harnessName, shellquote.Path(f.Path))
	}
	return errors.New(b.String())
}
