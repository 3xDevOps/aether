package scheduler

import (
	"testing"
)

// The workspace's upstream reaches the run's checkout, so a push or a
// pull request from inside the run goes where the workspace says.
func TestLaunchPassesWorkspaceOriginToTheCheckout(t *testing.T) {
	e := newTestEnv(t, nil)
	const origin = "https://github.com/acme/app.git"
	if err := e.db.SetWorkspaceOrigin(t.Context(), e.ws.ID, origin); err != nil {
		t.Fatalf("SetWorkspaceOrigin: %v", err)
	}

	run, _ := e.launchFake(t, "push from the run")
	if got := e.git.originFor(run.ID); got != origin {
		t.Fatalf("checkout origin = %q, want %q", got, origin)
	}
}
