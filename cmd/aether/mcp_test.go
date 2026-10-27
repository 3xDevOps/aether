package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintConnectors(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	const universal, own = "https://edge.onaether.dev/mcp", "https://edge.onaether.dev/mcp/abc"
	code := &protocol.ConnectorCodeResult{URL: own, Code: "ABCD-EFGH", ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	connectors := []protocol.Connector{
		{ID: "con-1", ClientName: "ChatGPT", RedirectHost: "chatgpt.com", Resource: own, Scopes: []string{"read", "write"},
			CreatedAt: now.UTC().Format(time.RFC3339), LastUsedAt: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
		{ID: "con-2", ClientName: "Claude", RedirectHost: "claude.ai", Resource: universal, Scopes: []string{"read", "write", "control"},
			CreatedAt: now.UTC().Format(time.RFC3339)},
	}
	for _, tt := range []struct {
		name string
		list protocol.ConnectorListResult
		code *protocol.ConnectorCodeResult
		want string
	}{
		{"older server", protocol.ConnectorListResult{Connectors: connectors[:1]}, code, `Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect, if it asks
Connected:
  con-1  ChatGPT (chatgpt.com)  scopes read,write  added 2026-10-18  last used 2h ago  at https://edge.onaether.dev/mcp/abc
Remove one: aether mcp revoke <id>
`},
		{"no GitHub account", protocol.ConnectorListResult{URL: own, UniversalURL: universal}, code, `Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect, if it asks
To use the one URL https://edge.onaether.dev/mcp instead, link a GitHub account: aether member link --github <login>
`},
		{"signing in replaces the code", protocol.ConnectorListResult{Connectors: connectors, URL: universal, UniversalURL: universal, SignIn: true}, nil,
			`Install Aether from the ChatGPT or Claude directory where it is listed, or add this URL as a custom connector:
  URL   https://edge.onaether.dev/mcp
  No code: on the page that opens, sign in to the edge with GitHub, pick this server and select Allow
Connected:
  con-1  ChatGPT (chatgpt.com)  scopes read,write  added 2026-10-18  last used 2h ago  at https://edge.onaether.dev/mcp/abc
  con-2  Claude (claude.ai)  scopes read,write,control  added 2026-10-18  never used  universal URL
Move con-1 to the universal URL: connect again at https://edge.onaether.dev/mcp, then aether mcp revoke <id>
Reconnecting one at the server's own URL asks for a code: aether mcp code
Remove one: aether mcp revoke <id>
`},
	} {
		var out strings.Builder
		if err := printConnectors(&out, tt.list, tt.code, now); err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want {
			t.Errorf("%s: output:\n%s\nwant:\n%s", tt.name, out.String(), tt.want)
		}
	}
}
