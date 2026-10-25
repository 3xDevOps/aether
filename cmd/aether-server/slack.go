package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/slack"
)

const slackUsage = "usage: aether-server slack <setup|remove> [--config <file>]"

func slackCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(slackUsage)
	}
	sub := args[0]
	o, _, rest, err := loadOptions("slack "+sub, args[1:], nil)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(slackUsage)
	}
	dir := slack.Dir(*o.dataDir)
	switch sub {
	case "setup":
		return slackSetup(context.Background(), os.Stdout, os.Stdin, dir)
	case "remove":
		removed, err := slack.RemoveTokens(dir)
		if err != nil {
			return err
		}
		if !removed {
			fmt.Printf("Slack is not set up: %s holds no tokens\n", dir)
			return nil
		}
		fmt.Printf("removed the Slack tokens from %s\napply it with:\n  %s\n", dir, serversetup.RestartCommand)
		return nil
	}
	return fmt.Errorf("unknown slack command %q (want setup or remove)", sub)
}

func slackSetup(ctx context.Context, w io.Writer, stdin io.Reader, dir string) error {
	prompts := []string{"App-level token (xapp-...)", "Bot token (xoxb-...)"}
	tokens := make([]string, 0, len(prompts))
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		for _, prompt := range prompts {
			_, _ = fmt.Fprintf(os.Stderr, "%s, input hidden: ", prompt)
			raw, err := term.ReadPassword(int(file.Fd()))
			_, _ = fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("read the %s: %w", strings.ToLower(prompt), err)
			}
			tokens = append(tokens, strings.TrimSpace(string(raw)))
		}
	} else {
		lines := bufio.NewScanner(stdin)
		for len(tokens) < len(prompts) && lines.Scan() {
			if line := strings.TrimSpace(lines.Text()); line != "" {
				tokens = append(tokens, line)
			}
		}
		if err := lines.Err(); err != nil {
			return fmt.Errorf("read the tokens from stdin: %w", err)
		}
	}
	if len(tokens) < len(prompts) || tokens[0] == "" || tokens[1] == "" {
		return errors.New("give the app-level token, then the bot token, on stdin, one per line; never as arguments")
	}
	app, bot := tokens[0], tokens[1]
	name, team, err := slack.CheckTokens(ctx, app, bot)
	if err != nil {
		return err
	}
	if err := slack.WriteTokens(dir, app, bot); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "Slack bot %s in workspace %s; its tokens are in %s\napply it with:\n  %s\n", name, team, dir, serversetup.RestartCommand)
	_, _ = fmt.Fprintln(w, "then each member links their Slack account with: aether slack link")
	return nil
}
