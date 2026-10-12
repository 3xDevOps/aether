package acphost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestUsageLimitFromPromptError(t *testing.T) {
	for _, tc := range []struct {
		fixture, message string
		ok               bool
	}{
		{"claude_usage_limit", "You've hit your weekly limit · resets Oct 14, 9am (UTC)", true},
		{"claude_rate_limited", "", false},
		{"codex_usage_limit", "You’ve hit your usage limit. Try again later.", true},
		{"codex_other_error", "", false},
		{"opencode_quota", "Go usage limit exceeded", true},
		{"opencode_rate_limited", "", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			resp := loadErrorResponse(t, filepath.Join("testdata", tc.fixture+".jsonl"))
			m := newMockAgent(t, loadFixture(t, "claude"))
			m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) { return nil, resp }
			s, rec := startMock(t, m, Config{})
			_, refused := s.Prompt(context.Background(), textPrompt("go"), false, nil)
			if reason := rec.waitIdle(t); reason != "error" {
				t.Fatalf("turn ended with %q, want error", reason)
			}
			rec.mu.Lock()
			failed := rec.failed
			rec.mu.Unlock()
			for _, err := range []error{refused, failed} {
				message, ok := UsageLimit(err)
				if ok != tc.ok || message != tc.message {
					t.Errorf("UsageLimit(%v) = %q, %v; want %q, %v", err, message, ok, tc.message, tc.ok)
				}
			}
		})
	}
}

// loadErrorResponse reads the JSON-RPC error response in a fixture, skipping
// its leading # provenance line.
func loadErrorResponse(t *testing.T, path string) *acp.RequestError {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "#") {
			continue
		}
		var resp struct {
			Error *acp.RequestError `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil || resp.Error == nil {
			t.Fatalf("%s: no JSON-RPC error: %v", path, err)
		}
		return resp.Error
	}
	t.Fatalf("%s: no response line", path)
	return nil
}
