package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestTerminalUsageRejectsMalformedSubcommands(t *testing.T) {
	for _, args := range [][]string{
		{"status", "extra"},
		{"stop", "extra"},
		{"--tab"},
		{"--tab", "dev", "status"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			err := runTerminal(args)
			if err == nil || !strings.Contains(err.Error(), "usage: aether terminal") {
				t.Fatalf("runTerminal(%v) = %v, want usage error", args, err)
			}
		})
	}
}

func TestPrintTerminalStatus(t *testing.T) {
	var out strings.Builder
	err := printTerminalStatus(&out, protocol.TerminalStatusResult{
		Running:   true,
		Image:     "ubuntu:latest",
		StartedAt: "2026-09-03T12:00:00Z",
		Tabs:      []string{"main", "dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"running", "ubuntu:latest", "2026-09-03T12:00:00Z", "main, dev"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}
