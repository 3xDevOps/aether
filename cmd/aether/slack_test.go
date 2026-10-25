package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintSlack(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name  string
		print func(*strings.Builder) error
		want  string
	}{
		{"code", func(w *strings.Builder) error {
			return printSlackCode(w, protocol.SlackLinkResult{Code: "ABCD-EFGH", ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339),
				Bot: "aether", Team: "Acme"}, now)
		}, "Send this code to aether in Slack workspace Acme in a direct message:\n  ABCD-EFGH   one use, expires in 10 minutes\n"},
		{"not set up", func(w *strings.Builder) error { return printSlackStatus(w, protocol.SlackStatusResult{}) },
			"This server has no Slack bot; an admin sets one up with: sudo aether-server slack setup (docs/slack.md)\n"},
		{"not linked", func(w *strings.Builder) error {
			return printSlackStatus(w, protocol.SlackStatusResult{Configured: true, Error: "slack: auth.test: invalid_auth"})
		}, "Slack bot  (unknown yet) in workspace (unknown yet), not connected: slack: auth.test: invalid_auth\n" +
			"Your link  none; link your Slack account with: aether slack link\n"},
		{"linked", func(w *strings.Builder) error {
			return printSlackStatus(w, protocol.SlackStatusResult{Configured: true, Connected: true, Bot: "aether", Team: "Acme",
				Link: &protocol.SlackLink{UserID: "U1", TeamID: "T1", LinkedAt: now.UTC().Format(time.RFC3339)}})
		}, "Slack bot  aether in workspace Acme, connected\nYour link  Slack user U1, since 2026-10-25\nUnlink it: aether slack unlink\n"},
	} {
		var out strings.Builder
		if err := tc.print(&out); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.String() != tc.want {
			t.Errorf("%s:\n%s\nwant:\n%s", tc.name, out.String(), tc.want)
		}
	}
}
