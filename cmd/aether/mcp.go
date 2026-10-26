package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "mcp",
		short: "add the linked server to ChatGPT or Claude as a custom connector, or revoke one",
		usage: "aether mcp\n" +
			"aether mcp revoke <connector-id>",
		example: "aether mcp",
		run:     runMCP,
	})
}

const mcpUsage = "usage: aether mcp [revoke <connector id>]"

func runMCP(args []string) error {
	switch {
	case len(args) == 0:
		return withControl(func(c *protocol.Client) error {
			var list protocol.ConnectorListResult
			if err := c.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
				return olderServer(err)
			}
			var code *protocol.ConnectorCodeResult
			if !list.SignIn {
				code = &protocol.ConnectorCodeResult{}
				if err := c.Call(protocol.MethodConnectorCode, struct{}{}, code); err != nil {
					return olderServer(err)
				}
			}
			return printConnectors(os.Stdout, list, code, time.Now())
		})
	case len(args) == 2 && args[0] == "revoke":
		return withControl(func(c *protocol.Client) error {
			var res protocol.ConnectorRevokeResult
			if err := c.Call(protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: args[1]}, &res); err != nil {
				return olderServer(err)
			}
			fmt.Printf("revoked connector %s, %s (%s); its tokens no longer work\n", res.Connector.ID, res.Connector.ClientName, res.Connector.RedirectHost)
			return nil
		})
	}
	return errors.New(mcpUsage)
}

func olderServer(err error) error {
	var rpc *protocol.Error
	if errors.As(err, &rpc) && rpc.Code == protocol.CodeMethodNotFound {
		return fmt.Errorf("%w; the server is older than this CLI, and an admin updates it with aether server update", err)
	}
	return err
}

func printConnectors(w io.Writer, list protocol.ConnectorListResult, code *protocol.ConnectorCodeResult, now time.Time) error {
	connectorURL := list.URL
	if code != nil {
		connectorURL = code.URL
	}
	if connectorURL != "" && connectorURL == list.UniversalURL {
		_, _ = fmt.Fprintf(w, "Install Aether from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:\n  URL   %s\n", connectorURL)
	} else {
		_, _ = fmt.Fprintf(w, "Add this server to ChatGPT or Claude as a custom connector:\n  URL   %s\n", connectorURL)
	}
	if code == nil {
		_, _ = fmt.Fprintln(w, "  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow")
	} else {
		expires, err := time.Parse(time.RFC3339, code.ExpiresAt)
		if err != nil {
			return fmt.Errorf("connector.code: expires_at: %w", err)
		}
		_, _ = fmt.Fprintf(w, "  Code  %s   one use, expires in %d minutes; paste it on the page that opens when you connect, if it asks\n",
			code.Code, int(expires.Sub(now).Round(time.Minute)/time.Minute))
	}
	if list.UniversalURL != "" && connectorURL != list.UniversalURL {
		_, _ = fmt.Fprintf(w, "To use the one URL %s instead, link a GitHub account: aether member link --github <login>\n", list.UniversalURL)
	}
	if len(list.Connectors) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w, "Connected:")
	var own []string
	for _, c := range list.Connectors {
		added, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			return fmt.Errorf("connector %s: created_at: %w", c.ID, err)
		}
		used := "never used"
		if c.LastUsedAt != "" {
			last, err := time.Parse(time.RFC3339, c.LastUsedAt)
			if err != nil {
				return fmt.Errorf("connector %s: last_used_at: %w", c.ID, err)
			}
			used = "last used " + ago(now.Sub(last))
		}
		at := ""
		switch c.Resource {
		case "":
		case list.UniversalURL:
			at = "  universal URL"
		default:
			at = "  at " + c.Resource
			own = append(own, c.ID)
		}
		_, _ = fmt.Fprintf(w, "  %s  %s (%s)  scopes %s  added %s  %s%s\n", c.ID, c.ClientName, c.RedirectHost, strings.Join(c.Scopes, ","),
			added.Local().Format(time.DateOnly), used, at)
	}
	if len(own) > 0 && connectorURL == list.UniversalURL {
		_, _ = fmt.Fprintf(w, "Move %s to the universal URL: connect again at %s, then aether mcp revoke <id>\n", strings.Join(own, ", "), list.UniversalURL)
	}
	_, _ = fmt.Fprintln(w, "Remove one: aether mcp revoke <id>")
	return nil
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}
