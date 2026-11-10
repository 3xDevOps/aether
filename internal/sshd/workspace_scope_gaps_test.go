package sshd

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
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
