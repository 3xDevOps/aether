//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationAssistantAutoApprove drives auto-approval through the wired
// server: the tool socket cannot turn it on, the member can, a confirmed tool
// then runs without a card, is recorded in the transcript and its run is
// still followed, and turning it off brings the card back.
func TestIntegrationAssistantAutoApprove(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	ws := seedAssistantWorkspace(t, ctx, env, "auto")
	sub, err := env.srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	if st := env.status(); st.AutoApprove {
		t.Fatalf("auto-approval of a new member = %+v, want off", st)
	}
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	frames := env.watch(ctx, 0)
	conn, err := net.Dial("unix", filepath.Join(env.data, "assistant", string(env.member.ID), "sock", "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	tool := func(method string, params any) protocol.Response {
		t.Helper()
		raw, _ := json.Marshal(params)
		req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatalf("tool %s: %v", method, err)
		}
		var resp protocol.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	on := true
	if resp := tool(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{AutoApprove: &on}); resp.Error == nil || resp.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("assistant.settings through the tool socket = %+v, want method not found", resp)
	}
	if st := env.status(); st.AutoApprove {
		t.Fatal("the tool socket turned auto-approval on")
	}

	var settings protocol.AssistantSettings
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{AutoApprove: &on}, &settings); perr != nil || !settings.AutoApprove {
		t.Fatalf("assistant.settings on = %+v, %v", settings, perr)
	}
	frames.until(t, "the status with auto-approval on", func(f protocol.AssistantFrame) bool { return f.Status != nil && f.Status.AutoApprove })

	const task = "auto-approved run"
	release := make(chan struct{})
	env.rt.script(task, func(c *e2eContainer) {
		c.output("working\r\n")
		<-release
		c.exitNow(0)
	})
	resp := tool(protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchHeadless)})
	if resp.Error != nil {
		t.Fatalf("auto-approved run.launch = %+v", resp.Error)
	}
	if st := env.status(); len(st.Confirmations) != 0 {
		t.Fatalf("cards after an auto-approved launch = %+v", st.Confirmations)
	}
	var launched protocol.RunResult
	if err = json.Unmarshal(resp.Result, &launched); err != nil {
		t.Fatal(err)
	}
	runID := domain.RunID(launched.Run.ID)
	if launched.Run.MemberID != string(env.member.ID) {
		t.Fatalf("auto-approved run belongs to %q, want the member", launched.Run.MemberID)
	}
	if follows, err := env.srv.Store().ListAssistantFollows(ctx, runID); err != nil || len(follows) != 1 {
		t.Fatalf("follows after an auto-approved launch = %+v, %v", follows, err)
	}
	waitRunStatus(t, sub, &seen, string(runID), domain.RunRunning)
	close(release)
	waitEvent(t, sub, &seen, "the finished notice", func(e events.Event) bool {
		p, ok := e.Payload.(events.AssistantNoticePayload)
		return ok && e.RunID == runID && p.Kind == events.AssistantNoticeFinished
	})

	off := false
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{AutoApprove: &off}, &settings); perr != nil || settings.AutoApprove {
		t.Fatalf("assistant.settings off = %+v, %v", settings, perr)
	}
	if perr := env.call(protocol.MethodAssistantSettings, protocol.AssistantSettingsParams{}, &settings); perr != nil || settings.AutoApprove {
		t.Fatalf("assistant.settings read = %+v, %v", settings, perr)
	}
	done := make(chan protocol.Response, 1)
	go func() { done <- tool(protocol.MethodEnvSave, struct{}{}) }()
	pending := frames.until(t, "a card once auto-approval is off", func(f protocol.AssistantFrame) bool {
		return f.Status != nil && len(f.Status.Confirmations) == 1
	})
	if perr := env.call(protocol.MethodAssistantConfirm, protocol.AssistantConfirmParams{ConfirmationID: pending.Status.Confirmations[0].ID}, nil); perr != nil {
		t.Fatalf("assistant.confirm: %v", perr)
	}
	if resp := <-done; resp.Error == nil || !strings.Contains(resp.Error.Message, "declined") {
		t.Fatalf("declined env.save = %+v", resp)
	}

	var history protocol.RunACPHistoryResult
	if perr := env.call(protocol.MethodAssistantHistory, protocol.AssistantHistoryParams{}, &history); perr != nil {
		t.Fatal(perr)
	}
	var notices []string
	for _, f := range history.Frames {
		if strings.Contains(string(f.Item), `"kind":"notice"`) {
			notices = append(notices, string(f.Item))
		}
	}
	all := strings.Join(notices, "\n")
	for _, want := range []string{
		`"title":"Auto-approval turned on"`,
		`"title":"Auto-approved: Launch fake (headless) in auto: \"` + task + `\""`,
		`run.launch passed without a card: auto-approval is on.`,
		`"title":"Auto-approval turned off"`,
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("transcript notices lack %s:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Approved: Save your environment") {
		t.Fatalf("a declined card was recorded as approved:\n%s", all)
	}
}
