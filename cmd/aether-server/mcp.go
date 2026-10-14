package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/3xDevOps/Aether/internal/mcpbridge"
)

// mcp runs the in-container MCP bridge. It is deliberately absent from the
// usage text: no operator runs it. The server stages this binary into a run
// container read-only and the agent's harness launches it there, on stdio,
// against the coordination socket mounted with it.
func mcp(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	socket := fs.String("socket", "", "socket to bridge (default: the run coordination socket, or the assistant socket with --assistant)")
	assistant := fs.Bool("assistant", false, "serve the member assistant's tools instead of the run coordination tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcpbridge.Run(ctx, mcpbridge.Config{Socket: *socket, Assistant: *assistant})
}
