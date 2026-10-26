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
	"time"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
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
	// coordtransport.SocketPath, or AssistantSocketPath with Assistant.
	Socket string
	// Assistant serves the member assistant's tool set instead of the run
	// coordination tools.
	Assistant bool
	// In and Out are the MCP stdio streams; nil means os.Stdin/os.Stdout.
	In  io.ReadCloser
	Out io.WriteCloser
}

// Run serves MCP until the client disconnects or ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Socket == "" {
		cfg.Socket = coordtransport.SocketPath
		if cfg.Assistant {
			cfg.Socket = AssistantSocketPath
		}
	}
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = nopCloser{os.Stdout}
	}

	g := newGate()
	var opts *mcp.ServerOptions
	if !cfg.Assistant {
		opts = &mcp.ServerOptions{Instructions: instructions(ctx, cfg.Socket)}
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version.String()}, opts)
	if cfg.Assistant {
		registerAssistantTools(srv, func(ctx context.Context, method string, params, out any) error {
			return coordtransport.Call(ctx, cfg.Socket, method, params, out)
		})
	} else if err := registerTools(srv, cfg.Socket, g); err != nil {
		return err
	} else {
		registerChildTools(ctx, srv, cfg.Socket)
	}
	if err := srv.Run(ctx, &mcp.IOTransport{
		Reader: g.reader(cfg.In),
		Writer: g.writer(cfg.Out),
	}); err != nil {
		return fmt.Errorf("mcpbridge: %w", err)
	}
	return nil
}

// instructions are the run's role card, or only its standalone reporting
// rules when the agent's system prompt already carries the card. Without a
// status answer they claim no reporting rules.
func instructions(ctx context.Context, socket string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var status protocol.CoordStatusResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordStatus, nil, &status); err != nil {
		return coordhooks.RoleCard(coordhooks.Guide{})
	}
	g := coordhooks.StatusGuide(status)
	if os.Getenv(coordtransport.RoleCardEnv) == "1" {
		return coordhooks.Brief(g)
	}
	return coordhooks.RoleCard(g)
}

// nopCloser adapts os.Stdout, which the bridge must not close, to the
// io.WriteCloser the transport takes.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
