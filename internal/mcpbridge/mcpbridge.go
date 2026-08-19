// Package mcpbridge is the in-container half of conflict coordination: the
// stdio MCP server an agent harness launches, mapping exactly three tools -
// aether_status, aether_send, aether_inbox - onto the coordination wire v1
// methods served on the run's unix socket (internal/coord).
//
// The bridge is the server's own binary, staged and bind-mounted read-only
// into the run container, so it long outlives the server process that put
// it there. It therefore holds no state that a restart could invalidate: it
// dials the socket per tool call, and the only thing it remembers between
// calls is the token acknowledging the last inbox batch, which never
// crosses the MCP boundary.
package mcpbridge

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// MountDir is where a run's coordination directory appears inside its
	// container, read-only. The mount is the whole authentication: whoever
	// connects on the socket inside it is that run.
	MountDir = "/run/aether"
	// BinaryPath is where the staged server binary appears inside its
	// container, read-only.
	BinaryPath = "/opt/aether/aether-server"
	// ServerName is the MCP server name a harness config points at.
	ServerName = "aether"
	// SocketPath is the coordination socket inside a container.
	SocketPath = MountDir + "/" + coord.SocketName
)

// Config configures the bridge. The zero value dials SocketPath and speaks
// MCP over the process's stdin and stdout.
type Config struct {
	// Socket is the coordination socket to bridge; empty means SocketPath.
	Socket string
	// In and Out are the MCP stdio streams; nil means os.Stdin/os.Stdout.
	In  io.ReadCloser
	Out io.WriteCloser
}

// Run serves MCP until the client disconnects or ctx is done.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Socket == "" {
		cfg.Socket = SocketPath
	}
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = nopCloser{os.Stdout}
	}

	g := newGate()
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version.String()}, nil)
	registerTools(srv, &client{socket: cfg.Socket}, g)

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
