package dashboard

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestCapabilitiesDescribeTheRemoteGateway: a client probes what the
// gateway can do instead of hard-coding a transport, so the answer must
// name the remote surface, carry exactly the allowlist (never a
// credential-minting method), and sit behind a token like every read.
func TestCapabilitiesDescribeTheRemoteGateway(t *testing.T) {
	e := newEnv(t)

	if status, body := e.get("", "/api/v1/capabilities"); status != http.StatusUnauthorized {
		t.Fatalf("untokened capabilities status = %d, want 401 (%s)", status, body)
	}

	token := e.mint(e.viewer)
	status, body := e.get(token, "/api/v1/capabilities")
	if status != http.StatusOK {
		t.Fatalf("capabilities status = %d, want 200 (%s)", status, body)
	}
	var got protocol.GatewayCapabilities
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if got.Gateway != "remote" {
		t.Errorf("gateway = %q, want %q", got.Gateway, "remote")
	}
	if !slices.Contains(got.Methods, protocol.MethodRunList) {
		t.Errorf("methods = %v, want %s listed", got.Methods, protocol.MethodRunList)
	}
	if slices.Contains(got.Methods, protocol.MethodMemberInvite) {
		t.Errorf("methods = %v: %s must never be advertised to a bearer token", got.Methods, protocol.MethodMemberInvite)
	}
	if !slices.IsSorted(got.Methods) {
		t.Errorf("methods = %v, want sorted", got.Methods)
	}
	if want := []string{"events", "attach"}; !slices.Equal(got.WS, want) {
		t.Errorf("ws = %v, want %v", got.WS, want)
	}
	if got.Local != nil {
		t.Errorf("local = %v, want absent on the remote gateway", got.Local)
	}
}
