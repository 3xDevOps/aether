package mcpbridge

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestGatePromotesResponseFromMultiLineWrite(t *testing.T) {
	g := newGate()

	responseID, err := jsonrpc.MakeID(float64(7))
	if err != nil {
		t.Fatalf("make staged response id: %v", err)
	}
	if err := g.claim(t.Context(), responseID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	g.stage(responseID, "token-a")
	g.release(responseID)

	payload := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}

 {"jsonrpc":"2.0","id":7,"result":{}}
`)
	g.outgoing(payload, nil)

	if got := g.token(); got != "token-a" {
		t.Fatalf("token = %q, want token-a", got)
	}
}
