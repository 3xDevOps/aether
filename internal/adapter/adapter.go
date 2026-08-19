// Package adapter translates structured harness output into typed events
// (layer-2 observability, design §6.1). Each harness with a machine-readable
// headless output format gets a small Adapter that turns its stdout lines
// into typed event payloads; the Manager watches run-status events and pumps
// the PTY output of running headless runs through the matching adapter.
// Runs without an adapter degrade gracefully to the PTY + diff timeline;
// no feature may hard-require adapter events.
//
// Headless runs keep the Wave 1 TTY plumbing (Wave 1 contract §9.7), so
// harness output arrives with terminal artifacts: CRLF line endings,
// interleaved escape sequences, arbitrary chunk boundaries. A
// LineNormalizer scrubs those before any parsing, and adapters treat every
// unparseable line as opaque PTY output, never an error.
package adapter

import "github.com/3xDevOps/Aether/internal/events"

// Adapter translates one harness's structured output into typed event
// payloads. Implementations are line consumers fed by the Manager through
// a LineNormalizer.
type Adapter interface {
	// ConsumeLine consumes one normalized output line and returns the
	// typed payloads it encodes, none when the line carries no structured
	// data. It never fails: lines that do not parse are not errors, they
	// are ordinary PTY output.
	ConsumeLine(line string) []events.Payload
}

// adapters maps a run's Harness string to its adapter constructor.
var adapters = map[string]func() Adapter{
	"claude": newClaude,
}

// ForHarness returns a fresh adapter for the harness; ok is false when the
// harness has no structured-output adapter and its runs degrade to layer-1
// (PTY + diff) observability.
func ForHarness(harness string) (Adapter, bool) {
	fn, ok := adapters[harness]
	if !ok {
		return nil, false
	}
	return fn(), true
}
