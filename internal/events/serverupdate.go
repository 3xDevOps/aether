package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeServerUpdate reports an admin-triggered update of the server's own
// binaries. It is a privileged act like a settings change, so it is
// attributed and lands in every workspace's timeline; the CLI and the
// dashboard follow the same events to show the phases live.
const TypeServerUpdate Type = "server.update"

// ServerUpdatePhase is where one update has got to.
type ServerUpdatePhase string

const (
	// ServerUpdateScheduled: the update is recorded and waits for the
	// first moment with no active run and no open workspace shell.
	ServerUpdateScheduled ServerUpdatePhase = "scheduled"
	// ServerUpdateApplying: the release assets are being downloaded and
	// verified. Nothing has been replaced yet.
	ServerUpdateApplying ServerUpdatePhase = "applying"
	// ServerUpdateRestarting: the binaries are swapped and the server is
	// re-executing on the new version. Connections drop here.
	ServerUpdateRestarting ServerUpdatePhase = "restarting"
	// ServerUpdateFailed: nothing was replaced; Detail is the real error.
	ServerUpdateFailed ServerUpdatePhase = "failed"
	// ServerUpdateCancelled: a pending update was cleared.
	ServerUpdateCancelled ServerUpdatePhase = "cancelled"
)

// ServerUpdatePayload is one moment of a server self-update. ActorID
// repeats the envelope's actor so a client reading only the payload can
// still attribute the act; Detail explains a failed phase.
type ServerUpdatePayload struct {
	Phase   ServerUpdatePhase `json:"phase"`
	Version string            `json:"version,omitempty"`
	ActorID domain.MemberID   `json:"actor_id,omitempty"`
	Detail  string            `json:"detail,omitempty"`
}

func (ServerUpdatePayload) EventType() Type { return TypeServerUpdate }

func init() { registerPayload[ServerUpdatePayload](TypeServerUpdate) }
