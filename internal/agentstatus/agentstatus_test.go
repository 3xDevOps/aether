package agentstatus

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

func TestFromCodexNotify(t *testing.T) {
	cases := []struct {
		name   string
		arg    string
		want   Report
		mapped bool
	}{
		{"turn complete", `{"type":"agent-turn-complete","turn-id":"t1","last-assistant-message":"done"}`,
			Report{State: Waiting, Reason: ReasonInput}, true},
		{"an event a newer CLI invented", `{"type":"agent-turn-started"}`, Report{}, false},
		{"no type at all", `{"turn-id":"t1"}`, Report{}, false},
		{"not JSON", `agent-turn-complete`, Report{}, false},
		{"empty", ``, Report{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromCodexNotify(tc.arg)
			if ok != tc.mapped {
				t.Fatalf("FromCodexNotify(%s) mapped = %v, want %v", tc.arg, ok, tc.mapped)
			}
			if got != tc.want {
				t.Errorf("FromCodexNotify(%s) = %+v, want %+v", tc.arg, got, tc.want)
			}
		})
	}
}

// The plugin asset is what makes the opencode events reach the reporter at
// all, so it is run here rather than read: opencode hands the hook every
// event on its bus, and what the plugin does with a subagent's session has
// no other test, nor does the order its reports reach the server in. The
// reporter command is stubbed by replacing the one constant naming it; the
// run container's real path is pinned where the scheduler mounts it
// (internal/scheduler registration tests).
func TestOpenCodePluginReportsTheRunsOwnTurn(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports.log")
	stub := filepath.Join(dir, "reporter")
	// A reporter that takes longer the earlier its event was posted. The
	// plugin runs one at a time, so the log below comes out in event order;
	// a plugin that spawned them together would write it upside down.
	writePluginFile(t, stub, "#!/bin/sh\ncase \"$*\" in\n"+
		"*session.status*) sleep 0.4 ;;\n"+
		"*permission.asked*) sleep 0.3 ;;\n"+
		"*permission.replied*) sleep 0.2 ;;\n"+
		"esac\necho \"$@\" >> "+reports+"\n", 0o755)
	stageOpenCodePlugin(t, dir, stub)

	out := driveOpenCodePlugin(t, dir, openCodeDriver, reports, 4)
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
	if len(strings.TrimSpace(out)) != 0 {
		t.Errorf("the plugin said %s, want nothing: every report reached the server", out)
	}
}

// openCodeDriver feeds the plugin one turn the way opencode would: the run
// starts - several times over, opencode says busy once for the prompt, once
// for the runner and once per step - a subagent runs a turn of its own
// inside it, the agent asks for a permission and gets it, and only then
// does the run's own turn end. Every busy after the first and the
// subagent's whole half must be invisible: neither is the member's turn to
// speak, and a reporter process per step is a process per step.
const openCodeDriver = `
import { AetherStatus } from "./plugin.mjs"

// opencode's client, of which the plugin uses one call: what it logs is
// this driver's stdout.
const client = { app: { log: async ({ body }) => console.log(JSON.stringify(body)) } }
const hooks = await AetherStatus({ client })
const status = (sessionID, type) => ({ type: "session.status", properties: { sessionID, status: { type } } })
const events = [
  status("root", "busy"),
  status("root", "busy"),
  status("sub", "busy"),
  status("root", "busy"),
  { type: "session.idle", properties: { sessionID: "sub" } },
  { type: "message.part.updated", properties: { part: { sessionID: "root" } } },
  { type: "permission.asked", properties: { sessionID: "root", id: "p1" } },
  { type: "permission.replied", properties: { sessionID: "root", requestID: "p1" } },
  { type: "session.idle", properties: { sessionID: "root" } },
]
for (const event of events) await hooks.event({ event })
`

