package agentstatus

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestClaudeCorrelatedInput(t *testing.T) {
	for _, tc := range []struct {
		name, payload, operation, kind, id, session string
		state                                       State
	}{
		{"question", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"root","tool_use_id":"q","tool_input":{"questions":["private"]}}`, "open", "question", "q", "root", ""},
		{"answer", `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","session_id":"root","tool_use_id":"q","tool_response":"secret"}`, "close", "question", "q", "root", Working},
		{"failure", `{"hook_event_name":"PostToolUseFailure","tool_name":"AskUserQuestion","session_id":"root","tool_use_id":"q"}`, "close", "question", "q", "root", Working},
		{"elicitation", `{"hook_event_name":"Elicitation","session_id":"root","elicitation_id":"f","url":"https://private","message":"credential prompt"}`, "open", "form", "f", "root", ""},
		{"dismissal", `{"hook_event_name":"ElicitationResult","session_id":"root","elicitation_id":"f","action":"cancel","content":{"token":"secret"}}`, "close", "form", "f", "root", ""},
		{"child question", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"root","agent_id":"child","tool_use_id":"q"}`, "open", "question", "q", `["root","child"]`, ""},
		{"interrupted question", `{"hook_event_name":"PostToolUseFailure","tool_name":"AskUserQuestion","session_id":"root","tool_use_id":"q","is_interrupt":true}`, "close", "question", "q", "root", ""},
		{"child termination", `{"hook_event_name":"SessionEnd","session_id":"root","agent_id":"child"}`, "clear", "", "", `["root","child"]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromClaudeHook([]byte(tc.payload))
			want := Report{State: tc.state, SessionID: "root", InputUpdates: []domain.RunInputUpdate{{
				Operation: tc.operation, Kind: tc.kind, ID: tc.id, SessionID: tc.session,
			}}}
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("report = %+v, mapped=%v; want %+v", got, ok, want)
			}
		})
	}
}

func TestClaudeDoesNotInventInput(t *testing.T) {
	for _, payload := range []string{
		`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`,
		`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`,
		`{"hook_event_name":"PermissionRequest","session_id":"root","tool_name":"Bash"}`,
		`{"hook_event_name":"Elicitation","session_id":"root"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"root"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"q"}`,
		`{"hook_event_name":"Stop","agent_id":"child"}`,
		`{"hook_event_name":"SubagentStop","session_id":"root","agent_id":"child"}`,
		`not JSON`,
	} {
		if got, ok := FromClaudeHook([]byte(payload)); ok || len(got.InputUpdates) != 0 {
			t.Errorf("uncorrelated callback %s produced %+v", payload, got)
		}
	}
	for _, event := range []string{"Stop", "StopFailure"} {
		got, ok := FromClaudeHook([]byte(`{"hook_event_name":"` + event + `","session_id":"root"}`))
		if !ok || got.State != Idle || len(got.InputUpdates) != 0 {
			t.Errorf("%s must only end execution: %+v", event, got)
		}
	}
	got, ok := FromClaudeHook([]byte(`{"hook_event_name":"Stop","background_tasks":[{"id":"child","description":"private"}]}`))
	if !ok || got.State != Working || len(got.InputUpdates) != 0 {
		t.Fatalf("background execution was parked: %+v", got)
	}
	got, ok = FromClaudeHook([]byte(`{"hook_event_name":"SessionEnd","session_id":"root"}`))
	if !ok || len(got.InputUpdates) != 1 || got.InputUpdates[0].Operation != "replace" || got.InputUpdates[0].Requests == nil {
		t.Fatalf("terminated session did not clear its request snapshot: %+v", got)
	}
}

func TestCodexNotifyOnlyReportsIdle(t *testing.T) {
	got, ok := FromCodexNotify(`{"type":"agent-turn-complete","thread-id":"0199a1b2-c3d4","turn-id":"7","last-assistant-message":"What is your password?"}`)
	if !ok || got.State != Idle || len(got.InputUpdates) != 0 || got.SessionID != "0199a1b2-c3d4" {
		t.Fatalf("turn completion = %+v", got)
	}
	for _, payload := range []string{`{"type":"agent-turn-started"}`, `{}`, `invalid`} {
		if _, mapped := FromCodexNotify(payload); mapped {
			t.Errorf("unexpected mapping for %s", payload)
		}
	}
}

func TestParseState(t *testing.T) {
	for _, state := range []State{Working, Idle} {
		if got, ok := ParseState(string(state)); !ok || got != state {
			t.Errorf("ParseState(%q) = %q, %v", state, got, ok)
		}
	}
	for _, bad := range []string{"", "Working", "idle", "running"} {
		if _, ok := ParseState(bad); ok {
			t.Errorf("accepted invalid execution state %q", bad)
		}
	}
}

