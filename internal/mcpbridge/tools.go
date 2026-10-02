package mcpbridge

import (
	"context"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP tools mirror the complete v3 coordination surface. The status
// reporter remains a wire-only run.report hook; it is not exposed as a tool.
const (
	toolStatus = "aether_status"
	toolSend   = "aether_send"
	toolInbox  = "aether_inbox"
	toolAsk    = "aether_ask"
	toolReply  = "aether_reply"
	toolReport = "aether_report"
)

// MetaErrorCode is the result-metadata key the bridge reports an Aether
// error code under when a tool call fails.
const MetaErrorCode = "aether/error_code"

// metaCallID is the request-metadata key the transport stamps an inbox
// call's JSON-RPC id under on its way to the handler (see gate.go).
const metaCallID = "aether/inbox_call_id"

// callID recovers the id the reader stamped on this call. A call without
// one still runs - it simply stages no token, so its batch redelivers rather
// than being acknowledged on a guess.
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

type inboxOutput struct {
	Messages []protocol.CoordMessage `json:"messages"`
	AckToken string                  `json:"ack_token,omitempty"`
}

func registerTools(srv *mcp.Server, socket string, g *gate) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        toolStatus,
		Description: "Report this run's v3 identity, assignment, authorized peers, and capabilities.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, protocol.CoordStatusResult, error) {
		out := protocol.CoordStatusResult{Peers: []protocol.CoordPeer{}, Capabilities: []string{}}
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordStatus, nil, &out); err != nil {
			return failed(err), out, nil
		}
		if out.Peers == nil {
			out.Peers = []protocol.CoordPeer{}
		}
		if out.Capabilities == nil {
			out.Capabilities = []string{}
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolSend,
		Description: "Send one durable, attributed message to an authorized peer. " +
			"An explicit stable idempotency_key is required; retries with the same key return the original receipt.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.CoordSendParams) (*mcp.CallToolResult, protocol.CoordSendResult, error) {
		var out protocol.CoordSendResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordSend, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolInbox,
		Description: "Read the at-least-once inbox, oldest first. The returned ack_token " +
			"acknowledges exactly this batch when supplied on the next read; replay is safe.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in protocol.CoordInboxParams) (*mcp.CallToolResult, protocol.CoordInboxResult, error) {
		empty := protocol.CoordInboxResult{Messages: []protocol.CoordMessage{}}
		id := callID(req)
		if err := g.claim(ctx, id); err != nil {
			return failed(err), empty, nil
		}
		defer g.release(id)
		// If the caller does not provide an explicit token, carry forward the
		// last response that reached the MCP stream. This preserves the simple
		// natural-checkpoint loop while exposing the token for explicit replay.
		if in.AckToken == "" {
			in.AckToken = g.token()
		}
		var out protocol.CoordInboxResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordInbox, in, &out); err != nil {
			return failed(err), empty, nil
		}
		g.stage(id, out.AckToken)
		if out.Messages == nil {
			out.Messages = []protocol.CoordMessage{}
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolAsk,
		Description: "Ask an authorized peer a durable, correlated question. " +
			"An explicit stable idempotency_key is required; retries with the same key return the original question_id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.CoordAskParams) (*mcp.CallToolResult, protocol.CoordAskResult, error) {
		var out protocol.CoordAskResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordAsk, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolReply,
		Description: "Reply to a question using its question_id. The reply stays correlated and durable. " +
			"An explicit stable idempotency_key is required.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.CoordReplyParams) (*mcp.CallToolResult, protocol.CoordReplyResult, error) {
		var out protocol.CoordReplyResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordReply, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: toolReport,
		Description: "Submit one durable success, failure, or blocked outcome with bounded evidence references. " +
			"An explicit stable idempotency_key is required; the server captures evidence before accepting it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protocol.CoordReportParams) (*mcp.CallToolResult, protocol.CoordReportResult, error) {
		var out protocol.CoordReportResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodCoordReport, in, &out); err != nil {
			return failed(err), out, nil
		}
		return nil, out, nil
	})
}

// failed reports a coordination failure the way MCP means a tool to report
// one: a result the agent reads and acts on, never a protocol error that
// would take the session down with it.
func failed(err error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	res.SetError(err)
	code := coordtransport.ErrorCode(err)
	if code == 0 {
		code = protocol.CodeInternal
	}
	res.Meta = mcp.Meta{MetaErrorCode: code}
	return res
}
