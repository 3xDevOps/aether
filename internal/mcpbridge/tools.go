package mcpbridge

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The three tools, one per coordination method. There is no fourth: an
// inbox read is its own acknowledgement, so nothing here lets the agent
// confirm, replay, or forge one.
const (
	toolStatus = "aether_status"
	toolSend   = "aether_send"
	toolInbox  = "aether_inbox"
)

// MetaErrorCode is the result-metadata key the bridge reports an Aether
// error code under when a tool call fails.
const MetaErrorCode = "aether/error_code"

// metaCallID is the request-metadata key the transport stamps an inbox
// call's JSON-RPC id under on its way to the handler (see gate.go). It is
// written and read entirely inside this package; no client ever sends it,
// and one that did could at most cause its own batch to redeliver.
const metaCallID = "aether/inbox_call_id"

// callID recovers the id the reader stamped on this call. A call without
// one still runs - it simply stages no token, so its batch redelivers
// rather than being acknowledged on a guess.
func callID(req *mcp.CallToolRequest) jsonrpc.ID {
	if req == nil || req.Params == nil {
		return jsonrpc.ID{}
	}
	raw, ok := req.Params.Meta[metaCallID]
	if !ok {
		return jsonrpc.ID{}
	}
	id, err := jsonrpc.MakeID(raw)
	if err != nil {
		return jsonrpc.ID{}
	}
	return id
}

// noArgs is the input of the two tools that take none.
type noArgs struct{}

// inboxOutput is what the agent sees of an inbox read. The token that
// acknowledges the batch is deliberately absent: it is bookkeeping between
// this bridge and the server, and an agent that could present one could
// discard a peer's message it never read.
type inboxOutput struct {
	Messages []protocol.CoordMessage `json:"messages"`
}

func registerTools(srv *mcp.Server, c *client, g *gate) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: toolStatus,
		Description: "Report this run's identity, the other runs it is currently allowed to message " +
			"(the ones the conflict radar has it editing the same files as, plus any whose overlap " +
			"just cleared), the files each pair shares, and how many messages are waiting.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, protocol.CoordStatusResult, error) {
		out := protocol.CoordStatusResult{Peers: []protocol.CoordPeer{}}
		if err := c.call(ctx, protocol.MethodCoordStatus, nil, &out); err != nil {
			return failed(err), protocol.CoordStatusResult{Peers: []protocol.CoordPeer{}}, nil
		}
		if out.Peers == nil {
			out.Peers = []protocol.CoordPeer{}
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolSend,
		Description: "Send one short message to another run you overlap with, to settle who is editing what. " +
			"Only runs aether_status lists may be messaged. Keep it to a sentence or two; this is advisory, " +
			"nothing waits for a reply, so say what you are changing and carry on.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.CoordSendParams) (*mcp.CallToolResult, protocol.CoordSendResult, error) {
		var out protocol.CoordSendResult
		if err := c.call(ctx, protocol.MethodCoordSend, in, &out); err != nil {
			return failed(err), protocol.CoordSendResult{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolInbox,
		Description: "Read the messages other runs have sent this one, oldest first. Reading acknowledges " +
			"the previous batch, so a message can arrive twice if a read is interrupted; that is harmless " +
			"and is why nothing is ever lost.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, inboxOutput, error) {
		empty := inboxOutput{Messages: []protocol.CoordMessage{}}
		// One inbox round trip at a time, claimed here rather than in the
		// transport's reader so a wait never stalls the stream behind it.
		id := callID(req)
		if err := g.claim(ctx, id); err != nil {
			return failed(internalError(protocol.MethodCoordInbox, err)), empty, nil
		}
		defer g.release(id)

		var out protocol.CoordInboxResult
		// No token means no params at all: acknowledging nothing is the
		// absence of the field, not an empty one.
		var params any
		if token := g.token(); token != "" {
			params = protocol.CoordInboxParams{AckToken: token}
		}
		if err := c.call(ctx, protocol.MethodCoordInbox, params, &out); err != nil {
			return failed(err), empty, nil
		}
		g.stage(id, out.AckToken)
		if out.Messages == nil {
			out.Messages = []protocol.CoordMessage{}
		}
		return nil, inboxOutput{Messages: out.Messages}, nil
	})
}

// failed reports a coordination failure the way MCP means a tool to report
// one: a result the agent reads and acts on, never a protocol error that
// would take the session down with it. The Aether code rides along in both
// the text and the result metadata, because what the agent should do next -
// pick another peer, wait and retry, or give up and note the overlap in its
// commit - is exactly what the code distinguishes.
func failed(err error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	res.SetError(err)
	var ce *coordError
	if errors.As(err, &ce) {
		res.Meta = mcp.Meta{MetaErrorCode: ce.Code}
	}
	return res
}
