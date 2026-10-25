package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const githubUsage = "usage: aether github connect"

// githubLoginCommand is what github.oauth.start runs in the Environment. gh
// always asks for repo, read:org and gist, and adds the scopes named here.
const (
	githubLoginCommand = "gh auth login --hostname github.com --git-protocol https --web --scopes admin:ssh_signing_key"
	githubLoginScopes  = "repo, read:org, gist, admin:ssh_signing_key"
)

// githubPollInterval is a variable so a test can shorten it.
var githubPollInterval = 2 * time.Second

func init() {
	register(command{
		name:  "github",
		short: "connect your Environment to GitHub",
		run:   runGitHub,
	})
}

func runGitHub(args []string) error {
	if len(args) != 1 || args[0] != "connect" {
		return errors.New(githubUsage)
	}
	return withControl(func(c *protocol.Client) error { return githubConnect(c, os.Stdout) })
}

func githubConnect(c *protocol.Client, out io.Writer) error {
	var result protocol.GitHubConnectResult
	err := c.Call(protocol.MethodGitHubConnect, struct{}{}, &result)
	if needsGitHubLogin(err) {
		return githubDeviceConnect(c, out)
	}
	if err != nil {
		return err
	}
	return printGitHubConnection(out, result)
}

// needsGitHubLogin reports a github.connect refusal that a device login
// fixes: gh is not logged in, its login lacks the signing scope, or the
// Environment is stopped and github.oauth.start would start it.
func needsGitHubLogin(err error) bool {
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidState {
		return false
	}
	return strings.HasPrefix(perr.Message, "github: not logged in to github.com") ||
		strings.Contains(perr.Message, "lacks the admin:ssh_signing_key scope") ||
		strings.Contains(perr.Message, "environment terminal is not running")
}

// githubDeviceConnect shows the pending code again on a second call: the
// server keeps one attempt per member.
func githubDeviceConnect(c *protocol.Client, out io.Writer) error {
	var r protocol.GitHubOAuthResult
	if err := c.Call(protocol.MethodGitHubOAuthStart, struct{}{}, &r); err != nil {
		var perr *protocol.Error
		if errors.As(err, &perr) && perr.Code == protocol.CodeDenied {
			return fmt.Errorf("%w\nrun this in aether terminal, then aether github connect again:\n  %s", err, githubLoginCommand)
		}
		return err
	}
	session := r.SessionID
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	ticker := time.NewTicker(githubPollInterval)
	defer ticker.Stop()
	shown := false
	for {
		switch r.State {
		case "starting", "finishing":
		case "pending":
			if !shown {
				if _, err := fmt.Fprintf(out, "url: %s\ncode: %s\nscopes: %s\nexpires: %s\nopen the URL in your browser and enter the code; waiting for approval...\n",
					r.VerificationURL, r.UserCode, githubLoginScopes, r.ExpiresAt); err != nil {
					return err
				}
				shown = true
			}
		case "connected":
			if r.Connection == nil {
				return fmt.Errorf("github: logged in to github.com as %s, but the server returned no connection; run aether github connect again", r.Login)
			}
			return printGitHubConnection(out, *r.Connection)
		case "expired":
			return fmt.Errorf("%s\nrun aether github connect for a new code", r.Error)
		default:
			if r.Error == "" {
				return fmt.Errorf("github: authorization ended %s", r.State)
			}
			return errors.New(r.Error)
		}
		select {
		case <-interrupts:
			var cancelled protocol.GitHubOAuthResult
			if err := c.Call(protocol.MethodGitHubOAuthCancel, protocol.GitHubOAuthCancelParams{SessionID: session}, &cancelled); err != nil {
				return fmt.Errorf("github: cancel the authorization: %w", err)
			}
			return errors.New("github: authorization cancelled")
		case <-ticker.C:
		}
		var next protocol.GitHubOAuthResult
		if err := c.Call(protocol.MethodGitHubOAuthStatus, protocol.GitHubOAuthStatusParams{SessionID: session}, &next); err != nil {
			return err
		}
		r = next
	}
}

func printGitHubConnection(w io.Writer, result protocol.GitHubConnectResult) error {
	_, err := fmt.Fprintf(w, "logged in to github.com as %s\nsigning key %s registered on GitHub\n",
		result.Login, result.Fingerprint)
	return err
}
