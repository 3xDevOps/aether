package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCoordWireV2Golden pins coordination wire v2: every field name,
// every omitted optional, and the order they serialize in. The bridge
// inside a container is built against these bytes and outlives the server
// that wrote them, so a change here that is not a new wire version is a
// break. The error responses are pinned separately by
// TestCoordWireV2ErrorsGolden, which provokes each one from the real
// coord service instead of restating it as a literal.
func TestCoordWireV2Golden(t *testing.T) {
	ack := "01k1h7m4z9q0r8s5t2v6w3x7y1"

	requests := []Request{
		{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: MethodCoordStatus},
		{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: MethodCoordSend,
			Params: mustJSON(t, CoordSendParams{ToRunID: "run_02", Body: "I'm rewriting login(); done in ~10 min."})},
		{JSONRPC: "2.0", ID: json.RawMessage("3"), Method: MethodCoordInbox},
		{JSONRPC: "2.0", ID: json.RawMessage("4"), Method: MethodCoordInbox,
			Params: mustJSON(t, CoordInboxParams{AckToken: ack})},
	}

	success := []Response{
		{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: mustJSON(t, CoordStatusResult{
			WireVersion: CoordWireVersion,
			RunID:       "run_01",
			WorkspaceID: "ws_01",
			MemberID:    "mem_01",
			Task:        "add OAuth login",
			Peers: []CoordPeer{
				{RunID: "run_02", MemberID: "mem_02", Task: "rename the auth helpers",
					Files: []string{"src/auth.go"}, State: CoordPeerActive},
				{RunID: "run_03", MemberID: "mem_03", Task: "split the login form",
					State: CoordPeerGrace, ExpiresAt: "2026-08-10T12:10:00Z"},
			},
			Unread: 1,
		})},
		{JSONRPC: "2.0", ID: json.RawMessage("2"), Result: mustJSON(t, CoordSendResult{MessageID: "msg_01"})},
		{JSONRPC: "2.0", ID: json.RawMessage("3"), Result: mustJSON(t, CoordInboxResult{
			Messages: []CoordMessage{{
				ID:        "msg_02",
				FromRunID: "run_02",
				Body:      "only adding an import - going ahead",
				CreatedAt: "2026-08-10T12:00:05Z",
			}},
			AckToken: ack,
		})},
		// An empty inbox still sends an empty list, never null, and no token.
		{JSONRPC: "2.0", ID: json.RawMessage("4"), Result: mustJSON(t, CoordInboxResult{Messages: []CoordMessage{}})},
	}

	for _, tc := range []struct {
		file  string
		lines []any
	}{
		{"requests.ndjson", anySlice(requests)},
		{"success.ndjson", anySlice(success)},
	} {
		want := goldenLines(t, tc.file)
		if len(want) != len(tc.lines) {
			t.Fatalf("%s has %d lines, want %d", tc.file, len(want), len(tc.lines))
		}
		for i, v := range tc.lines {
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("%s line %d: marshal: %v", tc.file, i+1, err)
			}
			if !bytes.Equal(got, want[i]) {
				t.Errorf("%s line %d:\n got %s\nwant %s", tc.file, i+1, got, want[i])
			}
		}
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return raw
}

func goldenLines(t *testing.T, name string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "coord-v2", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var out [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			out = append(out, line)
		}
	}
	return out
}

func anySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
