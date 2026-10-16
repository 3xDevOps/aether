package mcpbridge

import (
	"encoding/json"
	"io"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestAssistantTools(t *testing.T) {
	coord := newFakeCoord(t, func(req protocol.Request) protocol.Response {
		return protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"runs":[{"id":"run-1"}]}`)}
	})

	toBridge, fromClient := io.Pipe()
	toClient, fromBridge := io.Pipe()
	go func() {
		_ = Run(t.Context(), Config{Socket: coord.path, Assistant: true, In: toBridge, Out: fromBridge})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: toClient, Writer: fromClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	var names []string
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 16 || slices.Contains(names, toolSend) || !slices.Contains(names, "aether_stop_run") {
		t.Fatalf("tools: %v", names)
	}

	var out struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	callTool(t, cs, "aether_list_runs", map[string]any{"active_only": true}, &out)
	if len(out.Runs) != 1 || out.Runs[0].ID != "run-1" {
		t.Fatalf("runs: %+v", out)
	}
	callTool(t, cs, "aether_run_history", map[string]any{"run_id": "run-1"}, nil)
	callTool(t, cs, "aether_message_run", map[string]any{"run_id": "run-1", "message": "hi"}, nil)

	reqs := coord.requests
	var hist protocol.RunACPHistoryParams
	if err := json.Unmarshal(reqs[1].Params, &hist); err != nil || hist.Limit != defaultHistoryLimit {
		t.Fatalf("history params: %s %v", reqs[1].Params, err)
	}
	var inj protocol.RunInjectParams
	if err := json.Unmarshal(reqs[2].Params, &inj); err != nil || inj.IdempotencyKey == "" {
		t.Fatalf("inject params: %s %v", reqs[2].Params, err)
	}
}
