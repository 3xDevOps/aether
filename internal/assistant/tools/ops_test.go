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
		p, _ := protocol.DecodeParams[protocol.MissionShowParams](params)
		switch p.QuestionID {
		case "q-1":
			return json.RawMessage(`{"mission":{"id":"mis-1","objective":"Ship the billing page","accountable_human_id":"m2"},
				"questions":[{"id":"q-1","mission_id":"mis-1","body":"Which branch?"}]}`), nil
		case "q-2":
			return json.RawMessage(`{"mission":{"id":"mis-2","objective":"Port the API","accountable_human_id":"m1"},
				"questions":[{"id":"q-2","mission_id":"mis-2","body":"Which version?","answer":"v2","answered_at":"2026-10-26T10:00:00Z"}]}`), nil
		}
		if p.MissionID == "mis-1" {
			return json.RawMessage(`{"mission":{"id":"mis-1","objective":"Ship the billing page","accountable_human_id":"m2","current_integrator_run_id":"run-9"}}`), nil
		}
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "mission question not found"}
	case protocol.MethodIntegrationShow:
		return json.RawMessage(`{"candidate":{"candidate_id":"cand-1","mission_id":"mis-1","target_ref":"refs/heads/main"}}`), nil
	case protocol.MethodMemoryShow:
		return json.RawMessage(`{"item":{"id":"mem-1","kind":"gotcha","title":"CI needs Docker"}}`), nil
	}
	return c.fakeCaller.Call(ctx, method, params)
}

