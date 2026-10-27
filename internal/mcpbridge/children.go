package mcpbridge

import (
	"context"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// registerChildTools adds the child-run tools when this run's server offers
// child runs at all; the server still refuses a start it does not allow.
func registerChildTools(ctx context.Context, srv *mcp.Server, socket string) {
	var status protocol.CoordStatusResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordStatus, nil, &status); err != nil ||
		!slices.Contains(status.Capabilities, protocol.MethodChildOptions) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "aether_child_options",
		Description: "Whether this run may start child runs, why not, the tree's limits and usage, and each agent and account a child may use with its state: ready, exhausted, not-installed or not-signed-in. " +
			"Call it once before aether_child_start, and again only after an error. Pass harness to list that agent's models.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildOptionsParams) (*mcp.CallToolResult, protocol.ChildOptionsResult, error) {
		var out protocol.ChildOptionsResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildOptions, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: "aether_child_start",
		Description: "Start a focused child run on another agent or model, e.g. an adversarial review, from your last published commit (commit first). " +
			"The child sees only task, so include everything it needs. Its report arrives in aether_inbox and wakes you; follow the receipt's next. " +
			"Honor the number of children and the agents the user asks for. A retry with the same idempotency_key returns the first receipt.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildStartParams) (*mcp.CallToolResult, protocol.ChildReceipt, error) {
		var out protocol.ChildReceipt
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildStart, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "aether_children",
		Description: "List your child runs, or one by run_id, with state, latest report, branch, the account it runs on and whether its base is stale. Message a child with aether_send.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildListParams) (*mcp.CallToolResult, protocol.ChildListResult, error) {
		out := protocol.ChildListResult{Children: []protocol.Child{}}
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildList, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "aether_child_cancel",
		Description: "Stop one of your child runs, or drop it from the queue. `aether-internal child detach` lets one run on without you instead.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildRunParams) (*mcp.CallToolResult, protocol.Child, error) {
		var out protocol.Child
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildCancel, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
}