func runExtension(t *testing.T, scenario string) ([]Report, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native extension needs a POSIX reporter")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun is not installed: %v", err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "reports")
	reporter := filepath.Join(dir, "reporter")
	body := "#!/bin/sh\nprintf '%s\\n' \"$4\" >> " + log + "\n"
	if scenario == "unreachable" {
		body += "echo 'coordination socket unavailable' >&2\n"
	}
	mustWriteFile(t, reporter, body, 0o700)
	staged := strings.Replace(string(PiExtension), "const REPORTER = '"+ReporterCommand+"'", "const REPORTER = '"+reporter+"'", 1)
	mustWriteFile(t, filepath.Join(dir, "status.ts"), staged, 0o600)
	mustWriteFile(t, filepath.Join(dir, "status-copy.ts"), staged, 0o600)
	driver, err := os.ReadFile(filepath.Join("testdata", "drive.ts"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "drive.ts"), string(driver), 0o600)
	cmd := exec.Command(bun, "run", "drive.ts", scenario, log)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extension scenario %s: %v\n%s", scenario, err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var reports []Report
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var report Report
		if err := json.Unmarshal([]byte(line), &report); err != nil {
			t.Fatalf("decode native report: %v (%s)", err, line)
		}
		// These assertions cover execution/request transitions, not callback
		// counts. Identical Working reports are liveness heartbeats, exercised
		// through the production stall detector by the scheduler regression.
		if len(reports) != 0 && reflect.DeepEqual(reports[len(reports)-1], report) {
			continue
		}
		reports = append(reports, report)
	}
	return reports, string(out)
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionExecutionLifetimes(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		states   []State
	}{
		{"turn", []State{Working, Idle}},
		{"late-message", []State{Idle}},
		{"settled", []State{Idle}},
		{"idle-later", []State{Idle}},
		{"foreign-owner", nil},
		{"will-continue", nil},
		{"double", []State{Working, Idle}},
		{"omp-background", []State{Working, Idle}},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			reports, _ := runExtension(t, tc.scenario)
			var states []State
			for _, report := range reports {
				states = append(states, report.State)
				if len(report.InputUpdates[0].Requests) != 0 {
					t.Fatalf("execution event fabricated input: %+v", report)
				}
			}
			if !reflect.DeepEqual(states, tc.states) {
				t.Fatalf("execution = %v, want %v", states, tc.states)
			}
			if tc.scenario == "turn" && reports[0].SessionID != "root" {
				t.Fatalf("a turn's report without its session: %+v", reports[0])
			}
		})
	}
}

func TestExtensionCorrelatedRequests(t *testing.T) {
	reports, _ := runExtension(t, "requests")
	want := [][]domain.RunInputRequest{
		{},
		{{ID: "same", SessionID: "root", Kind: "question"}},
		{{ID: "same", SessionID: "root", Kind: "question"}, {ID: "same", SessionID: "child", Kind: "permission"}},
		{{ID: "same", SessionID: "child", Kind: "permission"}},
		{},
		{},
	}
	if len(reports) != len(want) {
		t.Fatalf("reports = %+v", reports)
	}
	for i, requests := range want {
		if !reflect.DeepEqual(reports[i].InputUpdates[0].Requests, requests) {
			t.Errorf("snapshot %d = %+v, want %+v", i, reports[i], requests)
		}
		state := Working
		if i == len(want)-1 {
			state = Idle
		}
		if reports[i].State != state {
			t.Errorf("request changed execution: %+v", reports[i])
		}
	}
}

func TestExtensionPromptSurvivesSettlement(t *testing.T) {
	reports, _ := runExtension(t, "ui-prompt")
	if len(reports) != 4 {
		t.Fatalf("reports = %+v", reports)
	}
	open := reports[1].InputUpdates[0].Requests
	if len(open) != 1 || open[0].Kind != "extension_ui" || open[0].SessionID != "root" || open[0].ID == "" {
		t.Fatalf("prompt identity = %+v", open)
	}
	if reports[2].State != Idle || !reflect.DeepEqual(reports[2].InputUpdates[0].Requests, open) {
		t.Fatalf("settlement cleared an unresolved prompt: %+v", reports[2])
	}
	if reports[3].State != Idle || len(reports[3].InputUpdates[0].Requests) != 0 {
		t.Fatalf("dismissal fabricated execution or retained prompt: %+v", reports[3])
	}
}

func TestExtensionWarnsReporterFailureOnce(t *testing.T) {
	_, out := runExtension(t, "unreachable")
	if strings.Count(out, "coordination socket unavailable") != 1 {
		t.Fatalf("reporter diagnostics = %q", out)
	}
}
