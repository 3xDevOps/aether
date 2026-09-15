package mcpbridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCallAllowsMaximumInboxWait(t *testing.T) {
	coord := newFakeCoord(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordInbox {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
		var params protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode inbox params: %v", err)
		}
		if params.WaitSeconds != protocol.CoordMaxInboxWaitSeconds {
			t.Fatalf("wait_seconds = %d, want %d", params.WaitSeconds, protocol.CoordMaxInboxWaitSeconds)
		}
		return protocol.Response{Result: json.RawMessage(`{"messages":[]}`)}
	})

	var result protocol.CoordInboxResult
	if err := Call(context.Background(), coord.path, protocol.MethodCoordInbox,
		protocol.CoordInboxParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds}, &result); err != nil {
		t.Fatalf("maximum inbox wait: %v", err)
	}
	if result.Messages == nil || len(result.Messages) != 0 {
		t.Fatalf("result = %+v, want a valid empty inbox", result)
	}
	want := callMargin + time.Duration(protocol.CoordMaxInboxWaitSeconds)*time.Second
	if got := callTimeout(protocol.MethodCoordInbox, protocol.CoordInboxParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds}); got != want {
		t.Fatalf("maximum inbox timeout = %s, want %s", got, want)
	}
}

func TestCallTimeoutAllowsReportCaptureBudget(t *testing.T) {
	if got, want := callTimeout(protocol.MethodCoordReport, protocol.CoordReportParams{}), callReportTimeout; got != want {
		t.Fatalf("coord.report timeout = %s, want %s", got, want)
	}
	if got := callTimeout(protocol.MethodCoordStatus, nil); got != callDefaultTimeout {
		t.Fatalf("coord.status timeout = %s, want default %s", got, callDefaultTimeout)
	}
}

func TestMCPInboxAllowsMaximumWait(t *testing.T) {
	coord := newFakeCoord(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodCoordInbox {
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound}}
		}
		var params protocol.CoordInboxParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode inbox params: %v", err)
		}
		if params.WaitSeconds != protocol.CoordMaxInboxWaitSeconds {
			t.Fatalf("wait_seconds = %d, want %d", params.WaitSeconds, protocol.CoordMaxInboxWaitSeconds)
		}
		return protocol.Response{Result: json.RawMessage(`{"messages":[]}`)}
	})
	session := session(t, coord.path, nil)
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolInbox,
		Arguments: protocol.CoordInboxParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds},
	})
	if err != nil {
		t.Fatalf("maximum MCP inbox wait: %v", err)
	}
	if res.IsError {
		t.Fatalf("maximum MCP inbox wait returned error: %+v", res.Content)
	}
	var result protocol.CoordInboxResult
	decodeStructured(t, res, &result)
	if result.Messages == nil || len(result.Messages) != 0 {
		t.Fatalf("result = %+v, want a valid empty inbox", result)
	}
}
