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
		t.Fatalf("respond while Bob controls the session = %+v", perr)
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

func TestSudoRequestIsTheOwnersOrAnAdminsToAllow(t *testing.T) {
	e := newTestEnv(t, nil)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	_, admin := addMember(t, e, "Root", domain.RoleAdmin, false)
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "install jq", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	sudo := func(id string) domain.RunInputRequest {
		return domain.RunInputRequest{ID: id, SessionID: domain.SudoSessionID, Kind: "permission", Title: "Run as root: apt-get install -y jq"}
	}
	e.runs.mu.Lock()
	e.runs.sudo = map[domain.RunID][]domain.RunInputRequest{run.ID: {sudo("sudo-a"), sudo("sudo-b")}}
	e.runs.mu.Unlock()

	options := []protocol.AttentionOption{{ID: domain.SudoAllow, Label: "Allow once", Kind: "allow_once"}, {ID: domain.SudoDeny, Label: "Deny", Kind: "reject_once"}}
	for _, member := range []domain.MemberID{e.member.ID, admin.ID} {
		item, ok := attentionItems(t, e, member, protocol.AttentionListParams{})[run.ID]
		if !ok || item.Kind != protocol.AttentionKindPermission || item.Reason != "Permission requested" || item.Request == nil ||
			item.Request.ID != "sudo-a" || item.Request.Title != "Run as root: apt-get install -y jq" || !slices.Equal(item.Request.Options, options) {
			t.Fatalf("%s's attention item = %+v (listed %v)", member, item, ok)
		}
	}
	if item, ok := attentionItems(t, e, bob.ID, protocol.AttentionListParams{})[run.ID]; ok {
		t.Fatalf("Bob, who may not allow it, sees %+v", item)
	}

	respond := func(member domain.MemberID, request, option string) *protocol.Error {
		t.Helper()
		_, perr := callJSON(t, e, member, protocol.MethodRunInputRespond, protocol.RunInputRespondParams{RunID: string(run.ID), RequestID: request, OptionID: option})
		return perr
	}
	if perr := respond(bob.ID, "sudo-a", domain.SudoAllow); perr == nil || perr.Code != protocol.CodeDenied ||
		perr.Message != "run.input.respond: permission denied: allowing a command to run as root requires the session's owner or an admin" {
		t.Fatalf("Bob allows = %+v", perr)
	}
	if perr := respond(bob.ID, "sudo-a", "allow"); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("an unknown option = %+v", perr)
	}
	if perr := respond(bob.ID, "sudo-a", domain.SudoDeny); perr != nil {
		t.Fatalf("Bob denies = %v", perr)
	}
	if perr := respond(admin.ID, "sudo-b", domain.SudoAllow); perr != nil {
		t.Fatalf("an admin allows = %v", perr)
	}
	want := []string{"sudo:" + string(run.ID) + ":sudo-a:false:" + string(bob.ID), "sudo:" + string(run.ID) + ":sudo-b:true:" + string(admin.ID)}
	if calls := e.runs.Calls(); !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if perr := respond(e.member.ID, "sudo-a", domain.SudoAllow); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Fatalf("a decided request = %+v, want CodeNotFound", perr)
	}
}
