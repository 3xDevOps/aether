package sshd

import (
	"bufio"
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// gatedEnv builds a test env whose fakePTY enforces the real write gate
// against the env's store, mirroring the server assembly's wiring.
func gatedEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	e.pty.gate = NewWriteGate(e.store)
	return e
}

// attachAs opens the attach subsystem for run as signer and returns the
// ack. withPTY requests a pty-req (write-capable attach).
func attachAs(t *testing.T, e *testEnv, signer ssh.Signer, run domain.RunID, withPTY bool) protocol.AttachResponse {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var setup func(*ssh.Session) error
	if withPTY {
		setup = func(s *ssh.Session) error {
			return s.RequestPty("xterm", 24, 80, ssh.TerminalModes{})
		}
	}
	pipe := openSubsystem(t, client, protocol.SubsystemAttach, setup)
	if _, err := pipe.Write([]byte(`{"run_id":"` + string(run) + `"}` + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, bufio.NewReader(pipe), &ack)
	t.Cleanup(func() { _ = pipe.Close() })
	return ack
}

func wantDenied(t *testing.T, err error, what string) {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("%s = %v, want CodeDenied", what, err)
	}
}

// A viewer cannot write to a PTY but read-only attach stays open.
func TestViewerReadOnlyAttachAllowedWriteDenied(t *testing.T) {
	e := gatedEnv(t)
	e.pty.replay = []byte("out")
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)

	if ack := attachAs(t, e, viewer, e.run.ID, false); !ack.OK {
		t.Fatalf("viewer read-only attach ack = %+v, want ok", ack)
	}

	if ack := attachAs(t, e, viewer, e.run.ID, true); ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("viewer write attach ack = %+v, want denied", ack)
	}
}

// A collaborator can write to another member's PTY by default.
func TestCollaboratorWriteAttachAllowedByDefault(t *testing.T) {
	e := gatedEnv(t)
	e.pty.replay = []byte("out")
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	if ack := attachAs(t, e, collab, e.run.ID, true); !ack.OK {
		t.Fatalf("collaborator write attach ack = %+v, want ok", ack)
	}
}

// A collaborator can kill another member's run by default; a viewer
// cannot kill, delete, or launch anything.
func TestCollaboratorKillsOthersRunViewerDenied(t *testing.T) {
	e := newTestEnv(t, nil)
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)

	cc := controlAs(t, e, collab)
	if err := cc.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)}, nil); err != nil {
		t.Fatalf("collaborator kill of admin's run: %v", err)
	}

	vc := controlAs(t, e, viewer)
	wantDenied(t, vc.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)}, nil), "viewer run.kill")
	wantDenied(t, vc.Call(protocol.MethodRunDelete, protocol.RunIDParams{RunID: string(e.run.ID)}, nil), "viewer run.delete")
	wantDenied(t, vc.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "t", Harness: "claude",
	}, nil), "viewer run.launch")
	wantDenied(t, vc.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(e.run.ID), Message: "hi"}, nil), "viewer run.inject")
}

// A protected run rejects non-owner steer and kill, even for
// collaborators, while the owner and admins stay unaffected.
func TestProtectedRunRestrictsToOwnerAndAdmin(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	// The admin (owner of e.run) protects it.
	admin := controlClient(t, e)
	var pr protocol.RunResult
	if err := admin.Call(protocol.MethodRunProtect, protocol.RunProtectParams{RunID: string(e.run.ID), Protected: true}, &pr); err != nil {
		t.Fatalf("run.protect: %v", err)
	}
	if !pr.Run.Protected {
		t.Fatal("run.protect result not protected")
	}
	run, err := e.store.GetRun(ctx, e.run.ID)
	if err != nil || !run.Protected {
		t.Fatalf("stored run protected = %v (err %v), want true", run != nil && run.Protected, err)
	}

	cc := controlAs(t, e, collab)
	wantDenied(t, cc.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(e.run.ID), Message: "x"}, nil), "non-owner steer of protected run")
	wantDenied(t, cc.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)}, nil), "non-owner kill of protected run")
	// Non-owner cannot toggle protection either.
	wantDenied(t, cc.Call(protocol.MethodRunProtect, protocol.RunProtectParams{RunID: string(e.run.ID)}, nil), "non-owner run.protect")

	// The owner (admin here) still steers.
	if err := admin.Call(protocol.MethodRunPause, protocol.RunIDParams{RunID: string(e.run.ID)}, nil); err != nil {
		t.Fatalf("owner pause of protected run: %v", err)
	}
}

