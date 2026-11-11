package sshd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memory"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// A development shell attach to a run in wsB answers A, U and N as for a
// run that does not exist, even with the run's account shared with them;
// the super admin attaches.
func TestDevelopmentShellAttachOfAnUnreachableRunReadsAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	installDevelopmentTestService(w.e)
	ctx := t.Context()
	for _, m := range []domain.MemberID{w.e.member.ID, w.a.ID, w.u.ID, w.n.ID} {
		if _, err := w.e.db.ShareAccount(ctx, w.b.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	attach := func(who domain.MemberID, run string, readOnly bool) (*LocalTerminal, protocol.AttachResponse) {
		t.Helper()
		term, ack, _ := w.e.srv.Local(who).Attach(ctx, protocol.AttachRequest{RunID: run, Shell: "shell", ReadOnly: readOnly,
			ControlSessionID: "shell-writer", Cols: 80, Rows: 24})
		return term, ack
	}
	echo := strings.NewReplacer(string(w.runB.ID), "<id>", noRun, "<id>")
	for who, m := range map[string]domain.MemberID{"A": w.a.ID, "U": w.u.ID, "N": w.n.ID} {
		for _, readOnly := range []bool{true, false} {
			_, got := attach(m, string(w.runB.ID), readOnly)
			_, missing := attach(m, noRun, readOnly)
			if got.OK || missing.Code != protocol.CodeNotFound || got.Code != missing.Code || echo.Replace(got.Error) != echo.Replace(missing.Error) {
				t.Errorf("%s shell attach (read-only %v) to wsB's run = %+v, to a missing run = %+v; want the same not found", who, readOnly, got, missing)
			}
		}
	}
	term, ack := attach(w.e.member.ID, string(w.runB.ID), true)
	if !ack.OK || ack.TerminalID != "shell" {
		t.Fatalf("S shell attach to wsB's run = %+v, want the shell", ack)
	}
	_ = term.Close()
}

// profile.push naming wsB answers A, U and N as a workspace that does not
// exist; B and the super admin push there.
func TestProfilePushToAnUnreachableWorkspaceReadsAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	withProfiles(t)(&w.e.srv.cfg)
	push := func(ws string) protocol.ProfilePushParams {
		return protocol.ProfilePushParams{Harness: "claude", WorkspaceID: ws,
			Files: []protocol.ProfileFile{{Path: "settings.json", Mode: 0o644, Content: []byte(`{"model":"opus"}`)}}}
	}
	for _, who := range []string{"A", "U", "N"} {
		w.sameAsMissing(t, who, protocol.MethodProfilePush, push(string(w.wsB.ID)), push(noWorkspace))
	}
	for _, who := range []string{"B", "S"} {
		if err := w.call(t, who, protocol.MethodProfilePush, push(string(w.wsB.ID)), nil); err != nil {
			t.Errorf("%s profile.push to wsB: %v", who, err)
		}
	}
}

// A handing a run off to B or N, whom A does not see, answers as a handoff
// to a member who does not exist; A hands it to U.
func TestHandoffToAnInvisibleMemberReadsAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	run := &domain.Run{WorkspaceID: w.wsA.ID, MemberID: w.a.ID, Task: "hand me off", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunRunning}
	if err := w.e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	handoff := func(to string) protocol.RunHandoffParams {
		return protocol.RunHandoffParams{RunID: string(run.ID), ToMemberID: to}
	}
	for _, to := range []*domain.Member{w.b, w.n} {
		w.sameAsMissing(t, "A", protocol.MethodRunHandoff, handoff(string(to.ID)), handoff(noMember))
	}
	if err := w.call(t, "A", protocol.MethodRunHandoff, handoff(string(w.u.ID)), nil); err != nil {
		t.Fatalf("A hands the run to U: %v", err)
	}
}

// A asking for the wsA brief of B, whom A does not see, or of pending P,
// whom A sees but who cannot reach wsA, answers as for a member who does
// not exist; A reads U's.
func TestMemoryBriefOfAMemberOutsideTheWorkspaceReadsAsMissing(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	svc, err := memory.New(memory.Config{Store: w.e.db, Bus: w.bus, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	w.e.srv.cfg.Services.Memory = svc
	_, p := addMember(t, w.e, "P", domain.RoleCollaborator, true)
	brief := func(member string) protocol.MemoryBriefParams {
		return protocol.MemoryBriefParams{WorkspaceID: string(w.wsA.ID), MemberID: member}
	}
	for _, m := range []*domain.Member{w.b, p} {
		w.sameAsMissing(t, "A", protocol.MethodMemoryBrief, brief(string(m.ID)), brief(noMember))
	}
	if err := w.call(t, "A", protocol.MethodMemoryBrief, brief(string(w.u.ID)), nil); err != nil {
		t.Fatalf("A reads U's wsA brief: %v", err)
	}
}
