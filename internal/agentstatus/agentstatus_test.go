package agentstatus

import (
	"encoding/json"
	"testing"
)

func TestFromClaudeHook(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    Report
		mapped  bool
	}{
		{"prompt submitted", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`, Report{State: Working}, true},
		{"tool starting", `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, Report{State: Working}, true},
		{"tool finished", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, Report{State: Working}, true},
		{"tool failed", `{"hook_event_name":"PostToolUseFailure","tool_name":"Bash"}`, Report{State: Working}, true},
		{"question asked", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`,
			Report{State: Waiting, Reason: ReasonAnswer}, true},
		{"permission requested", `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
			Report{State: Waiting, Reason: ReasonPermission}, true},
		{"permission prompt shown", `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`,
			Report{State: Waiting, Reason: ReasonPermission}, true},
		{"elicitation dialog shown", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`,
			Report{State: Waiting, Reason: ReasonPermission}, true},
		{"elicitation url dialog shown", `{"hook_event_name":"Notification","notification_type":"elicitation_url_dialog"}`,
			Report{State: Waiting, Reason: ReasonPermission}, true},
		{"idle at the prompt", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`,
			Report{State: Waiting, Reason: ReasonInput}, true},
		{"turn ended", `{"hook_event_name":"Stop","stop_hook_active":false}`,
			Report{State: Waiting, Reason: ReasonInput}, true},
		{"turn failed", `{"hook_event_name":"StopFailure"}`, Report{State: Waiting, Reason: ReasonInput}, true},

		{"other notification", `{"hook_event_name":"Notification","notification_type":"auth_success"}`, Report{}, false},
		{"session started", `{"hook_event_name":"SessionStart","source":"startup"}`, Report{}, false},
		{"session ended", `{"hook_event_name":"SessionEnd"}`, Report{}, false},
		{"subagent started", `{"hook_event_name":"SubagentStart"}`, Report{}, false},
		{"subagent stopped", `{"hook_event_name":"SubagentStop"}`, Report{}, false},
		{"compacting", `{"hook_event_name":"PreCompact","trigger":"auto"}`, Report{}, false},
		{"an event a newer CLI invented", `{"hook_event_name":"TeammateIdle"}`, Report{}, false},
		{"no event at all", `{"session_id":"abc"}`, Report{}, false},
		{"not JSON", `Stop`, Report{}, false},
		{"empty", ``, Report{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromClaudeHook([]byte(tc.payload))
			if ok != tc.mapped {
				t.Fatalf("FromClaudeHook(%s) mapped = %v, want %v", tc.payload, ok, tc.mapped)
			}
			if got != tc.want {
				t.Errorf("FromClaudeHook(%s) = %+v, want %+v", tc.payload, got, tc.want)
			}
		})
	}
}

func TestParseState(t *testing.T) {
	for _, want := range []State{Working, Waiting} {
		if got, ok := ParseState(string(want)); !ok || got != want {
			t.Errorf("ParseState(%q) = %q, %v", want, got, ok)
		}
	}
	for _, bad := range []string{"", "Working", "idle", "running"} {
		if got, ok := ParseState(bad); ok {
			t.Errorf("ParseState(%q) = %q, true; want it refused", bad, got)
		}
	}
}

// The settings asset is what makes the hooks exist, so its shape is part
// of the contract: every event the mapping above answers to has to be
// registered, and every registered event has to run the reporter.
func TestClaudeSettingsRegistersTheMappedEvents(t *testing.T) {
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
	if err := json.Unmarshal(ClaudeSettings, &doc); err != nil {
		t.Fatalf("decode %s: %v", ClaudeSettingsName, err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(ClaudeSettings, &keys); err != nil {
		t.Fatalf("decode %s: %v", ClaudeSettingsName, err)
	}
	if len(keys) != 1 {
		t.Errorf("%s has keys %v, want hooks alone: the file is merged into the member's own "+
			"settings and must not carry anything else", ClaudeSettingsName, keys)
	}
	want := []string{
		"UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
		"PermissionRequest", "Notification", "Stop", "StopFailure",
	}
	for _, event := range want {
		entries, ok := doc.Hooks[event]
		if !ok {
			t.Errorf("%s registers no hook for %s, which the mapping answers to", ClaudeSettingsName, event)
			continue
		}
		for _, entry := range entries {
			if entry.Matcher != "" {
				t.Errorf("%s matches %q on %s; the mapping is done in Go and needs every payload",
					ClaudeSettingsName, entry.Matcher, event)
			}
			for _, h := range entry.Hooks {
				if h.Type != "command" || h.Command == "" || h.Timeout <= 0 {
					t.Errorf("%s hook on %s = %+v, want a command hook with a timeout", ClaudeSettingsName, event, h)
				}
			}
		}
	}
	if len(doc.Hooks) != len(want) {
		t.Errorf("%s registers %d events, want exactly the %d the mapping answers to",
			ClaudeSettingsName, len(doc.Hooks), len(want))
	}
}
