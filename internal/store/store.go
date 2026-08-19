// Package store is the SQLite persistence layer for the core domain
// objects. It owns the schema, runs versioned migrations idempotently on
// open, and assigns IDs at creation time.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

var (
	// ErrNotFound is returned when the requested row does not exist, or
	// when a write references a row (via foreign key) that does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a write violates a uniqueness
	// constraint (e.g. a member with the same public key already exists).
	ErrConflict = errors.New("store: conflict")
	// ErrInUse is returned when a delete is blocked by rows that still
	// reference the target (e.g. a workspace with sessions).
	ErrInUse = errors.New("store: in use")
)

// Store is the persistence contract the rest of the system consumes.
//
// Create methods assign the object's ID (and CreatedAt when zero) and
// mutate the passed struct in place. Update methods replace every mutable
// field of the identified row; CreatedAt is immutable and never written by
// updates. Get and List return ErrNotFound / empty slices respectively
// when nothing matches. Constraint violations surface as ErrConflict,
// ErrInUse, or ErrNotFound (missing foreign key reference) — never as raw
// driver errors.
//
// Member public keys are canonicalized to "type base64" (options, comment,
// and surrounding whitespace stripped) on write and lookup, so equality
// follows the physical key rather than the authorized_keys line. A member
// may have an empty PublicKey or an empty TailnetLogin, never both.
type Store interface {
	CreateWorkspace(ctx context.Context, w *domain.Workspace) error
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
	ListWorkspaces(ctx context.Context) ([]*domain.Workspace, error)
	UpdateWorkspace(ctx context.Context, w *domain.Workspace) error
	DeleteWorkspace(ctx context.Context, id domain.WorkspaceID) error

	CreateSession(ctx context.Context, s *domain.Session) error
	GetSession(ctx context.Context, id domain.SessionID) (*domain.Session, error)
	ListSessions(ctx context.Context) ([]*domain.Session, error)
	ListSessionsByWorkspace(ctx context.Context, id domain.WorkspaceID) ([]*domain.Session, error)
	UpdateSession(ctx context.Context, s *domain.Session) error
	// SetSessionSteerOthers sets only the session's steer-others policy
	// ("" or domain.SteerOthersAdminsOnly), leaving every other field
	// untouched.
	SetSessionSteerOthers(ctx context.Context, id domain.SessionID, steerOthers string) error
	DeleteSession(ctx context.Context, id domain.SessionID) error

	CreateMember(ctx context.Context, m *domain.Member) error
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	GetMemberByPublicKey(ctx context.Context, publicKey string) (*domain.Member, error)
	GetMemberByTailnetLogin(ctx context.Context, login string) (*domain.Member, error)
	// ApproveMember clears only the member's pending flag, leaving every
	// other field untouched.
	ApproveMember(ctx context.Context, id domain.MemberID) error
	ListMembers(ctx context.Context) ([]*domain.Member, error)
	UpdateMember(ctx context.Context, m *domain.Member) error
	DeleteMember(ctx context.Context, id domain.MemberID) error

	CreateRun(ctx context.Context, r *domain.Run) error
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	ListRunsBySession(ctx context.Context, id domain.SessionID) ([]*domain.Run, error)
	ListRunsByMember(ctx context.Context, id domain.MemberID) ([]*domain.Run, error)
	ListActiveRuns(ctx context.Context) ([]*domain.Run, error)
	UpdateRun(ctx context.Context, r *domain.Run) error
	// UpdateRunStatus sets only the run's status, plus started/finished
	// timestamps when non-nil, leaving every other field untouched. This
	// is the mutator lifecycle transitions should use so they cannot
	// clobber concurrent writes to other fields.
	UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, startedAt, finishedAt *time.Time) error
	// TransferRun reassigns only the run's owning member (handoff),
	// leaving every other field untouched.
	TransferRun(ctx context.Context, id domain.RunID, to domain.MemberID) error
	// SetRunProtected sets only the run's protected flag, leaving every
	// other field untouched.
	SetRunProtected(ctx context.Context, id domain.RunID, protected bool) error
	DeleteRun(ctx context.Context, id domain.RunID) error

	// Profile snapshots are content-addressed per member+harness.
	// SaveProfileSnapshot assigns ID/CreatedAt when zero; if the digest
	// already exists for that member+harness the existing row is returned
	// (same identity) and files are not rewritten.
	SaveProfileSnapshot(ctx context.Context, s *domain.ProfileSnapshot, files []ProfileFile) error
	GetProfileSnapshot(ctx context.Context, id domain.ProfileSnapshotID) (*domain.ProfileSnapshot, error)
	GetProfileSnapshotByDigest(ctx context.Context, member domain.MemberID, harness, digest string) (*domain.ProfileSnapshot, error)
	ListProfileSnapshots(ctx context.Context, member domain.MemberID, harness string) ([]*domain.ProfileSnapshot, error)
	GetProfileFiles(ctx context.Context, id domain.ProfileSnapshotID) ([]ProfileFile, error)
	SetProfileHead(ctx context.Context, member domain.MemberID, harness string, id domain.ProfileSnapshotID) error
	GetProfileHead(ctx context.Context, member domain.MemberID, harness string) (*domain.ProfileSnapshot, error)
	PruneProfileSnapshots(ctx context.Context, member domain.MemberID, harness string, keep int) error
	SetRunProfileSnapshot(ctx context.Context, run domain.RunID, id domain.ProfileSnapshotID) error

	ApprovalStore
	TemplateStore
	CostStore

	Close() error
}

// ProfileFile is one path inside a stored profile snapshot.
type ProfileFile struct {
	Path    string
	Mode    uint32
	Content []byte
}
