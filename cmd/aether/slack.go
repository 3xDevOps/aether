package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "slack",
		short: "link your Slack account to the server's Slack bot, or unlink it",
		usage: "aether slack\n" +
			"aether slack link\n" +
			"aether slack unlink",
		example: "aether slack link",
		run:     runSlack,
	})
}

const slackUsage = "usage: aether slack [link|unlink]"

func runSlack(args []string) error {
	switch {
	case len(args) == 0:
		return withControl(func(c *protocol.Client) error {
			var st protocol.SlackStatusResult
			if err := c.Call(protocol.MethodSlackStatus, struct{}{}, &st); err != nil {
				return olderServer(err)
			}
			return printSlackStatus(os.Stdout, st)
		})
	case len(args) == 1 && args[0] == "link":
		return withControl(func(c *protocol.Client) error {
			var res protocol.SlackLinkResult
			if err := c.Call(protocol.MethodSlackLink, struct{}{}, &res); err != nil {
				return olderServer(err)
			}
			return printSlackCode(os.Stdout, res, time.Now())
		})
	case len(args) == 1 && args[0] == "unlink":
		return withControl(func(c *protocol.Client) error {
			var res protocol.SlackUnlinkResult
			if err := c.Call(protocol.MethodSlackUnlink, struct{}{}, &res); err != nil {
				return olderServer(err)
			}
			fmt.Printf("unlinked Slack user %s (workspace %s); the bot no longer messages you\n", res.Link.UserID, res.Link.TeamID)
			return nil
		})
	}
	return errors.New(slackUsage)
}

func printSlackStatus(w io.Writer, st protocol.SlackStatusResult) error {
	if !st.Configured {
		_, _ = fmt.Fprintln(w, "This server has no Slack bot; an admin sets one up with: sudo aether-server slack setup (docs/slack.md)")
		return nil
	}
	state := "connected"
	if !st.Connected {
		state = "not connected: " + st.Error
	}
	_, _ = fmt.Fprintf(w, "Slack bot  %s in workspace %s, %s\n", cmp.Or(st.Bot, "(unknown yet)"), cmp.Or(st.Team, "(unknown yet)"), state)
	if st.Link == nil {
		_, _ = fmt.Fprintln(w, "Your link  none; link your Slack account with: aether slack link")
		return nil
	}
	linked, err := time.Parse(time.RFC3339, st.Link.LinkedAt)
	if err != nil {
		return fmt.Errorf("slack.status: linked_at: %w", err)
	}
	_, _ = fmt.Fprintf(w, "Your link  Slack user %s, since %s\n", st.Link.UserID, linked.Local().Format(time.DateOnly))
	_, _ = fmt.Fprintln(w, "Unlink it: aether slack unlink")
	return nil
}

func printSlackCode(w io.Writer, res protocol.SlackLinkResult, now time.Time) error {
	expires, err := time.Parse(time.RFC3339, res.ExpiresAt)
	if err != nil {
		return fmt.Errorf("slack.link: expires_at: %w", err)
	}
	to := "the server's Slack bot"
	if res.Bot != "" {
		to = res.Bot
	}
	if res.Team != "" {
		to += " in Slack workspace " + res.Team
	}
	_, _ = fmt.Fprintf(w, "Send this code to %s in a direct message:\n", to)
	_, _ = fmt.Fprintf(w, "  %s   one use, expires in %d minutes\n", res.Code, int(expires.Sub(now).Round(time.Minute)/time.Minute))
	return nil
}
