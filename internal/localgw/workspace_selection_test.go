package localgw

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestWorkspaceSelectionSurvivesGatewayRestartAndIsolatesIdentity(t *testing.T) {
	t.Setenv(cli.ConfigDirEnv, t.TempDir())
	gateway := func(addr, member string) *Gateway {
		t.Helper()
		info, err := json.Marshal(protocol.ServerInfoResult{Member: protocol.Member{ID: member}})
		if err != nil {
			t.Fatal(err)
		}
		g := newVerbGateway(t, &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
			protocol.MethodServerInfo: info,
		}}}, cli.Config{Addr: addr})
		t.Cleanup(func() { _ = g.Close() })
		return g
	}
	selection := func(g *Gateway, body, want string) {
		t.Helper()
		rec := do(g, http.MethodPost, "/local/v1/workspace.selection", body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("selection: %d: %s", rec.Code, rec.Body)
		}
		var result struct {
			WorkspaceID string `json:"workspace_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.WorkspaceID != want {
			t.Fatalf("selection = %q, want %q", result.WorkspaceID, want)
		}
	}
	first := gateway("server-one:2222", "alice")
	selection(first, `{"workspace_id":"second"}`, "second")
	selection(gateway("server-one:2222", "alice"), `{}`, "second")
	selection(gateway("server-one:2222", "bob"), `{}`, "")
	selection(gateway("server-two:2222", "alice"), `{}`, "")
	selection(first, `{"workspace_id":""}`, "")
	selection(gateway("server-one:2222", "alice"), `{}`, "")
}

func TestWorkspaceSelectionRequiresGatewayToken(t *testing.T) {
	t.Setenv(cli.ConfigDirEnv, t.TempDir())
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	defer func() { _ = g.Close() }()
	rec := do(g, http.MethodPost, "/local/v1/workspace.selection", `{"workspace_id":"second"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated selection: %d: %s", rec.Code, rec.Body)
	}
}
