package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

type recoverRuntime struct {
	*fakeRuntime
	err error
}

func (r recoverRuntime) StartExecTTY(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("not supported")
}

func (r recoverRuntime) StartExecPipe(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("not supported")
}

func (r recoverRuntime) RecoverExec(context.Context, runtime.ExecIdentity) (runtime.ManagedExec, error) {
	return nil, r.err
}

// Only an exec the runtime proves gone counts as stopped; a transient failure
// must not let a second adapter start beside one that may still run.
func TestStopAssistantExecNeedsProofOfAbsence(t *testing.T) {
	identity := runtime.ExecIdentity{ContainerID: "c", ExecID: "e", CreationKey: "k", ClaimToken: "t"}
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"gone", fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, runtime.ErrNotFound), false},
		{"transient", fmt.Errorf("%w: docker is busy", runtime.ErrExecUnavailable), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, func(cfg *Config) { cfg.Runtime = recoverRuntime{fakeRuntime: newFakeRuntime(), err: tc.err} })
			err := e.sched.StopAssistantExec(t.Context(), identity)
			if (err != nil) != tc.wantErr {
				t.Fatalf("StopAssistantExec = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
