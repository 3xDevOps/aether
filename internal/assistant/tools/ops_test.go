package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/ops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Every method an op the assistant offers calls has a tier here, and the
// op's tier matches its methods': a read op never asks for approval, a
// reversible one changes something without asking, and a confirmed one
// asks.
func TestOpsMethodsAreTiered(t *testing.T) {
	for _, o := range ops.Ops {
		if o.Tier == ops.TierNone {
			continue
		}
		count := map[string]int{}
		for _, m := range o.Methods {
			tier, ok := tiers[m]
			if !ok {
				t.Errorf("%s calls %s, which has no tier", o.Name, m)
			}
			count[tier]++
		}
		switch o.Tier {
		case ops.TierRead:
			if count[tierRead] != len(o.Methods) {
				t.Errorf("%s is read but calls %v", o.Name, count)
			}
		case ops.TierReversible:
			if count[tierConfirmed] > 0 || count[tierReversible] == 0 {
				t.Errorf("%s is reversible but calls %v", o.Name, count)
			}
		case ops.TierConfirmed:
			if count[tierConfirmed] == 0 {
				t.Errorf("%s is confirmed but calls %v", o.Name, count)
			}
		}
	}
}

type attentionCaller struct{ fakeCaller }

func (c *attentionCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	switch method {
	case protocol.MethodAttentionList:
		return json.RawMessage(`{"items":[
			{"kind":"permission","run_id":"run-1","workspace_id":"ws-1","request":{"id":"req-1","title":"Run git push","options":[{"id":"once","label":"Allow once","kind":"allow_once"},{"id":"no","label":"Reject","kind":"reject_once"}]}},
			{"kind":"swarm-question","run_id":"run-1","mission_id":"mis-1","workspace_id":"ws-1","message":{"id":"q-1","body":"Which branch?"}}]}`), nil
	case protocol.MethodMissionShow:
		return json.RawMessage(`{"mission":{"id":"mis-1","objective":"Ship the billing page"}}`), nil
	}
	return c.fakeCaller.Call(ctx, method, params)
}

// Every confirmed method has its own summary naming what it acts on, and
// an action on another member's run says whose it is.
func TestEveryConfirmedMethodHasItsOwnSummary(t *testing.T) {
	params := map[string]any{
		protocol.MethodRunLaunch:             protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "fix it"},
		protocol.MethodRunInject:             protocol.RunInjectParams{RunID: "run-1", Message: "hi", IdempotencyKey: "k"},
		protocol.MethodRunKill:               protocol.RunIDParams{RunID: "run-1"},
		protocol.MethodEnvSave:               nil,
		protocol.MethodRunInputRespond:       protocol.RunInputRespondParams{RunID: "run-1", RequestID: "req-1", OptionID: "once"},
		protocol.MethodMissionQuestionAnswer: protocol.MissionQuestionAnswerParams{QuestionID: "q-1", Answer: "main", IdempotencyKey: "k"},
		protocol.MethodRunRoomPost:           protocol.RunRoomPostParams{WorkspaceID: "ws-1", RunID: "run-1", Kind: protocol.RoomMessageReply, Body: "yes", IdempotencyKey: "k"},
	}
	want := map[string]string{
		protocol.MethodRunLaunch:             `Launch claude (default mode) in shop: "fix it"`,
		protocol.MethodRunInject:             `Send to run run-1 ("Fix login"), owned by Grace: "hi"`,
		protocol.MethodRunKill:               `Stop run run-1 ("Fix login"), owned by Grace`,
		protocol.MethodEnvSave:               `Save your environment as a new image; new runs and terminals start from it`,
		protocol.MethodRunInputRespond:       `Allow run run-1's request ("Fix login"), owned by Grace: "Run git push", choosing "Allow once"`,
		protocol.MethodMissionQuestionAnswer: `Answer swarm mis-1 ("Ship the billing page"), owned by Grace, which asked "Which branch?": "main"`,
		protocol.MethodRunRoomPost:           `Reply on run run-1 ("Fix login"), owned by Grace: "yes"`,
	}
	var got protocol.AssistantConfirmation
	s := &Server{Caller: &attentionCaller{}, Member: "m1", Confirm: func(_ context.Context, c protocol.AssistantConfirmation) error {
		got = c
		return nil
	}}
	for method, tier := range tiers {
		if tier != tierConfirmed {
			continue
		}
		p, ok := params[method]
		if !ok {
			t.Errorf("%s is confirmed but this test has no params for it", method)
			continue
		}
		raw, _ := json.Marshal(p)
		got = protocol.AssistantConfirmation{}
		if _, perr := s.dispatch(t.Context(), protocol.Request{Method: method, Params: raw}); perr != nil {
			t.Errorf("%s: %v", method, perr)
			continue
		}
		if got.Summary != want[method] || got.OtherMember != strings.Contains(want[method], "owned by") {
			t.Errorf("%s summary = %q (other member %t), want %q", method, got.Summary, got.OtherMember, want[method])
		}
	}

	raw, _ := json.Marshal(protocol.RunInputRespondParams{RunID: "run-1", RequestID: "gone", OptionID: "once"})
	if _, perr := s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodRunInputRespond, Params: raw}); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("answering a request that is not pending = %v, want not found before any card", perr)
	}
}
