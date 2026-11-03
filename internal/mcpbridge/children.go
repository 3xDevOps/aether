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
		Name: "aether_subsession_options",
		Description: "Whether this session may start subsessions, why not, the tree's limits and usage, and each agent and account a subsession may use with its state: ready, exhausted, not-installed or not-signed-in. " +
			"Call it once before aether_subsession_start, and again only after an error. Pass harness to list that agent's models.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildOptionsParams) (*mcp.CallToolResult, protocol.ChildOptionsResult, error) {
		var out protocol.ChildOptionsResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildOptions, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: "aether_subsession_start",
		Description: "Start a subsession: a separate Aether session with a fresh context and its own container and branch from your last published commit (commit first), on any installed agent and model, on your session owner's own account. " +
			"Use one for what your own subagents cannot give: another agent or model, a separate branch or build, long work that would crowd out your context, or an independent review; do quick or tightly coupled work yourself. " +
			"The subsession sees only task: state the goal, the files it owns and must not touch, how to verify and what to report, with repository-relative paths. Its report arrives in aether_inbox and wakes you; follow the receipt's next. A subsession cannot start subsessions. " +
			"Honor the number of subsessions and the agents the user asks for. A retry with the same idempotency_key returns the first receipt.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildStartParams) (*mcp.CallToolResult, protocol.ChildReceipt, error) {
		var out protocol.ChildReceipt
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildStart, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "aether_subsessions",
		Description: "List your subsessions, or one by run_id, with state, latest report, branch and the account it runs on. Message a subsession with aether_send.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildListParams) (*mcp.CallToolResult, protocol.ChildListResult, error) {
		out := protocol.ChildListResult{Children: []protocol.Child{}}
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildList, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "aether_subsession_cancel",
		Description: "Stop one of your subsessions, or drop it from the queue.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.ChildRunParams) (*mcp.CallToolResult, protocol.Child, error) {
		var out protocol.Child
		if err := coordtransport.Call(ctx, socket, protocol.MethodChildCancel, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
}
