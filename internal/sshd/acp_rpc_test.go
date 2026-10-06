package sshd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func callJSON(t *testing.T, e *testEnv, member domain.MemberID, method string, params any) (json.RawMessage, *protocol.Error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return e.srv.Local(member).Call(t.Context(), method, raw)
}

func TestACPInputNeedsTheControlLease(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	answer := func(member domain.MemberID, lease protocol.ACPLease) *protocol.Error {
		_, perr := callJSON(t, e, member, protocol.MethodRunInputAnswer, protocol.RunInputAnswerParams{
			RunID: string(run.ID), RequestID: "r1", OptionID: "allow", ACPLease: lease,
		})
		return perr
	}
	if perr := answer(e.member.ID, protocol.ACPLease{}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("answer without a lease: %v", perr)
	}

	writer, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	lease := protocol.ACPLease{ControlSessionID: "tab-1", ControlGeneration: ack.ControlGeneration}
	if perr := answer(e.member.ID, protocol.ACPLease{ControlSessionID: "tab-2", ControlGeneration: ack.ControlGeneration}); perr == nil || perr.Code != protocol.CodeConflict {
		t.Fatalf("answer from a session without the lease: %v", perr)
	}
	_, viewer := addMember(t, e, "Vic", domain.RoleViewer, false)
	if perr := answer(viewer.ID, lease); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("answer from a viewer: %v", perr)
	}
	if perr := answer(e.member.ID, lease); perr != nil {
		t.Fatalf("answer from the lease holder: %v", perr)
	}
	if _, perr := callJSON(t, e, e.member.ID, protocol.MethodRunACPCancel, protocol.RunACPCancelParams{RunID: string(run.ID), ACPLease: lease}); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := callJSON(t, e, e.member.ID, protocol.MethodRunACPSetOption, map[string]any{
		"run_id": run.ID, "option_id": "mode", "value": 3, "control_session_id": "tab-1", "control_generation": ack.ControlGeneration,
	}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("set_option with a number: %v", perr)
	}
	if _, perr := callJSON(t, e, e.member.ID, protocol.MethodRunACPSetOption, protocol.RunACPSetOptionParams{
		RunID: string(run.ID), OptionID: "mode", Value: json.RawMessage(`"plan"`), ACPLease: lease,
	}); perr != nil {
		t.Fatal(perr)
	}
	want := []string{
		fmt.Sprintf("acp.answer:%s:r1:allow", run.ID),
		fmt.Sprintf("acp.cancel:%s", run.ID),
		fmt.Sprintf("acp.set_option:%s:mode:plan", run.ID),
	}
	e.runs.mu.Lock()
	calls := slices.Clone(e.runs.calls)
	e.runs.mu.Unlock()
	for _, w := range want {
		if !slices.Contains(calls, w) {
			t.Fatalf("calls %v, want %s", calls, w)
		}
	}
}

func TestACPHistoryAndItem(t *testing.T) {
	e, run := newACPEnv(t)
	big := acphost.Item{Seq: 2, Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "t1", Output: strings.Repeat("x", 64<<10)}}
	e.runs.acpStream = scheduler.ACPStream{Replay: []acphost.Item{{Seq: 1, Kind: acphost.KindTurnStart}, big}}
	raw, perr := callJSON(t, e, e.member.ID, protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: string(run.ID)})
	if perr != nil {
		t.Fatal(perr)
	}
	var page protocol.RunACPHistoryResult
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Frames) != 2 || page.Frames[1].Seq != 2 || !page.Frames[1].Truncated {
		t.Fatalf("history %+v", page)
	}
	raw, perr = callJSON(t, e, e.member.ID, protocol.MethodRunACPItem, protocol.RunACPItemParams{RunID: string(run.ID), Seq: 2})
	if perr != nil {
		t.Fatal(perr)
	}
	var item protocol.RunACPItemResult
	if err := json.Unmarshal(raw, &item); err != nil || !strings.Contains(string(item.Item), strings.Repeat("x", 64<<10)) {
		t.Fatalf("full item missing its output (%v)", err)
	}
	if _, perr = callJSON(t, e, e.member.ID, protocol.MethodRunACPItem, protocol.RunACPItemParams{RunID: string(run.ID), Seq: 9}); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("missing item: %v", perr)
	}
	if _, perr = callJSON(t, e, e.member.ID, protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: string(e.run.ID)}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("history of a terminal run: %v", perr)
	}
}

func TestACPAlreadyAnsweredIsTyped(t *testing.T) {
	perr := acpError(fmt.Errorf("wrapped: %w", acphost.ErrAlreadyAnswered))
	var data struct {
		Reason string `json:"reason"`
	}
	if perr == nil || perr.Code != protocol.CodeConflict || json.Unmarshal(perr.Data, &data) != nil || data.Reason != protocol.ErrorReasonAlreadyAnswered {
		t.Fatalf("already answered: %+v", perr)
	}
	if perr := acpError(scheduler.ErrACPNotRunning); perr.Code != protocol.CodeUnavailable {
		t.Fatalf("not running: %+v", perr)
	}
	if perr := acpError(errors.Join(scheduler.ErrACPItemNotFound)); perr.Code != protocol.CodeNotFound {
		t.Fatalf("missing item: %+v", perr)
	}
}
