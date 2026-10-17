package serverupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes the update lock under stateDir, so the server's own update and
// `aether-server update` never swap binaries at once. The lock is a flock
// on an O_CLOEXEC file: a process that exits or execs for the restart
// drops it.
func Lock(stateDir string) (unlock func(), err error) {
	if err = os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("serverupdate: %w", err)
	}
	path := filepath.Join(stateDir, "update.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("serverupdate: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: another update holds %s: %w", ErrBusy, path, err)
	}
	return func() { _ = f.Close() }, nil
}

// Staged is a release whose binaries are installed and whose swap record
// says verifying: the next start of aether-server runs it as a trial.
type Staged struct {
	// Binaries are the files replaced, the server first.
	Binaries []string
	dir      string
	rec      *swapRecord
}

// Stage moves the installation Plan describes to tag the way the server's
// own update does: refuses while the last update is unfinished, runs the
// release preflight, and replaces the binaries with a swap record the next
// start verifies. It never starts or stops anything and never opens the
// database.
func Stage(ctx context.Context, p Plan, tag string) (*Staged, error) {
	if err := ensureSettled(p.StateDir); err != nil {
		return nil, err
	}
	m, err := preflight(ctx, p, tag, false)
	if err != nil {
		return nil, err
	}
	rec, err := swap(ctx, p, tag, m)
	if err != nil {
		return nil, err
	}
	return &Staged{Binaries: rec.Binaries, dir: p.StateDir, rec: rec}, nil
}

// Snapshot copies the database when the release migrates it. Call it once
// the server has stopped and before it starts again.
func (s *Staged) Snapshot() error { return takeSnapshot(s.rec) }

// Undo puts the previous binaries back and drops the swap record, for a
// start that must not happen.
func (s *Staged) Undo() error { return unswap(s.dir, s.rec) }

// RollBack puts the previous binaries back, and the database snapshot when
// a start of the new binary migrated past it, and records cause for the
// previous binary's next start to report. Call it with the unit stopped.
func (s *Staged) RollBack(cause string) error { return rollBack(s.dir, s.rec, cause) }

// Outcome is where the last update stands in the swap record under a
// state directory.
type Outcome struct {
	// Settled is set once the new binary committed or was rolled back.
	Settled    bool
	RolledBack bool
	// Phase is the record's phase, empty when there is no record.
	Phase string
	// Error is the recorded reason for a rollback.
	Error string
}

// ReadOutcome reads the swap record under dir.
func ReadOutcome(dir string) (Outcome, error) {
	rec, err := loadRecord(dir)
	if err != nil || rec == nil {
		return Outcome{}, err
	}
	return Outcome{
		Settled:    rec.Phase == recordApplied || rec.Phase == recordRolledBack,
		RolledBack: rec.Phase == recordRolledBack,
		Phase:      rec.Phase,
		Error:      rec.Error,
	}, nil
}
