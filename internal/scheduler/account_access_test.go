package scheduler

import (
	"testing"

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
