package server

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Conflict coordination (): the run-to-run mailbox and the per-run
// coordination socket under <data>/coord. The service is built even when
// the kill switch is off, because turning coordination off still has host
// work to do - the sockets a previous process left behind are unlinked so
// the mounts already inside live containers go inert.
func init() {
	registerService("coord", func(d Deps) (Service, error) {
		mail, ok := d.Store.(store.MessageStore)
		if !ok {
			return nil, errors.New("coordination needs a store with the run mailbox")
		}
		svc, err := coord.New(coord.Config{
			Dir:      filepath.Join(d.DataDir, "coord"),
			Store:    d.Store,
			Mail:     mail,
			Bus:      d.Bus,
			Peers:    lazyRadar{ssh: d.SSH},
			PTY:      d.PTY,
			Disabled: d.Config.CoordinationDisabled,
		})
		if err != nil {
			return nil, err
		}
		// The in-container half (): the scheduler stages the MCP
		// bridge binary and mounts it beside this service's per-run
		// directory. Leaving the seam unset with the kill switch off is
		// what keeps new containers free of coordination assets, while the
		// service above still runs to make the old ones inert.
		if !d.Config.CoordinationDisabled {
			d.Runs.UseCoordination(svc, filepath.Join(d.DataDir, "runtime", "bin"))
		}
		return svc, nil
	})
}

// lazyRadar reads the conflict radar seam at call time. The overlap index
// attaches itself to the same sshd config from its own builder, which may
// run after this one; authorization is only ever consulted long after
// every builder has run.
type lazyRadar struct{ ssh *sshd.Config }

func (r lazyRadar) Overlaps(ctx context.Context) ([]overlap.Entry, error) {
	idx := r.ssh.Services.Overlaps
	if idx == nil {
		return nil, errors.New("server: the conflict radar is not enabled")
	}
	return idx.Overlaps(ctx)
}
