package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const githubUsage = "usage: aether github connect"

func init() {
	register(command{
		name:  "github",
		short: "connect the member environment to GitHub",
		run:   runGitHub,
	})
}

func runGitHub(args []string) error {
	if len(args) != 1 || args[0] != "connect" {
		return errors.New(githubUsage)
	}
	return withControl(func(c *protocol.Client) error {
		var result protocol.GitHubConnectResult
		if err := c.Call(protocol.MethodGitHubConnect, struct{}{}, &result); err != nil {
			return err
		}
		_, err := fmt.Fprintf(os.Stdout, "logged in to github.com as %s\nsigning key %s registered on GitHub\n",
			result.Login, result.Fingerprint)
		return err
	})
}
