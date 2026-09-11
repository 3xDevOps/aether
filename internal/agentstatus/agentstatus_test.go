package agentstatus

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func TestFromOpenCodeEvent(t *testing.T) {
	cases := []struct {
		name   string
		event  string
		status string
		want   Report
		mapped bool
	}{
		{"turn started", "session.status", "busy", Report{State: Working}, true},
		{"turn ended", "session.idle", "", Report{State: Waiting, Reason: ReasonInput}, true},
		{"permission asked", "permission.asked", "", Report{State: Waiting, Reason: ReasonPermission}, true},
		{"question asked", "question.asked", "", Report{State: Waiting, Reason: ReasonAnswer}, true},
		{"permission answered", "permission.replied", "", Report{State: Working}, true},
		{"question answered", "question.replied", "", Report{State: Working}, true},
		{"question rejected", "question.rejected", "", Report{State: Working}, true},

		{"session went idle", "session.status", "idle", Report{}, false},
		{"provider call being retried", "session.status", "retry", Report{}, false},
		{"status with no type", "session.status", "", Report{}, false},
		{"reply streaming in", "message.part.updated", "", Report{}, false},
		{"session created", "session.created", "", Report{}, false},
		{"an event a newer opencode invented", "session.hibernated", "", Report{}, false},
		{"no event at all", "", "", Report{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromOpenCodeEvent(tc.event, tc.status)
			if ok != tc.mapped {
				t.Fatalf("FromOpenCodeEvent(%q, %q) mapped = %v, want %v", tc.event, tc.status, ok, tc.mapped)
			}
			if got != tc.want {
				t.Errorf("FromOpenCodeEvent(%q, %q) = %+v, want %+v", tc.event, tc.status, got, tc.want)
			}
		})
	}
}

// The plugin asset is what makes the opencode events reach the reporter at
// all, so it is run here rather than read: opencode hands the hook every
// event on its bus, and what the plugin does with a subagent's session has
// no other test. The reporter command is stubbed by replacing the one
// constant naming it; the run container's real path is pinned where the
// scheduler mounts it (internal/scheduler registration tests).
func TestOpenCodePluginReportsTheRunsOwnTurn(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("the opencode plugin scenario needs node on PATH")
	}
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports.log")
	stub := filepath.Join(dir, "reporter")
	writePluginFile(t, stub, "#!/bin/sh\necho \"$@\" >> "+reports+"\n", 0o755)

	const binary = `"/opt/aether/aether-server"`
	if bytes.Count(OpenCodePlugin, []byte(binary)) != 1 {
		t.Fatalf("%s does not name %s exactly once; the container has nothing else to run", OpenCodePluginName, binary)
	}
	plugin := bytes.Replace(OpenCodePlugin, []byte(binary), []byte(strconv.Quote(stub)), 1)
	writePluginFile(t, filepath.Join(dir, "plugin.mjs"), string(plugin), 0o644)
	writePluginFile(t, filepath.Join(dir, "drive.mjs"), openCodeDriver, 0o644)

	out, err := exec.Command(node, filepath.Join(dir, "drive.mjs"), reports).CombinedOutput()
	if err != nil {
		t.Fatalf("drive the plugin: %v (%s)", err, out)
	}
	got, err := os.ReadFile(reports)
	if err != nil {
		t.Fatalf("read the reports the plugin posted: %v", err)
	}
	want := strings.Join([]string{
		"report opencode --event session.status --status busy",
		"report opencode --event permission.asked",
		"report opencode --event permission.replied",
		"report opencode --event session.idle",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("the plugin posted\n%s\nwant\n%s", got, want)
	}
}

// openCodeDriver feeds the plugin one turn the way opencode would: the run
// starts, a subagent runs a turn of its own inside it, the agent asks for a
// permission and gets it, and only then does the run's own turn end. The
// subagent's half must be invisible - it is not the member's turn to speak.
const openCodeDriver = `
import { AetherStatus } from "./plugin.mjs"

const hooks = await AetherStatus({})
const status = (sessionID, type) => ({ type: "session.status", properties: { sessionID, status: { type } } })
const events = [
  status("root", "busy"),
  status("sub", "busy"),
  { type: "session.idle", properties: { sessionID: "sub" } },
  { type: "message.part.updated", properties: { part: { sessionID: "root" } } },
  { type: "permission.asked", properties: { sessionID: "root", id: "p1" } },
  { type: "permission.replied", properties: { sessionID: "root", requestID: "p1" } },
  { type: "session.idle", properties: { sessionID: "root" } },
]
for (const event of events) await hooks.event({ event })

// The handler never waits for its own report, so wait for the posts here.
const { readFileSync } = await import("node:fs")
const deadline = Date.now() + 10000
while (Date.now() < deadline) {
  let lines = ""
  try {
    lines = readFileSync(process.argv[2], "utf8")
  } catch {}
  if (lines.split("\n").length > 4) break
  await new Promise((r) => setTimeout(r, 50))
}
`

// writePluginFile writes one file of the plugin scenario.
func writePluginFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