// steer_others=admins_only blocks a collaborator from steering or killing
// another member's run but leaves their own runs steerable.
func TestSteerOthersAdminsOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	// Admin flips the workspace setting; the change lands in the store and
	// on the timeline.
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	admin := controlClient(t, e)
	var sr protocol.WorkspaceSettingsResult
	if err := admin.Call(protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{
		WorkspaceID: string(e.ws.ID), SteerOthers: domain.SteerOthersAdminsOnly,
	}, &sr); err != nil {
		t.Fatalf("workspace.settings: %v", err)
	}
	if sr.Workspace.SteerOthers != domain.SteerOthersAdminsOnly {
		t.Fatalf("workspace.settings result steer_others = %q", sr.Workspace.SteerOthers)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineNote || ev.ActorID != e.member.ID {
			t.Errorf("settings timeline event = %+v actor %s", ev.Payload, ev.ActorID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline event for workspace.settings")
	}

	cc := controlAs(t, e, collab)
	wantDenied(t, cc.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(e.run.ID), Message: "x"}, nil), "admins_only steer of another's run")
	wantDenied(t, cc.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(e.run.ID)}, nil), "admins_only kill of another's run")

	// Own runs stay steerable under admins_only.
	own := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: cm.ID, Task: "mine",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if cerr := e.store.CreateRun(ctx, own); cerr != nil {
		t.Fatalf("create run: %v", cerr)
	}
	if err := cc.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(own.ID), Message: "x"}, nil); err != nil {
		t.Fatalf("admins_only steer of own run: %v", err)
	}

	// workspace.settings itself is admin-only.
	wantDenied(t, cc.Call(protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{
		WorkspaceID: string(e.ws.ID), SteerOthers: "",
	}, nil), "collaborator workspace.settings")
}

// Handoff is owner-or-admin: a non-owner collaborator is denied, the
// owner may hand off, and the handoff lands on the timeline attributed to
// the acting member.
func TestHandoffOwnerOrAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	_, om := addMember(t, e, "Omar", domain.RoleCollaborator, false)

	cc := controlAs(t, e, collab)
	// e.run is owned by the admin; a non-owner non-admin cannot hand it off.
	wantDenied(t, cc.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(cm.ID),
	}, nil), "non-owner handoff")

	// The owner hands off their own run.
	own := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: cm.ID, Task: "mine",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if cerr := e.store.CreateRun(ctx, own); cerr != nil {
		t.Fatalf("create run: %v", cerr)
	}
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck
	if herr := cc.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(own.ID), ToMemberID: string(om.ID),
	}, nil); herr != nil {
		t.Fatalf("owner handoff: %v", herr)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineHandoff || ev.ActorID != cm.ID || ev.RunID != own.ID {
			t.Errorf("handoff event = %+v actor %s run %s", ev.Payload, ev.ActorID, ev.RunID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline event for handoff")
	}

	got, err := e.store.GetRun(ctx, own.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.MemberID != om.ID {
		t.Errorf("run owner after handoff = %s, want %s", got.MemberID, om.ID)
	}

	// Admin may hand off anyone's run (now Omar's).
	admin := controlClient(t, e)
	if err := admin.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(own.ID), ToMemberID: string(cm.ID),
	}, nil); err != nil {
		t.Fatalf("admin handoff: %v", err)
	}

	// A viewer is denied handoff too.
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	vc := controlAs(t, e, viewer)
	wantDenied(t, vc.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(own.ID), ToMemberID: string(om.ID),
	}, nil), "viewer handoff")
}

// A run must land on someone who can act on it: handing one to a viewer
// or to a member awaiting approval would orphan it, so both are refused
// before ownership moves.
func TestHandoffRecipientMustBeAbleToOwnRun(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, viewer := addMember(t, e, "Vera", domain.RoleViewer, false)
	_, pending := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	admin := controlClient(t, e)

	wantInvalid := func(err error, what string) {
		t.Helper()
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s = %v, want CodeInvalidParams", what, err)
		}
	}
	wantInvalid(admin.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(viewer.ID),
	}, nil), "handoff to viewer")
	wantInvalid(admin.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(pending.ID),
	}, nil), "handoff to pending member")

	// An unknown recipient is a not-found, not a silent transfer.
	var pe *protocol.Error
	err := admin.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: "nobody",
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("handoff to unknown member = %v, want CodeNotFound", err)
	}

	// Nothing moved.
	run, gerr := e.store.GetRun(ctx, e.run.ID)
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	if run.MemberID != e.member.ID {
		t.Errorf("run owner = %q, want the original owner %q", run.MemberID, e.member.ID)
	}
}

