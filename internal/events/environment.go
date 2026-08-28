package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeEnvironmentBuild reports a workspace environment build: the
// definition version moving through its lifecycle (building, verifying,
// active, failed) and the engine's build output while it runs. The
// dashboard and CLI stream it the way they stream run output.
const TypeEnvironmentBuild Type = "environment.build"

// EnvironmentBuildPayload is one moment of an environment build. Status is
// the definition version's lifecycle state at that moment; Line carries
// one line of engine build output while Status is building; Detail
// explains a failed Status.
type EnvironmentBuildPayload struct {
	Version int                      `json:"version"`
	Status  domain.EnvironmentStatus `json:"status"`
	Line    string                   `json:"line,omitempty"`
	Detail  string                   `json:"detail,omitempty"`
}

func (EnvironmentBuildPayload) EventType() Type { return TypeEnvironmentBuild }

func init() { registerPayload[EnvironmentBuildPayload](TypeEnvironmentBuild) }
