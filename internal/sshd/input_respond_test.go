package sshd

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func TestRunInputRespondAnswersWithoutALease(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	_, vic := addMember(t, e, "Vic", domain.RoleViewer, false)
	respond := func(member domain.MemberID) (string, *protocol.Error) {
		t.Helper()
		raw, perr := callJSON(t, e, member, protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: string(run.ID), RequestID: "r1", OptionID: "allow"})
		if perr != nil {
			return "", perr
		}
		var result protocol.RunInputRespondResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result.Status, nil
	}

	if status, perr := respond(e.member.ID); perr != nil || status != protocol.RunInputRespondAnswered {
		t.Fatalf("respond with nobody in control = %q, %v", status, perr)
	}
	if !slices.Contains(e.runs.Calls(), "acp.answer:"+string(run.ID)+":r1:allow:map[]") {
		t.Fatalf("calls = %v, want the answer", e.runs.Calls())
	}
	if _, perr := respond(vic.ID); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("respond from a viewer = %v, want CodeDenied", perr)
	}

	writer, _, err := e.srv.Local(bob.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "bob-tab"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	_, perr := respond(e.member.ID)
	var data struct {
		Reason   string `json:"reason"`
		MemberID string `json:"member_id"`
	}
	if perr == nil || perr.Code != protocol.CodeConflict || json.Unmarshal(perr.Data, &data) != nil ||
		data.Reason != protocol.ErrorReasonControlHeld || data.MemberID != string(bob.ID) {
		t.Fatalf("respond while Bob controls the run = %+v", perr)
	}
	if status, perr := respond(bob.ID); perr != nil || status != protocol.RunInputRespondAnswered {
		t.Fatalf("respond from the controller's member = %q, %v", status, perr)
	}

	e.runs.setErr(acphost.ErrAlreadyAnswered)
	if status, perr := respond(bob.ID); perr != nil || status != protocol.RunInputRespondAlreadyAnswered {
		t.Fatalf("respond to an answered request = %q, %v", status, perr)
	}
	e.runs.setErr(acphost.ErrUnknownRequest)
	if _, perr := respond(bob.ID); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("respond to an unknown request = %v, want CodeNotFound", perr)
	}
}
