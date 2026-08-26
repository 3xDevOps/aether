package events

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestPayloadCodecRoundtrip(t *testing.T) {
	payloads := []Payload{
		RunStatusPayload{From: domain.RunRunning, To: domain.RunFailed, Reason: "agent exited 1"},
		RunDiffPayload{Files: []FileDiffStat{{Path: "main.go", Additions: 10, Deletions: 2}}},
		RunCostPayload{InputTokens: 1200, OutputTokens: 340, CostUSD: 0.42, Metered: true},
		PresencePayload{State: PresenceWatching},
		ApprovalPayload{RequestID: "req_1", Action: "rm -rf build", Decision: ApprovalDenied},
		TimelinePayload{Kind: TimelineSteer, Message: "focus on the failing test"},
		GitBranchPayload{WorkspaceID: "ws_1", Branch: "aether/run-r1-fix", Commit: "abc123"},
		AgentEventPayload{Kind: AgentToolCall, Tool: "Bash", ToolUseID: "toolu_1", Detail: "go test ./..."},
		ProfilePayload{Member: "mem_1", Harness: "claude", SnapshotID: "snap_1", Action: ProfileActionPut},
		SyncConflictPayload{RunID: "r1", SyncSessionID: "sync_1", Files: []string{"main.go"}, Members: []domain.MemberID{"m1", "m2"}},
		OverlapPayload{With: []OverlapPeer{{RunID: "r2", Files: []string{"main.go"}}}},
		BudgetPayload{State: BudgetExceeded, SpendUSD: 12.5, LimitUSD: 10, WarnUSD: 8,
			UnmeteredRuns: 2, Reason: "new run refused"},
	}
	seen := map[Type]bool{}
	for _, p := range payloads {
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal %T: %v", p, err)
		}
		got, err := DecodePayload(p.EventType(), body)
		if err != nil {
			t.Fatalf("decode %T: %v", p, err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Errorf("roundtrip %T: got %#v, want %#v", p, got, p)
		}
		seen[p.EventType()] = true
	}
	if len(seen) != len(payloadCodecs) {
		t.Errorf("roundtrip covered %d types, codec registry has %d", len(seen), len(payloadCodecs))
	}
}

func TestDecodePayloadUnknownType(t *testing.T) {
	_, err := DecodePayload("nope", []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("want unknown-type error, got %v", err)
	}
}

func TestFilterMatches(t *testing.T) {
	e := Event{
		WorkspaceID: "w1",
		RunID:       "r1",
		Type:        TypeRunStatus,
	}
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"zero matches all", Filter{}, true},
		{"workspace match", Filter{Workspace: "w1"}, true},
		{"workspace mismatch", Filter{Workspace: "w2"}, false},
		{"run match", Filter{Run: "r1"}, true},
		{"run mismatch", Filter{Run: "r2"}, false},
		{"type match", Filter{Types: []Type{TypePresence, TypeRunStatus}}, true},
		{"type mismatch", Filter{Types: []Type{TypePresence}}, false},
		{"all fields match", Filter{Workspace: "w1", Run: "r1", Types: []Type{TypeRunStatus}}, true},
		{"one field mismatch", Filter{Workspace: "w1", Run: "r2", Types: []Type{TypeRunStatus}}, false},
	}
	for _, tc := range cases {
		if got := tc.f.Matches(e); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
