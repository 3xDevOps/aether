package domain

import "time"

// TurnPhase is where an Enhanced run's turn is, derived from its session.
type TurnPhase string

const (
	PhaseSetup         TurnPhase = "setup"
	PhaseFirstEvent    TurnPhase = "awaiting-first-event"
	PhaseToolRunning   TurnPhase = "tool-running"
	PhaseAwaitingInput TurnPhase = "awaiting-input"
	PhaseStreaming     TurnPhase = "streaming"
	PhaseBetweenTurns  TurnPhase = "between-turns"
)

// Transport says whether an Enhanced run's agent connection is open;
// unknown when the run has no session.
const (
	TransportOpen    = "open"
	TransportClosed  = "closed"
	TransportUnknown = "unknown"
)

// RunLiveness is a live Enhanced run's turn phase, when its agent last made
// progress (zero if not since its connection opened at Since), and whether
// that connection is open.
type RunLiveness struct {
	Phase        TurnPhase
	LastProgress time.Time
	Since        time.Time
	Transport    string
}
