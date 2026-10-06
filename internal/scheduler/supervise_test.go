package scheduler

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestFailedExitReasonCarriesTheAgentsLastLine(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "migrate", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	c := e.rt.byName(string(run.ID))
	c.output("applying 0042_add_invoice_index\r\n")
	c.output(`migration 0042_add_invoice_index failed: relation "invoices" does not exist` + "\r\n\r\n")
	c.output("Error: " + strings.Repeat("x", 300) + "\r\n")
	c.exitNow(1)

	row := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	want := "agent exited 1: Error: " + strings.Repeat("x", 190) + "..."
	if row.Reason != want {
		t.Fatalf("reason = %q, want %q", row.Reason, want)
	}
}

func TestFailedExitReasonWithoutOutput(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "silent", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	e.rt.byName(string(run.ID)).exitNow(3)

	if row := e.waitStoreStatus(t, run.ID, domain.RunFailed); row.Reason != "agent exited 3" {
		t.Fatalf("reason = %q, want agent exited 3", row.Reason)
	}
}
