package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func (r *Runtime) Freeze(id runtime.ID) (bool, error) {
	key, ok := vmKey(id)
	if !ok {
		return false, errors.New("vm runtime: only a VM can be frozen")
	}
	return r.vms.freeze(key)
}

func (r *Runtime) Frozen() []runtime.ID { return r.vms.frozenVMs() }

func (r *Runtime) Thaw(id runtime.ID) error {
	key, ok := vmKey(id)
	if !ok {
		return nil
	}
	return r.vms.thaw(key)
}

// freezeFile is the cgroup v2 freezer of key's unit. Writing it is all a
// freeze takes: no process starts and neither systemd, the monitor nor the
// guest has to answer, so it works on a host with no memory left. A frozen
// monitor runs no vCPU, so the guest touches no new memory.
func (e *engine) freezeFile(key string) string {
	return filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(key), "cgroup.freeze")
}

// frozen is false for a VM whose unit does not run.
func (e *engine) frozen(key string) bool {
	data, err := os.ReadFile(e.freezeFile(key))
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

// freeze reports whether it froze key's VM; one already frozen stays so. It
// never waits: a VM whose lock is held is in the middle of a start, stop,
// save or restore that needs its monitor to answer, and is refused, as is
// one that does not run.
func (e *engine) freeze(key string) (bool, error) {
	l := e.keyLock(key)
	if !l.TryLock() {
		return false, fmt.Errorf("vm runtime: freeze VM %s: it is starting, stopping, saving or restoring", key)
	}
	defer l.Unlock()
	if e.frozen(key) {
		return false, nil
	}
	if err := e.writeFreezer(key, "1"); err != nil {
		return false, fmt.Errorf("vm runtime: freeze VM %s: %w", key, err)
	}
	e.mu.Lock()
	e.frozenAt[key] = time.Now()
	e.mu.Unlock()
	return true, nil
}

// frozenVMs reads the units, so it also finds the VMs a server that has
// since stopped froze; those count as the longest frozen.
func (e *engine) frozenVMs() []runtime.ID {
	keys, err := e.keys()
	if err != nil {
		slog.Warn("vm runtime: list the frozen VMs", "error", err)
	}
	keys = slices.DeleteFunc(keys, func(key string) bool { return !e.frozen(key) })
	e.mu.Lock()
	slices.SortStableFunc(keys, func(a, b string) int { return e.frozenAt[a].Compare(e.frozenAt[b]) })
	e.mu.Unlock()
	ids := make([]runtime.ID, len(keys))
	for i, key := range keys {
		ids[i] = runtime.ID(idPrefix + key)
	}
	return ids
}

// thaw lets key's VM run on; one that is not frozen, or no longer runs, is
// left as it is.
func (e *engine) thaw(key string) error {
	if err := e.writeFreezer(key, "0"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("vm runtime: thaw VM %s: %w", key, err)
	}
	e.mu.Lock()
	delete(e.frozenAt, key)
	e.mu.Unlock()
	return nil
}

func (e *engine) writeFreezer(key, value string) error {
	f, err := os.OpenFile(e.freezeFile(key), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}
