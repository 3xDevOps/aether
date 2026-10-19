package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintConnectors(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)
	code := protocol.ConnectorCodeResult{URL: "https://edge.onaether.dev/mcp/abc", Code: "ABCD-EFGH",
		ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	connectors := []protocol.Connector{
		{ID: "con-1", ClientName: "ChatGPT", RedirectHost: "chatgpt.com", CreatedAt: now.UTC().Format(time.RFC3339), LastUsedAt: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
		{ID: "con-2", ClientName: "Claude", RedirectHost: "claude.ai", CreatedAt: now.UTC().Format(time.RFC3339)},
	}
	var out strings.Builder
	if err := printConnectors(&out, code, connectors, now); err != nil {
		t.Fatal(err)
	}
	want := `Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/abc
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect
Connected:
  con-1  ChatGPT (chatgpt.com)  added 2026-10-18  last used 2h ago
  con-2  Claude (claude.ai)  added 2026-10-18  never used
Remove one: aether mcp revoke <id>
`
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