// Every confirmed method has its own summary naming what it acts on, and
// an action on another member's run says whose it is.
func TestEveryConfirmedMethodHasItsOwnSummary(t *testing.T) {
	params := map[string]any{
		protocol.MethodRunLaunch:             protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "fix it"},
		protocol.MethodRunInject:             protocol.RunInjectParams{RunID: "run-1", Message: "hi", Attachments: []string{"shot.png"}, IdempotencyKey: "k"},
		protocol.MethodRunKill:               protocol.RunIDParams{RunID: "run-1"},
		protocol.MethodEnvSave:               nil,
		protocol.MethodRunInputRespond:       protocol.RunInputRespondParams{RunID: "run-1", RequestID: "req-1", OptionID: "once"},
		protocol.MethodMissionQuestionAnswer: protocol.MissionQuestionAnswerParams{QuestionID: "q-1", Answer: "main", IdempotencyKey: "k"},
		protocol.MethodRunRoomPost: protocol.RunRoomPostParams{WorkspaceID: "ws-1", RunID: "run-1", Kind: protocol.RoomMessageReply, Body: "yes",
			Anchor: &protocol.RoomMessageAnchor{Kind: "file", Path: "main.go", StartLine: 3, EndLine: 9}, IdempotencyKey: "k"},
		protocol.MethodRunRelaunch:          protocol.RunIDParams{RunID: "run-1"},
		protocol.MethodRunModeSwitch:        protocol.RunModeSwitchParams{RunID: "run-1", Mode: "acp"},
		protocol.MethodRunProtect:           protocol.RunProtectParams{RunID: "run-1", Protected: true},
		protocol.MethodRunArchive:           protocol.RunArchiveParams{RunID: "run-1"},
		protocol.MethodRunHandoff:           protocol.RunHandoffParams{RunID: "run-1", ToMemberID: "m1"},
		protocol.MethodMissionWorkerRelease: protocol.MissionWorkerReleaseParams{RunID: "run-1", ExpectedTakeoverGeneration: 2},
		protocol.MethodMissionArchive:       protocol.MissionIDParams{MissionID: "mis-1"},
		protocol.MethodMissionUnarchive:     protocol.MissionIDParams{MissionID: "mis-1"},
		protocol.MethodMissionReplaceIntegrator: protocol.MissionReplaceIntegratorParams{MissionID: "mis-1", ExpectedGeneration: 1, IdempotencyKey: "k",
			Integrator: protocol.MissionIntegrator{AccountMemberID: "m2", Harness: "codex", Mode: "acp"}},
		protocol.MethodIntegrationDecide:  protocol.IntegrationDecideParams{WorkspaceID: "ws-1", CandidateID: "cand-1", RequestID: "req-1", RequestVersion: 1, Approve: true},
		protocol.MethodIntegrationDeliver: protocol.IntegrationDeliverParams{WorkspaceID: "ws-1", CandidateID: "cand-1", RequestID: "req-1", RequestVersion: 2},
		protocol.MethodRunPRCreate: protocol.RunPRCreateParams{RunID: "run-1", Expected: protocol.RunGitExpected{Branch: "fix-login", Head: "0123456789abcdef"},
			Title: "Fix login", Draft: true},
		protocol.MethodRunPRMerge: protocol.RunPRMergeParams{RunID: "run-1", Method: "squash", ExpectedHead: "fedcba9876543210", Repository: "acme/shop", Number: 7,
			ExpectedBase: "main"},
		protocol.MethodMemoryAdd:   protocol.MemoryAddParams{WorkspaceID: "ws-1", Kind: "gotcha", Text: "CI needs Docker.", Supersedes: "mem-0"},
		protocol.MethodMemoryPin:   protocol.MemoryPinParams{WorkspaceID: "ws-1", ID: "mem-1", Scope: protocol.MemoryScopeMember},
		protocol.MethodMemoryUnpin: protocol.MemoryUnpinParams{WorkspaceID: "ws-1", ID: "mem-1"},
	}
	want := map[string]string{
		protocol.MethodRunLaunch:                `Launch claude (default mode) in shop: "fix it"`,
		protocol.MethodRunInject:                `Send to run run-1 ("Fix login"), owned by Grace: "hi", attaching "shot.png"`,
		protocol.MethodRunKill:                  `Stop run run-1 ("Fix login"), owned by Grace`,
		protocol.MethodEnvSave:                  `Save your environment as a new image; new runs and terminals start from it`,
		protocol.MethodRunInputRespond:          `Allow once run run-1's request ("Fix login"), owned by Grace: "Run git push", choosing "Allow once"`,
		protocol.MethodMissionQuestionAnswer:    `Answer swarm mis-1 ("Ship the billing page"), owned by Grace, which asked "Which branch?": "main"`,
		protocol.MethodRunRoomPost:              `Reply on run run-1 ("Fix login"), owned by Grace: "yes", anchored at "main.go:3-9"`,
		protocol.MethodRunRelaunch:              `Relaunch run run-1 ("Fix login"), owned by Grace`,
		protocol.MethodRunModeSwitch:            `Switch run run-1 ("Fix login") to Enhanced mode, owned by Grace`,
		protocol.MethodRunProtect:               `Protect run run-1 ("Fix login") from cleanup, owned by Grace`,
		protocol.MethodRunArchive:               `Restore run run-1 ("Fix login") from the archive, owned by Grace`,
		protocol.MethodRunHandoff:               `Hand run run-1 ("Fix login"), owned by Grace, to Ada (m1), who then owns it`,
		protocol.MethodMissionWorkerRelease:     `Release your control of worker run run-1 ("Fix login"), owned by Grace`,
		protocol.MethodMissionArchive:           `Archive, closing its completed runs, swarm mis-1 ("Ship the billing page"), owned by Grace`,
		protocol.MethodMissionUnarchive:         `Unarchive swarm mis-1 ("Ship the billing page"), owned by Grace`,
		protocol.MethodMissionReplaceIntegrator: `Replace integrator run run-9 of swarm mis-1 ("Ship the billing page"), owned by Grace, with codex (acp) on Grace's (m2) account`,
		protocol.MethodIntegrationDecide:        `Approve delivering candidate cand-1, moving refs/heads/main, for swarm mis-1 ("Ship the billing page"), owned by Grace`,
		protocol.MethodIntegrationDeliver:       `Deliver candidate cand-1, moving refs/heads/main, for swarm mis-1 ("Ship the billing page"), owned by Grace`,
		protocol.MethodRunPRCreate:              `Open a draft pull request from run run-1 ("Fix login"), owned by Grace, branch fix-login at 0123456, into the workspace's base branch: "Fix login"`,
		protocol.MethodRunPRMerge:               `Merge (squash) acme/shop#7 of run run-1 ("Fix login"), owned by Grace, at fedcba9 into main`,
		protocol.MethodMemoryAdd:                `Remember a gotcha in shop for everyone: "CI needs Docker.", replacing note mem-0 ("CI needs Docker")`,
		protocol.MethodMemoryPin:                `Pin, into new runs' briefs, note mem-1 (gotcha) for you only: "CI needs Docker"`,
		protocol.MethodMemoryUnpin:              `Unpin note mem-1 (gotcha) for everyone: "CI needs Docker"`,
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

	// A swarm question no longer pending is still named by its swarm, and
	// one that never existed fails before any card.
	raw, _ = json.Marshal(protocol.MissionQuestionAnswerParams{QuestionID: "q-2", Answer: "v2", IdempotencyKey: "k"})
	if _, perr := s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodMissionQuestionAnswer, Params: raw}); perr != nil {
		t.Fatal(perr)
	}
	if want := `Answer swarm mis-2 ("Port the API") again, which asked "Which version?" and no longer waits: "v2"`; got.Summary != want || got.OtherMember {
		t.Fatalf("an answered question's summary = %q (other member %t), want %q", got.Summary, got.OtherMember, want)
	}
	got = protocol.AssistantConfirmation{}
	raw, _ = json.Marshal(protocol.MissionQuestionAnswerParams{QuestionID: "q-x", Answer: "v2", IdempotencyKey: "k"})
	if _, perr := s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodMissionQuestionAnswer, Params: raw}); perr == nil || perr.Code != protocol.CodeNotFound || got.Summary != "" {
		t.Fatalf("answering an unknown question = %v, card %q; want not found before any card", perr, got.Summary)
	}
}