// TestOpenCodePluginKeepsTheRunParkedUntilTheLastPromptIsAnswered is the
// other half of the set the plugin speaks for: a run can have a permission
// and a question open at once, and the first answer is not the member
// handing the run back. The retry in the middle is the second rule - a
// status that is not busy is not a session to wait for - and a session the
// plugin never saw start would otherwise sit in the busy set forever and
// swallow the run's own idle.
func TestOpenCodePluginKeepsTheRunParkedUntilTheLastPromptIsAnswered(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports.log")
	stub := filepath.Join(dir, "reporter")
	writePluginFile(t, stub, "#!/bin/sh\necho \"$@\" >> "+reports+"\n", 0o755)
	stageOpenCodePlugin(t, dir, stub)

	driveOpenCodePlugin(t, dir, openCodePromptDriver, reports, 5)
	got, err := os.ReadFile(reports)
	if err != nil {
		t.Fatalf("read the reports the plugin posted: %v", err)
	}
	want := strings.Join([]string{
		"report opencode --event session.status --status busy",
		"report opencode --event permission.asked",
		"report opencode --event question.asked",
		"report opencode --event question.replied",
		"report opencode --event session.idle",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("the plugin posted\n%s\nwant\n%s", got, want)
	}
}

// openCodePromptDriver asks two prompts of two sessions and answers them
// one at a time. opencode names a prompt with id when it asks and quotes it
// back as requestID in the answer, which is what the plugin pairs them by.
const openCodePromptDriver = `
import { AetherStatus } from "./plugin.mjs"

const client = { app: { log: async ({ body }) => console.log(JSON.stringify(body)) } }
const hooks = await AetherStatus({ client })
const events = [
  { type: "session.status", properties: { sessionID: "root", status: { type: "busy" } } },
  { type: "permission.asked", properties: { sessionID: "root", id: "per_1" } },
  { type: "question.asked", properties: { sessionID: "sub", id: "que_1" } },
  // The member answers the permission; the question is still on their screen.
  { type: "permission.replied", properties: { sessionID: "root", requestID: "per_1" } },
  // A provider call being retried in a session that never announced a turn.
  { type: "session.status", properties: { sessionID: "other", status: { type: "retry", attempt: 1 } } },
  { type: "question.replied", properties: { sessionID: "sub", requestID: "que_1" } },
  { type: "session.idle", properties: { sessionID: "root" } },
]
for (const event of events) await hooks.event({ event })
`

// TestOpenCodePluginReportsIdleWhenTheAnsweredTurnHasEnded is the case the
// held-back idle leaves behind: the turn that asked the question ends while
// the prompt is still on the member's screen, so its idle is suppressed and
// never comes again. Answering the last prompt has to park the run itself,
// or the finished turn stays on the card as running.
func TestOpenCodePluginReportsIdleWhenTheAnsweredTurnHasEnded(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports.log")
	stub := filepath.Join(dir, "reporter")
	writePluginFile(t, stub, "#!/bin/sh\necho \"$@\" >> "+reports+"\n", 0o755)
	stageOpenCodePlugin(t, dir, stub)

	driveOpenCodePlugin(t, dir, openCodeAnsweredAfterIdleDriver, reports, 3)
	got, err := os.ReadFile(reports)
	if err != nil {
		t.Fatalf("read the reports the plugin posted: %v", err)
	}
	want := strings.Join([]string{
		"report opencode --event session.status --status busy",
		"report opencode --event question.asked",
		"report opencode --event session.idle",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("the plugin posted\n%s\nwant\n%s", got, want)
	}
}

// openCodeAnsweredAfterIdleDriver ends the only turn there is while its
// question is unanswered. The answer is the run's last event, so the plugin
// has no later idle to fall back on.
const openCodeAnsweredAfterIdleDriver = `
import { AetherStatus } from "./plugin.mjs"

const client = { app: { log: async ({ body }) => console.log(JSON.stringify(body)) } }
const hooks = await AetherStatus({ client })
const events = [
  { type: "session.status", properties: { sessionID: "root", status: { type: "busy" } } },
  { type: "question.asked", properties: { sessionID: "root", id: "que_1" } },
  // The turn ends with the question still on the member's screen.
  { type: "session.idle", properties: { sessionID: "root" } },
  { type: "question.replied", properties: { sessionID: "root", requestID: "que_1" } },
]
for (const event of events) await hooks.event({ event })
`

// TestOpenCodePluginWarnsWhatTheReporterSaid drives the failure path: the
// reporter exits 0 and puts one line on stderr whatever goes wrong, so that
// line is the only trace a member has of a reporter that ran and could not
// reach the server. The plugin has to carry it verbatim into opencode's own
// log - the TUI owns the terminal - once however many turns fail.
func TestOpenCodePluginWarnsWhatTheReporterSaid(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports.log")
	stub := filepath.Join(dir, "reporter")
	writePluginFile(t, stub, "#!/bin/sh\necho \"$@\" >> "+reports+"\n"+
		"echo 'aether-server report opencode: dial /run/aether/coord.sock: connection refused' >&2\n", 0o755)
	stageOpenCodePlugin(t, dir, stub)

	out := driveOpenCodePlugin(t, dir, openCodeFailureDriver, reports, 2)
	const want = `{"service":"aether","level":"error","message":"status reporter: ` +
		`aether-server report opencode: dial /run/aether/coord.sock: connection refused"}`
	if got := strings.Count(out, want); got != 1 {
		t.Fatalf("the plugin logged %s, want %s exactly once", out, want)
	}
}

// openCodeFailureDriver ends two turns against a reporter that fails every
// time. The second post is what proves the warning does not repeat: it only
// starts once the first child has closed, which is after that child's
// stderr was delivered.
const openCodeFailureDriver = `
import { AetherStatus } from "./plugin.mjs"

const client = { app: { log: async ({ body }) => console.log(JSON.stringify(body)) } }
const hooks = await AetherStatus({ client })
for (const sessionID of ["first", "second"]) {
  await hooks.event({ event: { type: "session.idle", properties: { sessionID } } })
}
`

// openCodeDriverWait ends every scenario: the handler never waits for its
// own report, so the driver waits here for the count the test expects.
const openCodeDriverWait = `
const { readFileSync } = await import("node:fs")
const deadline = Date.now() + 10000
while (Date.now() < deadline) {
  let lines = ""
  try {
    lines = readFileSync(process.argv[2], "utf8")
  } catch {}
  if (lines.split("\n").length > Number(process.argv[3])) break
  await new Promise((r) => setTimeout(r, 50))
}
`

// driveOpenCodePlugin runs one scenario against the plugin staged in dir
// and returns what it printed, once want reports have reached reports.
func driveOpenCodePlugin(t *testing.T, dir, driver, reports string, want int) string {
	t.Helper()
	writePluginFile(t, filepath.Join(dir, "drive.mjs"), driver+openCodeDriverWait, 0o644)
	out, err := exec.Command(requireNode(t), filepath.Join(dir, "drive.mjs"), reports, strconv.Itoa(want)).CombinedOutput()
	if err != nil {
		t.Fatalf("drive the plugin: %v (%s)", err, out)
	}
	return string(out)
}

// requireNode is the node the plugin scenarios run under. They are the only
// check that the embedded plugin is valid JavaScript at all, so a machine
// without node loses that coverage; CI installs one.
func requireNode(t *testing.T) string {
	t.Helper()
	// The scenarios run the reporter as a shell script, which Windows has
	// no way to execute; the plugin itself only ever runs in a Linux run
	// container.
	if runtime.GOOS == "windows" {
		t.Skip("the opencode plugin scenario needs a POSIX shell for its stub reporter")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("the opencode plugin scenario needs node on PATH")
	}
	return node
}

// stageOpenCodePlugin writes the embedded plugin into dir as an importable
// module, with the staged server binary replaced by stub: the container
// path is the one thing a scenario cannot provide.
func stageOpenCodePlugin(t *testing.T, dir, stub string) {
	t.Helper()
	const binary = `"` + ReporterCommand + `"`
	if bytes.Count(OpenCodePlugin, []byte(binary)) != 1 {
		t.Fatalf("%s does not name %s exactly once; the container has nothing else to run", OpenCodePluginName, binary)
	}
	plugin := bytes.Replace(OpenCodePlugin, []byte(binary), []byte(strconv.Quote(stub)), 1)
	writePluginFile(t, filepath.Join(dir, "plugin.mjs"), string(plugin), 0o644)
}

// writePluginFile writes one file of the plugin scenario.
func writePluginFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFromPiEvent(t *testing.T) {
	cases := []struct {
		event  string
		tool   string
		want   Report
		mapped bool
	}{
		{event: "before_agent_start", want: Report{State: Working}, mapped: true},
		{event: "agent_start", want: Report{State: Working}, mapped: true},
		{event: "tool_execution_start", tool: "bash", want: Report{State: Working}, mapped: true},
		{event: "tool_execution_end", tool: "bash", want: Report{State: Working}, mapped: true},
		{event: "message_end", want: Report{State: Working}, mapped: true},
		{event: "tool_call", tool: "bash", want: Report{State: Working}, mapped: true},
		{event: "tool_call", tool: "ask", want: Report{State: Waiting, Reason: ReasonAnswer}, mapped: true},
		{event: "tool_call", tool: "AskUserQuestion", want: Report{State: Waiting, Reason: ReasonAnswer}, mapped: true},
		{event: "tool_approval_requested", tool: "bash",
			want: Report{State: Waiting, Reason: ReasonPermission}, mapped: true},
		{event: "tool_approval_resolved", tool: "bash", want: Report{State: Working}, mapped: true},
		{event: "agent_end", want: Report{State: Waiting, Reason: ReasonInput}, mapped: true},
		{event: "agent_settled", want: Report{State: Waiting, Reason: ReasonInput}, mapped: true},

		{event: "session_start"},
		{event: "agent_abort"},
		{event: ""},
		// The ask tools are named exactly; a tool whose name merely
		// contains one is an ordinary tool call.
		{event: "tool_call", tool: "asking", want: Report{State: Working}, mapped: true},
	}
	for _, tc := range cases {
		t.Run(tc.event+"/"+tc.tool, func(t *testing.T) {
			got, ok := FromPiEvent(tc.event, tc.tool)
			if ok != tc.mapped {
				t.Fatalf("FromPiEvent(%q, %q) mapped = %v, want %v", tc.event, tc.tool, ok, tc.mapped)
			}
			if got != tc.want {
				t.Errorf("FromPiEvent(%q, %q) = %+v, want %+v", tc.event, tc.tool, got, tc.want)
			}
		})
	}
}

// The extension is the pi and omp end of the same contract the settings
// document is for Claude Code: every event it subscribes to has to be one
// the mapping answers to, or the agent spawns the reporter for nothing.
func TestPiExtensionSubscribesToMappedEvents(t *testing.T) {
	source := string(PiExtension)
	subscriptions := regexp.MustCompile(`pi\.on\('([a-z_]+)'`).FindAllStringSubmatch(source, -1)
	if len(subscriptions) == 0 {
		t.Fatalf("%s subscribes to nothing", PiExtensionName)
	}
	for _, m := range subscriptions {
		if _, ok := FromPiEvent(m[1], ""); !ok {
			t.Errorf("%s subscribes to %s, which the mapping ignores", PiExtensionName, m[1])
		}
	}
	// The reporter is spawned by absolute path: nothing puts the staged
	// binary on the agent's PATH.
	if !strings.Contains(source, ReporterCommand) {
		t.Errorf("%s does not spawn %s", PiExtensionName, ReporterCommand)
	}
	if !strings.Contains(string(ClaudeSettings), ReporterCommand) {
		t.Errorf("%s does not run %s", ClaudeSettingsName, ReporterCommand)
	}
	if !strings.Contains(CodexNotifySetting, ReporterCommand) {
		t.Errorf("the codex notify setting does not run %s: %s", ReporterCommand, CodexNotifySetting)
	}
}