// The run.protect timeline note is attributed to the acting member.
func TestRunProtectPublishesAttributedNote(t *testing.T) {
	e := newTestEnv(t, nil)
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	admin := controlClient(t, e)
	if err := admin.Call(protocol.MethodRunProtect, protocol.RunProtectParams{RunID: string(e.run.ID), Protected: true}, nil); err != nil {
		t.Fatalf("run.protect: %v", err)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineNote || ev.ActorID != e.member.ID || ev.RunID != e.run.ID {
			t.Errorf("protect event = %+v actor %s run %s", ev.Payload, ev.ActorID, ev.RunID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline event for run.protect")
	}
}

// Wire round trip: protected and steer_others appear on run.get and
// workspace.get results.
func TestPermissionFieldsOnWire(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	if err := e.store.SetRunProtected(ctx, e.run.ID, true); err != nil {
		t.Fatalf("SetRunProtected: %v", err)
	}
	if err := e.store.SetWorkspaceSteerOthers(ctx, e.ws.ID, domain.SteerOthersAdminsOnly); err != nil {
		t.Fatalf("SetWorkspaceSteerOthers: %v", err)
	}
	c := controlClient(t, e)
	var rr protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(e.run.ID)}, &rr); err != nil {
		t.Fatalf("run.get: %v", err)
	}
	if !rr.Run.Protected {
		t.Error("run.get did not carry protected")
	}
	var wg protocol.WorkspaceGetResult
	if err := c.Call(protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: string(e.ws.ID)}, &wg); err != nil {
		t.Fatalf("workspace.get: %v", err)
	}
	if wg.Workspace.SteerOthers != domain.SteerOthersAdminsOnly {
		t.Errorf("workspace.get steer_others = %q", wg.Workspace.SteerOthers)
	}
}

// The write gate resolves fresh state per attach: revoking a member mid
// flight denies the next write attach.
func TestWriteGateChecksFreshRole(t *testing.T) {
	e := gatedEnv(t)
	e.pty.replay = []byte("out")
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	if ack := attachAs(t, e, collab, e.run.ID, true); !ack.OK {
		t.Fatalf("collaborator write attach ack = %+v, want ok", ack)
	}

	// Demote to viewer; a fresh write attach is now denied.
	cm.Role = domain.RoleViewer
	if err := e.store.UpdateMember(context.Background(), cm); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if ack := attachAs(t, e, collab, e.run.ID, true); ack.OK || ack.Code != protocol.CodeDenied {
		t.Fatalf("demoted write attach ack = %+v, want denied", ack)
	}
}

// Guards surface store failures faithfully: an unknown run under a guard
// is CodeNotFound, not a denial.
func TestGuardUnknownRunIsNotFound(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	err := c.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: "run_missing"}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("guarded kill of unknown run = %v, want CodeNotFound", err)
	}
}
func TestWorkspaceImageAdminCanSetReadAndPersist(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	const image = "ghcr.io/example/workspace:v2"
	var set protocol.WorkspaceImageResult
	if setErr := controlClient(t, e).Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
		WorkspaceID: string(e.ws.ID),
		Image:       image,
	}, &set); setErr != nil {
		t.Fatalf("workspace.image set: %v", setErr)
	}
	if set.Image != image {
		t.Fatalf("workspace.image set image = %q, want %q", set.Image, image)
	}
	stored, err := e.store.GetWorkspace(ctx, e.ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if stored.Environment.CustomImage != image || stored.Environment.NeutralImage {
		t.Fatalf("stored environment = %+v, want custom image", stored.Environment)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineNote || p.Message != "workspace image set to "+image || ev.ActorID != e.member.ID {
			t.Fatalf("workspace.image timeline event = %+v actor %s", ev.Payload, ev.ActorID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeline event for workspace.image")
	}

	var read protocol.WorkspaceImageResult
	if err := controlClient(t, e).Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
		WorkspaceID: string(e.ws.ID),
	}, &read); err != nil {
		t.Fatalf("workspace.image read: %v", err)
	}
	if read.Image != image || read.Workspace.ID != string(e.ws.ID) {
		t.Fatalf("workspace.image read = %+v, want image %q", read, image)
	}
}

func TestWorkspaceImageDeniedForCollaborator(t *testing.T) {
	e := newTestEnv(t, nil)
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	wantDenied(t, controlAs(t, e, collab).Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
		WorkspaceID: string(e.ws.ID),
		Image:       "ghcr.io/example/workspace:v2",
	}, nil), "collaborator workspace.image")
}

func TestWorkspaceImageRejectsUnpinnedOrUppercaseRefs(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, image := range []string{
		"ghcr.io/example/workspace",
		"ghcr.io/Example/workspace:v2",
	} {
		var pe *protocol.Error
		err := controlClient(t, e).Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
			WorkspaceID: string(e.ws.ID),
			Image:       image,
		}, nil)
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Errorf("workspace.image %q = %v, want CodeInvalidParams", image, err)
		}
	}
}
