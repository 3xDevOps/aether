package protocol

// MissionScopeDiagnostic explains the relationship between a task's intended
// scope and observed run changes. It is advisory evidence for review, not an
// execution lock or an independent verification result.
type MissionScopeDiagnostic struct {
	TaskID         string   `json:"task_id"`
	TaskRevision   int      `json:"task_revision"`
	RunID          string   `json:"run_id"`
	Kind           string   `json:"kind"`
	Paths          []string `json:"paths,omitempty"`
	PeerTaskID     string   `json:"peer_task_id,omitempty"`
	PeerRunID      string   `json:"peer_run_id,omitempty"`
	Unavailable    bool     `json:"unavailable,omitempty"`
	UnavailableWhy string   `json:"unavailable_why,omitempty"`
	Detail         string   `json:"detail,omitempty"`
}
