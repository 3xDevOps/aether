// Package mcpbridge is the in-container half of conflict coordination: the
// stdio MCP server an agent harness launches, mapping the v3 coordination
// tools onto the run's unix socket (internal/coord).
//
// The bridge is the server's own binary, staged and bind-mounted read-only
// into the run container, so it outlives the server process that put it
// there. It therefore holds no state that a restart could invalidate: it
// dials the socket per tool call and lets the coordination service own
// authorization, persistence, and replay.
package mcpbridge

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// ServerName is the MCP server name a harness config points at.
	ServerName = "aether"
)

// Config configures the bridge. The zero value dials coordtransport.SocketPath
// and speaks MCP over the process's stdin and stdout.
type Config struct {
	// Socket is the coordination socket to bridge; empty means the mounted
	// coordtransport.SocketPath.
	Socket string
	// In and Out are the MCP stdio streams; nil means os.Stdin/os.Stdout.
	In  io.ReadCloser
	Out io.WriteCloser
}

// Run serves MCP until the client disconnects or ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Socket == "" {
		cfg.Socket = coordtransport.SocketPath
	}
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = nopCloser{os.Stdout}
	}

	g := newGate()
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version.String()}, nil)
	registerTools(srv, cfg.Socket, g)
	if err := srv.Run(ctx, &mcp.IOTransport{
		Reader: g.reader(cfg.In),
		Writer: g.writer(cfg.Out),
	}); err != nil {
		return fmt.Errorf("mcpbridge: %w", err)
	}
	return nil
}

// nopCloser adapts os.Stdout, which the bridge must not close, to the
// io.WriteCloser the transport takes.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
