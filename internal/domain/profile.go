package domain

import "time"

// ProfileSnapshotID identifies an immutable per-member+harness profile snapshot.
type ProfileSnapshotID string

// ProfileSnapshot is one content-addressed capture of a member's agent
// profile for a single harness. Identical trees reuse Digest and identity.
type ProfileSnapshot struct {
	ID        ProfileSnapshotID
	MemberID  MemberID
	Harness   string
	Digest    string // hex sha256 of the canonical snapshot
	CreatedAt time.Time
}
