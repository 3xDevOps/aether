package edgetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

func rpc(t *testing.T, base, token, method string, params any) (*http.Response, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	resp, text := mcpRequest(t, http.MethodPost, base, http.Header{"Content-Type": {"application/json"},
		"Accept": {"application/json, text/event-stream"}, "Authorization": {"Bearer " + token}}, strings.NewReader(string(body)))
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%s: %s %v", method, text, err)
		}
	}
	return resp, out
}

func toolText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.IsError, res.Content[0].(*mcp.TextContent).Text
}

func TestConnectorProtocolThroughTheEdge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	s := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	base := h.relayURL + edgeproto.MCPPath(s.id)
	session, token := connectWithToken(t, h.control(al, s), client, base, "write", "control")
	if v := session.InitializeResult().ProtocolVersion; v != "2026-07-28" {
		t.Fatalf("go-sdk's session negotiated %s, want 2026-07-28 over server/discover", v)
	}

	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28", "2099-01-01"} {
		resp, out := rpc(t, base, token.AccessToken, "initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "openai-mcp", "version": "1.0.0"}})
		result, _ := out["result"].(map[string]any)
		// 2026-07-28 replaced initialize with server/discover, so an
		// initialize at it or later settles on the newest that has one.
		want := min(version, "2025-11-25")
		if resp.StatusCode != http.StatusOK || result["protocolVersion"] != want || resp.Header.Get("Mcp-Session-Id") != "" {
			t.Fatalf("initialize at %s: %s %v %v, want protocol %s and no session", version, resp.Status, resp.Header, out, want)
		}
	}

	resp, out := rpc(t, base, token.AccessToken, "tools/list", map[string]any{})
	tools, _ := out["result"].(map[string]any)["tools"].([]any)
	if resp.StatusCode != http.StatusOK || len(tools) != 29 || resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("tools/list without initialize: %s %v", resp.Status, out)
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	// ChatGPT budgets about 5000 tokens for a connector's tool
	// definitions; JSON runs about 4 bytes a token.
	t.Logf("tools/list: %d tools, %d bytes of definitions, about %d tokens", len(tools), len(raw), len(raw)/4)
	if len(raw) > 5000*4 {
		t.Errorf("tools/list is %d bytes, over ChatGPT's budget of about %d", len(raw), 5000*4)
	}

	resp, out = rpc(t, base, token.AccessToken, "tools/call", map[string]any{"name": "aether_list_workspaces", "arguments": map[string]any{}})
	if result, _ := out["result"].(map[string]any); resp.StatusCode != http.StatusOK || result["isError"] == true || result["structuredContent"] == nil {
		t.Fatalf("tools/call without initialize: %s %v", resp.Status, out)
	}
	resp, out = rpc(t, base, token.AccessToken, "tools/call", map[string]any{"name": "aether_no_such_tool", "arguments": map[string]any{}})
	if resp.StatusCode != http.StatusOK || out["error"] == nil {
		t.Fatalf("tools/call of an unknown tool: %s %v, want a JSON-RPC error", resp.Status, out)
	}
}

func TestConnectorIsolationThroughTheEdge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	rooms := &fakeRooms{}
	s := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{Rooms: rooms, Missions: &fakeMissions{}})
	other := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	clients := h.login(alice, bob)
	al, bo := clients[0], clients[1]
	h.claimServer(al, s)
	h.claimServer(al, other)
	actl := h.control(al, s)
	h.join(actl, bo, s, string(domain.RoleCollaborator))
	ws := &domain.Workspace{Name: "api", BaseBranch: "main"}
	if err := s.db.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	alicesRun := &domain.Run{WorkspaceID: ws.ID, MemberID: s.memberOf(t, alice).ID, AccountMemberID: s.memberOf(t, alice).ID, Task: "migrate the schema",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, StartedAt: &now}
	if err := s.db.CreateRun(t.Context(), alicesRun); err != nil {
		t.Fatal(err)
	}
	base := h.relayURL + edgeproto.MCPPath(s.id)
	bctl := h.control(bo, s)
	session, token := connectWithToken(t, bctl, client, base, "write", "control")
	run := string(alicesRun.ID)

	if failed, text := toolText(t, session, "aether_message_run", map[string]any{"run_id": run, "message": "use postgres", "idempotency_key": "m-1"}); failed {
		t.Fatalf("bob messaging alice's run in an open workspace: %s", text)
	}
	if posts, _ := rooms.recorded(); len(posts) != 1 || posts[0].ActorID != s.memberOf(t, bob).ID {
		t.Fatalf("room posts = %+v, want bob's message", posts)
	}
	ws.SteerOthers = domain.SteerOthersAdminsOnly
	if err := s.db.UpdateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"aether_message_run": {"run_id": run, "message": "use mysql", "idempotency_key": "m-2"},
		"aether_stop_run":    {"run_id": run},
		"aether_pause_run":   {"run_id": run},
	} {
		if failed, text := toolText(t, session, name, args); !failed || !strings.Contains(text, "permission denied") {
			t.Errorf("%s on alice's run in an owners-only workspace = %v %s, want refused", name, failed, text)
		}
	}
	if got, err := s.db.GetRun(t.Context(), alicesRun.ID); err != nil || got.Status != domain.RunRunning {
		t.Fatalf("alice's run = %+v %v, want it still running", got, err)
	}

	for name, args := range map[string]map[string]any{
		"aether_launch_run": {"workspace_id": string(ws.ID), "harness": "claude", "task": "t", "member_id": string(s.memberOf(t, alice).ID)},
		"aether_list_runs":  {"member_id": string(s.memberOf(t, alice).ID)},
	} {
		if failed, text := toolText(t, session, name, args); !failed || !strings.Contains(text, "member_id") {
			t.Errorf("%s with a member id = %v %s, want refused", name, failed, text)
		}
	}
	// Alice has not shared her agent account with bob.
	for name, args := range map[string]map[string]any{
		"aether_launch_run":   {"workspace_id": string(ws.ID), "harness": "claude", "task": "t", "account_member_id": string(s.memberOf(t, alice).ID)},
		"aether_launch_swarm": {"workspace_id": string(ws.ID), "objective": "o", "harness": "claude", "workers": []any{map[string]any{"harness": "codex", "account_member_id": string(s.memberOf(t, alice).ID)}}},
	} {
		if failed, text := toolText(t, session, name, args); !failed || !strings.Contains(text, "not shared") {
			t.Errorf("%s on an account not shared with bob = %v %s, want refused", name, failed, text)
		}
	}

	call[protocol.MemberRoleResult](t, actl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(s.memberOf(t, bob).ID), Role: string(domain.RoleViewer)})
	if failed, text := toolText(t, session, "aether_launch_run", map[string]any{"workspace_id": string(ws.ID), "harness": "claude", "task": "t", "idempotency_key": "l-1"}); !failed ||
		!strings.Contains(text, "permission denied: launching runs requires the collaborator role") {
		t.Fatalf("a demoted member's launch = %v %s, want refused for the role", failed, text)
	}

	otherBase := h.relayURL + edgeproto.MCPPath(other.id)
	if resp, body := mcpRequest(t, http.MethodPost, otherBase, http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"},
		"Authorization": {"Bearer " + token.AccessToken}}, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); resp.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(resp.Header.Get("WWW-Authenticate"), fmt.Sprintf(`resource_metadata="%s/.well-known/oauth-protected-resource%s"`, h.relayURL, edgeproto.MCPPath(other.id))) {
		t.Fatalf("server %s's token at server %s: %s %v %s, want 401", s.id, other.id, resp.Status, resp.Header, body)
	}
}
