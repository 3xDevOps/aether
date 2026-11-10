package scheduler

import (
	"strings"
	"sync"
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

	if err := e.db.RevokeWorkspaceGrant(t.Context(), e.ws.ID, e.owner.ID, ""); err != nil {
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

	if err := e.db.RevokeWorkspaceGrant(t.Context(), e.ws.ID, e.member.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.sched.leaveRevokedAccounts(t.Context(), nil)

	waitFor(t, "the run to leave Ada's home", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Status.Terminal()
	})
}

// unscopedFallbackEnv is a fallback env on the real store, with Ada granted
// the workspace.
func unscopedFallbackEnv(t *testing.T) *shareEnv {
	t.Helper()
	e, _, _ := newFallbackEnv(t, func(cfg *Config) {
		enhancedClaude(cfg)
		cfg.Store = cfg.Store.(storetest.Unscoped).DB
	})
	if err := e.db.GrantWorkspace(t.Context(), e.ws.ID, e.member.ID, ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *shareEnv) revokeWorkspace(t *testing.T) {
	t.Helper()
	if err := e.db.RevokeWorkspaceGrant(t.Context(), e.ws.ID, e.member.ID, ""); err != nil {
		t.Error(err)
	}
}

func (e *shareEnv) waitStoppedByRevoke(t *testing.T, run domain.RunID) {
	t.Helper()
	waitFor(t, "the run to stop", func() bool {
		row, err := e.db.GetRun(t.Context(), run)
		return err == nil && row.Status.Terminal() && strings.HasPrefix(row.Reason, workspaceRevokedReason)
	})
}

// A grant revoked after Ada's launch on her own account was admitted, and
// before its container exists, fails the launch: no container starts.
func TestWorkspaceRevokeBeforeTheContainerFailsTheLaunch(t *testing.T) {
	t.Parallel()
	e := unscopedFallbackEnv(t)
	var created domain.RunID
	e.git.createHook = func(id domain.RunID) {
		created = id
		e.revokeWorkspace(t)
	}

	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "revoked", "claude", domain.LaunchACP)
	if err == nil || !strings.Contains(err.Error(), "check workspace access") {
		t.Fatalf("Launch = %v, want a refusal from the workspace access check", err)
	}
	if n := e.containerCount(); n != 0 {
		t.Fatalf("%d containers, want none", n)
	}
	if row, err := e.db.GetRun(t.Context(), created); err != nil || row.Status != domain.RunFailed {
		t.Fatalf("run = %+v, %v; want it failed", row, err)
	}
}

// A grant revoked while Ada's own-account launch creates its container is
// caught once the run is running: the run stops.
func TestWorkspaceRevokeDuringTheContainerStopsTheRun(t *testing.T) {
	t.Parallel()
	e := unscopedFallbackEnv(t)
	e.sched.recoveryReadyOnce.Do(func() { close(e.sched.recoveryReady) })
	var once sync.Once
	e.rt.createHook = func() { once.Do(func() { e.revokeWorkspace(t) }) }

	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "revoked", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}

	e.waitStoppedByRevoke(t, run.ID)
}

// The sweep stops a run on its launcher's own account once the launcher
// loses the run's workspace.
func TestSweepStopsAnOwnAccountRunWhoseLauncherLostTheWorkspace(t *testing.T) {
	t.Parallel()
	e := unscopedFallbackEnv(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "own", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	e.sched.leaveRevokedAccounts(t.Context(), nil)
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status.Terminal() {
		t.Fatalf("run while Ada reaches the workspace = %+v, %v; want it kept", row, err)
	}

	e.revokeWorkspace(t)
	e.sched.leaveRevokedAccounts(t.Context(), nil)

	e.waitStoppedByRevoke(t, run.ID)
}
