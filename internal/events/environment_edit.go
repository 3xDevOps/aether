package events

// TypeEnvironmentEdit reports a server-side environment edit: an agent
// rewriting a workspace's Dockerfile and manifest from an admin's change
// request. The dashboard streams it the way it streams a build - a
// status line plus raw agent output - and picks up the proposed version
// from the terminal event.
const TypeEnvironmentEdit Type = "environment.edit"

// EnvironmentEditStatus is the coarse state of one edit run.
type EnvironmentEditStatus string

const (
	// EnvironmentEditRunning: the agent is working; Line carries its
	// output while this status holds.
	EnvironmentEditRunning EnvironmentEditStatus = "running"
	// EnvironmentEditValidating: the agent finished and its output pair
	// is being checked against the contract.
	EnvironmentEditValidating EnvironmentEditStatus = "validating"
	// EnvironmentEditRetrying: validation failed and the one allowed
	// retry is starting with the error appended to the prompt.
	EnvironmentEditRetrying EnvironmentEditStatus = "retrying"
	// EnvironmentEditProposed: the edit was saved as a new definition
	// version; Version names it. Nothing builds until it is approved.
	EnvironmentEditProposed EnvironmentEditStatus = "proposed"
	// EnvironmentEditFailed: the edit produced nothing; Detail explains
	// and names the next step where one exists.
	EnvironmentEditFailed EnvironmentEditStatus = "failed"
)

// EnvironmentEditPayload is one moment of an environment edit. Harness
// names the agent driving it; Line carries one line of agent output while
// Status is running; Detail explains a failed Status; Version is the
// proposed definition version, set on proposed.
type EnvironmentEditPayload struct {
	Harness string                `json:"harness"`
	Status  EnvironmentEditStatus `json:"status"`
	Line    string                `json:"line,omitempty"`
	Detail  string                `json:"detail,omitempty"`
	Version int                   `json:"version,omitempty"`
}

func (EnvironmentEditPayload) EventType() Type { return TypeEnvironmentEdit }

func init() { registerPayload[EnvironmentEditPayload](TypeEnvironmentEdit) }
