package scheduler

import (
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Two launches by one member provision at once: the launcher's admission
// lock is released when the run is admitted, and the member's cache lock once
// the container is published, so neither waits for the other's attach.
func TestLaunchesOverlapAfterAdmission(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	arrived := make(chan struct{}, 2)
	both := make(chan struct{})
	var bothOnce sync.Once
	e.git.watchHook = func(domain.RunID) {
		arrived <- struct{}{}
		if len(arrived) == 2 {
			bothOnce.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(10 * time.Second):
		}
	}
	// admission stands in for sshd's authorizationMu.
	var admission sync.Mutex
	launch := func(task string) error {
		admission.Lock()
		unlock := sync.OnceFunc(admission.Unlock)
		defer unlock()
		_, err := e.sched.LaunchWithOptions(ctx, e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchTUI, domain.LaunchOptions{Admitted: unlock})
		return err
	}
	errs := make(chan error, 2)
	for _, task := range []string{"first", "second"} {
		go func() { errs <- launch(task) }()
	}
	select {
	case <-both:
	case <-time.After(10 * time.Second):
		t.Fatal("the second launch did not provision while the first was still provisioning")
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("launch: %v", err)
		}
	}
}

// A Kill that lands the moment a launch is admitted, after its launcher let
// go of the admission lock and before any provisioning, stops the launch; the
// run is already among the active runs a revocation lists.
func TestKillAtAdmissionStopsTheLaunch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	var admitted *domain.Run
	kill := func() {
		active, err := e.db.ListActiveRuns(ctx)
		if err != nil || len(active) != 1 {
			t.Errorf("active sessions at admission = %v, %v; want the admitted run", active, err)
			return
		}
		admitted = active[0]
		if err := e.sched.Kill(ctx, admitted.ID, e.member.ID); err != nil {
			t.Errorf("Kill at admission: %v", err)
		}
	}
	if _, err := e.sched.LaunchWithOptions(ctx, e.ws.ID, e.member.ID, e.member.ID, "killed at admission", "fake", domain.LaunchTUI, domain.LaunchOptions{Admitted: kill}); err == nil {
		t.Fatal("a launch killed at admission succeeded")
	}
	if admitted == nil {
		t.Fatal("the launch was never admitted")
	}
	if admitted.Status != domain.RunQueued {
		t.Fatalf("admitted run status = %s, want queued", admitted.Status)
	}
	if r := e.waitStoreStatus(t, admitted.ID, domain.RunAbandoned); r.Reason != "killed" {
		t.Fatalf("abandoned reason = %q, want killed", r.Reason)
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(admitted.ID)) == nil })
}

// A handoff and a protection committed while an admitted launch provisions
// survive it: provisioning writes only the columns it owns.
func TestHandoffAndProtectionDuringProvisioningSurvive(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	to := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(ctx, to); err != nil {
		t.Fatalf("create member: %v", err)
	}
	e.git.createHook = func(run domain.RunID) {
		if err := e.db.TransferRun(ctx, run, to.ID); err != nil {
			t.Errorf("transfer run: %v", err)
		}
		if err := e.db.SetRunProtected(ctx, run, true); err != nil {
			t.Errorf("protect run: %v", err)
		}
	}
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "handed off", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	got, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.MemberID != to.ID || !got.Protected || got.Worktree == "" || got.Branch == "" {
		t.Fatalf("run after provisioning = owner %s, protected %t, worktree %q, branch %q; want owner %s, protected, and the checkout",
			got.MemberID, got.Protected, got.Worktree, got.Branch, to.ID)
	}
}
