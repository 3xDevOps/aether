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

// The per-run authenticated transport remains available independently of the
// conflict-coordination policy. Disabled coordination still blocks peer/radar,
// mission, and lifecycle-report actions without removing run identity.
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
		// Every new container receives the verified CLI; runs receive identity
		// transport even when conflict coordination is disabled. The policy flag
		// still gates mission admission and harness lifecycle reporting.
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
