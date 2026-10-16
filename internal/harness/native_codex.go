package harness

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/coordhooks"
)

// codexHookEvents fixes the order of the launch arguments.
var codexHookEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"}

// Codex has no hooks flag. A -c override is a session-flags layer whose hooks
// Codex lists, and trusts once per definition, under one source key shared by
// every worktree. Each value is one argv element and never passes a shell.
func codexNativeLaunch() (NativeLaunch, error) {
	body, err := coordhooks.Files.ReadFile("codex.json")
	if err != nil {
		return NativeLaunch{}, fmt.Errorf("read codex native coordination asset: %w", err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return NativeLaunch{}, fmt.Errorf("decode codex native coordination asset: %w", err)
	}
	var args []string
	for _, event := range codexHookEvents {
		groups, ok := doc.Hooks[event]
		if !ok {
			return NativeLaunch{}, fmt.Errorf("codex native coordination asset has no %s hook", event)
		}
		var value strings.Builder
		value.WriteString("hooks." + event + "=[")
		for i, group := range groups {
			if i > 0 {
				value.WriteByte(',')
			}
			value.WriteByte('{')
			if group.Matcher != "" {
				value.WriteString("matcher=" + strconv.Quote(group.Matcher) + ",")
			}
			value.WriteString("hooks=[")
			for j, hook := range group.Hooks {
				if j > 0 {
					value.WriteByte(',')
				}
				fmt.Fprintf(&value, "{type=%s,command=%s,timeout=%d}", strconv.Quote(hook.Type), strconv.Quote(hook.Command), hook.Timeout)
			}
			value.WriteString("]}")
		}
		value.WriteByte(']')
		args = append(args, "-c", value.String())
	}
	return NativeLaunch{Args: args}, nil
}
