package main

import (
	"flag"
	"fmt"

	cliprofile "github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
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
	session := fs.String("session", "", "optional session ID for --allow-secret audit")
	var allow stringList
	fs.Var(&allow, "allow-secret", "allow a scanned secret in this file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" {
		return fmt.Errorf("usage: aether profile push --agent <harness> [--allow-secret <file> ...]")
	}
	if len(allow) > 0 && *session == "" {
		return fmt.Errorf("profile push: --allow-secret requires --session")
	}
	files, err := cliprofile.Discover(*agent, allow)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		snap, err := cliprofile.Push(c, *agent, files, allow, *session)
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
