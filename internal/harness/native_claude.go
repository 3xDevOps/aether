package harness

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
)

// Claude Code takes one --settings value, so the status reporter, the inbox
// hooks and the idle-wake waiter share a single document.
func claudeNativeLaunch(dir string) (NativeLaunch, error) {
	inbox, err := coordhooks.Files.ReadFile("claude.json")
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("read claude native coordination asset: %w", err)
	}
	merged, err := decodeClaudeHooks(agentstatus.ClaudeSettings)
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("decode claude status settings: %w", err)
	}
	extra, err := decodeClaudeHooks(inbox)
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("decode claude native coordination asset: %w", err)
	}
	for event, entries := range extra {
		merged[event] = append(merged[event], entries...)
	}
	wake, err := json.Marshal(map[string][]map[string]any{"hooks": {{
		"type":        "command",
		"command":     coordtransport.CLIPath + " hook claude wake",
		"async":       true,
		"asyncRewake": true,
		"timeout":     coordhooks.ClaudeWakeTimeout,
	}}})
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("encode claude wake hook: %w", err)
	}
	for _, event := range []string{"SessionStart", "Stop"} {
		merged[event] = append(merged[event], wake)
	}
	body, err := json.Marshal(map[string]map[string][]json.RawMessage{"hooks": merged})
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("encode claude native settings: %w", err)
	}
	return NativeLaunch{
		Files:          map[string][]byte{agentstatus.ClaudeSettingsName: body},
		Args:           []string{"--settings", path.Join(dir, agentstatus.ClaudeSettingsName)},
		ReplacesStatus: true,
		Reporter:       ReporterFull,
	}, nil
}

func decodeClaudeHooks(body []byte) (map[string][]json.RawMessage, error) {
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if doc.Hooks == nil {
		doc.Hooks = make(map[string][]json.RawMessage)
	}
	return doc.Hooks, nil
}
