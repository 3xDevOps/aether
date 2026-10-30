package server

import (
	"context"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
)

type coordWakeAdmission struct {
	store   coord.Runs
	control *control.Service
	// unheld refuses a run a member controls. A child run is always refused
	// while a member controls it.
	unheld bool
}

func newCoordWakeAdmission(d Deps) coord.WakeAdmission {
	return coordWakeAdmission{store: d.Store, control: d.Control}.Admit
}

// newCoordReminderAdmission is the wake admission that also refuses a run a
// member controls: the next prompt is theirs.
func newCoordReminderAdmission(d Deps) coord.WakeAdmission {
	return coordWakeAdmission{store: d.Store, control: d.Control, unheld: true}.Admit
}

func (a coordWakeAdmission) Admit(ctx context.Context, run domain.RunID, dispatch func() error) error {
	if a.control == nil || a.store == nil || dispatch == nil {
		return errors.New("coord wake: admission authority unavailable")
	}
	// This first lookup only selects which control path to enter; the run is
	// checked again inside it.
	initial, err := a.store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	admit := func() error {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		current, err := a.store.GetRun(checkCtx, run)
		if err != nil {
			return err
		}
		if current == nil || current.ID != run || !current.Mode.Interactive() || current.Protected ||
			(current.Status != domain.RunRunning && current.Status != domain.RunNeedsAttention) {
			return errors.New("coord wake: session is not an eligible live interactive session")
		}
		if err := checkCtx.Err(); err != nil {
			return err
		}
		return dispatch()
	}
	if a.unheld || initial.ParentRunID != "" && !initial.Detached {
		return a.control.TryAdmitUnheld(string(run), admit)
	}
	return a.control.TryAdmit(string(run), admit)
}
