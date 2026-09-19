package server

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
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
			Dir:              filepath.Join(d.DataDir, "coord"),
			Disabled:         d.Config.CoordinationDisabled,
			Store:            d.Store,
			RetainsContainer: d.Runs.RetainsContainer,
			Mail:             mail,
			Bus:              d.Bus,
			Peers:            lazyRadar{ssh: d.SSH},
			PTY:              d.PTY,
			// The scheduler is the single writer of run statuses, so the
			// agent's own status reports land on it.
			Reports: d.Runs,
			// coord.report captures evidence before accepting the durable
			// outcome; the coordination service publishes its evidence event
			// only after finalization.
			Evidence:        coordEvidenceCapture{service: d.Evidence},
			EvidencePackets: d.Store,
			Mission:         lazyMission{ssh: d.SSH},
		})
		if err != nil {
			return nil, err
		}
		// Binary staging is independent from the conflict-coordination
		// kill switch. Every new container receives the verified CLI; only
		// enabled runs receive a socket and lifecycle assets.
		d.Runs.UseCoordination(
			svc,
			filepath.Join(d.DataDir, "runtime", "bin"),
			!d.Config.CoordinationDisabled,
		)
		return svc, nil
	})
}

// coordEvidenceCapture keeps the report seam narrow. Publication is owned by
// coord.CoordReport and happens only after its durable finalization; capturing
// alone must never create an externally visible event.
type coordEvidenceCapture struct {
	service *evidence.Service
}

func (c coordEvidenceCapture) Capture(ctx context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	if c.service == nil {
		return protocol.EvidencePacket{}, errors.New("server: evidence service is unavailable")
	}
	return c.service.Capture(ctx, req)
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
