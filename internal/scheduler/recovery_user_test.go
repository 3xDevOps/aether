package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

type unavailableRunMetadata struct {
	runtime.Runtime
	run runtime.ID
}

func (r unavailableRunMetadata) Inspect(ctx context.Context, id runtime.ID) (runtime.ContainerInfo, error) {
	if id == r.run {
		return runtime.ContainerInfo{}, errors.New("runtime inspection unavailable")
	}
	return r.Runtime.Inspect(ctx, id)
}

func TestRecoveryResolvesUnknownRunUserBeforeTerminalAdoption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		runUser      string
		terminalUser string
		conflict     bool
	}{
		{name: "root"},
		{name: "nonroot", runUser: "1000:1000", terminalUser: "1000:1000"},
		{name: "conflicting user", runUser: "1000:1000", terminalUser: "2000:2000", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			run, container := e.launchFake(t, "recover container identity")
			terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			if closeErr := e.sched.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			container.spec.User = tc.runUser
			terminalContainer, err := e.rt.get(runtime.ID(terminal.ContainerID))
			if err != nil {
				t.Fatal(err)
			}
			terminalContainer.spec.User = tc.terminalUser

			stored, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			unavailable := e.newScheduler(t, e.rt, newFakePTY())
			unavailable.cfg.Runtime = unavailableRunMetadata{Runtime: e.rt, run: container.id}
			unavailable.recoverSupervised(t.Context(), stored)
			if _, adoptErr := unavailable.EnsureTerminal(t.Context(), e.member.ID); adoptErr == nil {
				t.Fatal("terminal adopted while the live run's identity was unknown")
			}
			if closeErr := unavailable.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}

			recoveredPTY := newFakePTY()
			recovered := e.newScheduler(t, e.rt, recoveredPTY)
			recovered.recoverSupervised(t.Context(), stored)
			adopted, err := recovered.EnsureTerminal(t.Context(), e.member.ID)
			if tc.conflict {
				if err == nil {
					t.Fatal("terminal adopted with a conflicting live run user")
				}
			} else {
				if err != nil {
					t.Fatalf("terminal adoption after successful inspection: %v", err)
				}
				if adopted.ContainerID != terminal.ContainerID || !recovered.hasTerminalSession(e.member.ID, terminalTabMain) {
					t.Fatalf("terminal adoption = %+v, want attached survivor %s", adopted, terminal.ContainerID)
				}
			}
			persisted, err := recovered.readSidecar(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.RunUser != tc.runUser {
				t.Fatalf("persisted run user = %q, want %q", persisted.RunUser, tc.runUser)
			}
			if container.currentState() != "running" || terminalContainer.currentState() != "running" ||
				recoveredPTY.session(run.ID) == nil {
				t.Fatal("identity recovery did not preserve the running containers and agent session")
			}
		})
	}
}
