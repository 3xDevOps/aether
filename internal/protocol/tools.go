package protocol

import "github.com/3xDevOps/Aether/internal/domain"

// ToolManifest is stable executable metadata and has no server filesystem paths.
type ToolManifest struct {
	Executable string            `json:"executable,omitempty"`
	Version    string            `json:"version,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ToolSnapshot is immutable member/workspace tool metadata, never a host path.
type ToolSnapshot struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	MemberID    string       `json:"member_id"`
	Digest      string       `json:"digest"`
	Manifest    ToolManifest `json:"manifest"`
	CreatedAt   string       `json:"created_at"`
	Active      bool         `json:"active,omitempty"`
}

// ToolSnapshotFromDomain converts snapshot metadata without exposing storage.
func ToolSnapshotFromDomain(s domain.ToolSnapshot) ToolSnapshot {
	metadata := make(map[string]string, len(s.Manifest.Metadata))
	for key, value := range s.Manifest.Metadata {
		metadata[key] = value
	}
	return ToolSnapshot{
		ID:          string(s.ID),
		WorkspaceID: string(s.WorkspaceID),
		MemberID:    string(s.MemberID),
		Digest:      s.Digest,
		Manifest: ToolManifest{
			Executable: s.Manifest.Executable,
			Version:    s.Manifest.Version,
			Metadata:   metadata,
		},
		CreatedAt: rfc3339(s.CreatedAt),
	}
}

// ToolSnapshotListParams are the params of workspace.tools.list. Approved
// members may list snapshots only in their own workspace context.
type ToolSnapshotListParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
}

// ToolSnapshotListResult is the active and retained snapshot metadata.
type ToolSnapshotListResult struct {
	Active    *ToolSnapshot  `json:"active,omitempty"`
	Snapshots []ToolSnapshot `json:"snapshots"`
}

// ToolSnapshotVerifyParams are the params of workspace.tools.verify. The
// executable is a name, not a host path, and verification requires Launch.
type ToolSnapshotVerifyParams struct {
	Workspace              WorkspaceSelector `json:"workspace"`
	VerificationExecutable string            `json:"verification_executable,omitempty"`
}

// ToolSnapshotVerifyResult reports verification without a storage path.
type ToolSnapshotVerifyResult struct {
	Verified               bool          `json:"verified"`
	VerificationExecutable string        `json:"verification_executable,omitempty"`
	Error                  string        `json:"error,omitempty"`
	Snapshot               *ToolSnapshot `json:"snapshot,omitempty"`
}

// ToolSnapshotRollbackParams are the params of workspace.tools.rollback.
// Rollback mutates the active head and requires the existing Launch capability.
type ToolSnapshotRollbackParams struct {
	Workspace  WorkspaceSelector `json:"workspace"`
	SnapshotID string            `json:"snapshot_id"`
}

// ToolSnapshotRollbackResult is the newly active snapshot.
type ToolSnapshotRollbackResult struct {
	Snapshot ToolSnapshot `json:"snapshot"`
}

// ToolSnapshotResetParams are the params of workspace.tools.reset. Reset
// removes snapshots and requires the existing Launch capability.
type ToolSnapshotResetParams struct {
	Workspace WorkspaceSelector `json:"workspace"`
	Confirm   bool              `json:"confirm"`
}

// ToolSnapshotResetResult reports whether reset completed.
type ToolSnapshotResetResult struct {
	Reset bool `json:"reset"`
}
