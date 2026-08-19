package adapter

import (
	"os"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/events"
)

// claudeSessionID is the harness session ID both fixtures carry.
const claudeSessionID = "b3e29e5a-0f1c-4c8e-a9d2-7f6e5d4c3b2a"

// fixturePayloads is the exact payload sequence both Claude fixtures
// (clean and TTY-mangled) must produce: every AgentEventKind plus the
// final metered cost report.
func fixturePayloads() []events.Payload {
	return []events.Payload{
		events.AgentEventPayload{Kind: events.AgentSession, HarnessSessionID: claudeSessionID},
		events.AgentEventPayload{Kind: events.AgentToolCall, Tool: "Bash", ToolUseID: "toolu_01Bash", Detail: "go test ./..."},
		events.AgentEventPayload{Kind: events.AgentToolResult, ToolUseID: "toolu_01Bash"},
		events.AgentEventPayload{Kind: events.AgentSubagent, Tool: "Task", ToolUseID: "toolu_02Task", Detail: "Audit retry error handling"},
		events.AgentEventPayload{Kind: events.AgentToolResult, ToolUseID: "toolu_02Task"},
		events.AgentEventPayload{Kind: events.AgentPause, Tool: "ExitPlanMode", ToolUseID: "toolu_03Plan", Detail: "1. Fix the backoff reset in the retry loop\n2. Re-run the test suite"},
		events.AgentEventPayload{Kind: events.AgentToolResult, ToolUseID: "toolu_03Plan"},
		events.AgentEventPayload{Kind: events.AgentToolCall, Tool: "Read", ToolUseID: "toolu_04Read", Detail: "/workspace/internal/retry/retry.go"},
		events.AgentEventPayload{Kind: events.AgentToolResult, ToolUseID: "toolu_04Read", IsError: true},
		events.RunCostPayload{InputTokens: 40124, OutputTokens: 1834, CostUSD: 0.2841, Metered: true},
	}
}

// consumeFixture pumps the fixture's raw bytes through the normalizer and
// the claude adapter in chunk-sized reads, mimicking arbitrary PTY read
// boundaries.
func consumeFixture(t *testing.T, path string, chunk int) []events.Payload {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	a, ok := ForHarness("claude")
	if !ok {
		t.Fatal(`ForHarness("claude") has no adapter`)
	}
	var norm LineNormalizer
	var got []events.Payload
	for len(data) > 0 {
		n := chunk
		if n <= 0 || n > len(data) {
			n = len(data)
		}
		for _, line := range norm.Feed(data[:n]) {
			got = append(got, a.ConsumeLine(line)...)
		}
		data = data[n:]
	}
	if line, ok := norm.Flush(); ok {
		got = append(got, a.ConsumeLine(line)...)
	}
	return got
}

func assertPayloads(t *testing.T, got, want []events.Payload) {
	t.Helper()
	for i := 0; i < len(got) && i < len(want); i++ {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("payload %d:\n got  %#v\n want %#v", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d payloads, want %d", len(got), len(want))
	}
}

func TestClaudeCleanFixture(t *testing.T) {
	assertPayloads(t, consumeFixture(t, "testdata/claude_clean.jsonl", 0), fixturePayloads())
}

func TestClaudeTTYFixture(t *testing.T) {
	// A 7-byte chunk size splits lines and escape sequences across reads;
	// the fixture itself adds CRLF endings, ANSI/OSC chrome, bare-CR
	// spinner redraws, and an unterminated final line.
	assertPayloads(t, consumeFixture(t, "testdata/claude_tty.jsonl", 7), fixturePayloads())
}

func TestClaudeUnparseableLinesAreOpaque(t *testing.T) {
	a, _ := ForHarness("claude")
	for _, line := range []string{
		"",
		"plain terminal output",
		"{not json at all",
		`{"type":"assistant","message":{"content":"a plain string"}}`,
		`{"type":"weird_future_type","payload":42}`,
		`[1,2,3]`,
	} {
		if got := a.ConsumeLine(line); len(got) != 0 {
			t.Errorf("ConsumeLine(%q) = %#v, want none", line, got)
		}
	}
}

func TestForHarness(t *testing.T) {
	if _, ok := ForHarness("claude"); !ok {
		t.Error(`no adapter for "claude"`)
	}
	for _, h := range []string{"codex", "", "CLAUDE"} {
		if _, ok := ForHarness(h); ok {
			t.Errorf("unexpected adapter for %q", h)
		}
	}
}

func TestDetailTruncation(t *testing.T) {
	long := make([]byte, maxDetail+50)
	for i := range long {
		long[i] = 'x'
	}
	a, _ := ForHarness("claude")
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"` + string(long) + `"}}]}}`
	got := a.ConsumeLine(line)
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
	p := got[0].(events.AgentEventPayload)
	if len(p.Detail) != maxDetail+len("...") {
		t.Errorf("detail length = %d, want %d", len(p.Detail), maxDetail+3)
	}
}
