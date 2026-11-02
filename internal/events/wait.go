package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeRunWaitChanged tells readers of a run to read its wait conditions
// again: one changed state or delivery.
const TypeRunWaitChanged Type = "run.wait.changed"

type RunWaitChangedPayload struct {
	RunID       domain.RunID       `json:"run_id"`
	WorkspaceID domain.WorkspaceID `json:"workspace_id"`
	WaitID      string             `json:"wait_id"`
	State       string             `json:"state"`
}

func (RunWaitChangedPayload) EventType() Type { return TypeRunWaitChanged }

func init() { registerPayload[RunWaitChangedPayload](TypeRunWaitChanged) }
