package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// A run on a shared account works with its owner's login, so the sweep
// moves it off that account once the owner loses the run's workspace, as
// it does when the share itself is revoked.
func TestSweepMovesARunWhoseAccountOwnerLostTheWorkspace(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, func(cfg *Config) {
		enhancedClaude(cfg)
		cfg.Store = cfg.Store.(storetest.Unscoped).DB
	})
	if err := e.db.GrantWorkspace(t.Context(), e.ws.ID, e.member.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.db.GrantWorkspace(t.Context(), e.ws.ID, e.owner.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.share(t)
	run := e.launchOnGrace(t)

	e.sched.leaveRevokedAccounts(t.Context(), nil)
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.AccountMemberID != e.owner.ID {
		t.Fatalf("run while its account owner reaches the workspace = %+v, %v; want it kept on Grace's account", row, err)
	}

	if err := e.db.RevokeWorkspaceGrant(t.Context(), e.ws.ID, e.owner.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.leaveRevokedAccounts(t.Context(), nil)

	e.assertMovedToAda(t, rt, run.ID, notes, 0)
}

// A handoff keeps the launcher's home and account, so the sweep stops a run
// on them once the launcher loses the workspace, whoever owns it now.
func TestSweepStopsAHandedOffRunWhoseLauncherLostTheWorkspace(t *testing.T) {
	t.Parallel()
	e, _, _ := newFallbackEnv(t, func(cfg *Config) {
		enhancedClaude(cfg)
		cfg.Store = cfg.Store.(storetest.Unscoped).DB
	})
	lin := e.addRecipient(t)
	for _, m := range []domain.MemberID{e.member.ID, lin.ID} {
		if err := e.db.GrantWorkspace(t.Context(), e.ws.ID, m, ""); err != nil {
			t.Fatal(err)
		}
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "handed", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.TransferRunWithHandoff(t.Context(), &store.HandoffOutbox{
		ID: "handoff-test", WorkspaceID: e.ws.ID, RunID: run.ID, ActorID: e.member.ID,
		FromMemberID: e.member.ID, ToMemberID: lin.ID,
		EvidenceState: store.HandoffEvidencePending, PublicationState: store.HandoffPublicationPending,
	}); err != nil {
		t.Fatal(err)
	}
	container := e.rt.byName(string(run.ID))

	e.sched.leaveRevokedAccounts(t.Context(), nil)
	time.Sleep(100 * time.Millisecond)
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status != domain.RunRunning || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("handed-off run while its launcher reaches the workspace = %+v, %v; want it kept", row, err)
	}

	if err := e.db.RevokeWorkspaceGrant(t.Context(), e.ws.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.leaveRevokedAccounts(t.Context(), nil)

	waitFor(t, "the run to leave Ada's home", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Status.Terminal()
	})
}
