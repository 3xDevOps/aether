package mcpbridge

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/ops"
)

// AssistantSocketPath is the assistant tool socket inside the member's
// environment container.
const AssistantSocketPath = "/run/aether-assistant/assistant.sock"

func registerAssistantTools(srv *mcp.Server, socket string) {
	ops.RegisterAssistant(srv, func(ctx context.Context, method string, params, out any) error {
		return coordtransport.ProtocolError(coordtransport.Call(ctx, socket, method, params, out))
	})
}
