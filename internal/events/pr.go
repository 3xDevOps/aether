package events

import (
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TypeRunPR signals that the server read or changed a run's pull request on
// GitHub. Consumers re-read run.pr.status or run.pr.feedback for details.
const TypeRunPR Type = "run.pr"

type RunPRPayload struct {
	RunID domain.RunID `json:"run_id"`
	// Repo is the extra repository's workspace for a pull request opened
	// from an extra repository; empty for the primary one.
	Repo     domain.WorkspaceID `json:"repo,omitempty"`
	Number   int                `json:"number"`
	URL      string             `json:"url"`
	State    string             `json:"state"`
	MergedAt *time.Time         `json:"merged_at,omitempty"`
}

func (RunPRPayload) EventType() Type { return TypeRunPR }

func (p RunPRPayload) RepoWorkspace() domain.WorkspaceID { return p.Repo }

func init() { registerPayload[RunPRPayload](TypeRunPR) }
