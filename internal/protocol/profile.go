package protocol

import "github.com/3xDevOps/Aether/internal/domain"

// Profile control-channel method names. Kept out of the main method const
// block so later waves can add profile RPCs without colliding on that file.
const (
	MethodProfilePush     = "profile.push"
	MethodProfileStatus   = "profile.status"
	MethodProfileRollback = "profile.rollback"
)

// ProfileFile is one path in a push: either full content, or a digest
// referencing a blob the server already has / that accompanies the request.
type ProfileFile struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Content []byte `json:"content,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// ProfileBlob is a content-addressed file body (sha256 hex digest).
type ProfileBlob struct {
	Digest  string `json:"digest"`
	Content []byte `json:"content"`
}

// ProfilePushParams are the params of profile.push. Send either Files
// (path/mode/content) or a content-addressed delta (Paths + only the Blobs
// the server does not already have). WorkspaceID is optional: when set with
// AllowSecret, the server stamps a workspace timeline audit entry.
type ProfilePushParams struct {
	Harness     string        `json:"harness"`
	WorkspaceID string        `json:"workspace_id,omitempty"`
	AllowSecret []string      `json:"allow_secret,omitempty"`
	Files       []ProfileFile `json:"files,omitempty"`
	Paths       []ProfileFile `json:"paths,omitempty"`
	Blobs       []ProfileBlob `json:"blobs,omitempty"`
}

// ProfilePushResult is the result of profile.push: the head snapshot.
type ProfilePushResult struct {
	Snapshot ProfileSnapshot `json:"snapshot"`
}

// ProfileSnapshot is the wire form of an immutable member+harness snapshot.
type ProfileSnapshot struct {
	ID        string `json:"id"`
	Harness   string `json:"harness"`
	Digest    string `json:"digest"`
	CreatedAt string `json:"created_at"`
}

// ProfileFileMeta is one path in a snapshot listing (no content).
type ProfileFileMeta struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode,omitempty"`
}

// ProfileStatusParams are the params of profile.status.
type ProfileStatusParams struct {
	Harness string `json:"harness"`
}

// ProfileStatusResult is the latest snapshot, its file path+hash list, and
// recent snapshots for a rollback UI. Snapshot is nil when none exists.
type ProfileStatusResult struct {
	Snapshot  *ProfileSnapshot  `json:"snapshot,omitempty"`
	Files     []ProfileFileMeta `json:"files,omitempty"`
	Snapshots []ProfileSnapshot `json:"snapshots"`
}

// ProfileRollbackParams are the params of profile.rollback. This does not
// change any run pin.
type ProfileRollbackParams struct {
	Harness    string `json:"harness"`
	SnapshotID string `json:"snapshot_id"`
}

// ProfileRollbackResult is the head snapshot after rollback.
type ProfileRollbackResult struct {
	Snapshot ProfileSnapshot `json:"snapshot"`
}

// ProfileSnapshotFromDomain converts a domain snapshot to its wire form.
func ProfileSnapshotFromDomain(s domain.ProfileSnapshot) ProfileSnapshot {
	return ProfileSnapshot{
		ID:        string(s.ID),
		Harness:   s.Harness,
		Digest:    s.Digest,
		CreatedAt: rfc3339(s.CreatedAt),
	}
}
