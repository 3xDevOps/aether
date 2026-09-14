// Package evidence captures durable, factual evidence for completed or
// handed-off runs. Git snapshots and transcript copies are retained before
// metadata is written, so runtime cleanup cannot destroy the evidence a packet
// describes.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// MaxTranscriptBytes is the immutable upper bound for a retained cast.
	MaxTranscriptBytes = 16 << 20
	// DefaultRetention is the lifetime of retained evidence metadata and
	// supporting objects.
	DefaultRetention = 30 * 24 * time.Hour
	// DefaultPageSize is used when callers do not provide a list limit.
	DefaultPageSize = 50
	// MaxPageSize bounds every evidence list operation.
	MaxPageSize = 100
	// DefaultPatchBytes bounds rendered patch output.
	DefaultPatchBytes = 1 << 20
	// MaxObjectiveBytes and the other text bounds keep metadata compact even
	// when a harness reports unexpectedly large fields.
	MaxObjectiveBytes  = 4096
	MaxNextActionBytes = 2048
	MaxProvenanceBytes = 2048
	MaxUnresolvedFacts = 64
	MaxFactBytes       = 512
	MaxRelatedMessages = 100
	MaxChangedFiles    = 512
	MaxSourceFacts     = 16
)
const cleanupTimeout = 5 * time.Second
const tombstoneRetention = 30 * 24 * time.Hour

var (
	ErrExpired               = errors.New("evidence: packet expired")
	ErrTranscriptUnavailable = errors.New("evidence: transcript unavailable")
	ErrInvalidRequest        = errors.New("evidence: invalid capture request")
)

// Store is the narrow durable evidence metadata surface. It includes a
// bounded global expiry query so cleanup is restart-safe and does not depend
// on callers enumerating workspaces. Idempotency lookup is required so a
// retry can return a committed packet before touching live Git or transcript
// sources.
type Store interface {
	CreateEvidencePacket(context.Context, *store.EvidencePacket) error
	GetEvidencePacket(context.Context, string) (*store.EvidencePacket, error)
	GetEvidencePacketByIdempotency(context.Context, domain.MemberID, domain.RunID, string) (*store.EvidencePacket, error)
	ListEvidencePackets(context.Context, domain.WorkspaceID, domain.RunID, string, int) (*store.EvidencePacketPage, error)
	ListExpiredEvidencePackets(context.Context, time.Time, int) ([]*store.EvidencePacket, error)
	DeleteEvidencePacket(context.Context, string) error
}

// StagingStore journals artifacts before packet metadata is committed. The
// SQLite store clears the matching row in the packet insert transaction.
type StagingStore interface {
	CreateEvidenceStaging(context.Context, *store.EvidenceStaging) error
	ListEvidenceStaging(context.Context, time.Time, int) ([]*store.EvidenceStaging, error)
	DeleteEvidenceStaging(context.Context, string) error
}

// GitEvidence retains a run's exact tree and renders or removes its retained
// private revision. *gitengine.Engine satisfies this interface.
type GitEvidence interface {
	CaptureEvidence(context.Context, domain.RunID, string) (gitengine.EvidenceRevision, error)
	RenderEvidence(context.Context, domain.WorkspaceID, string, int) (gitengine.Patch, error)
	RemoveEvidence(context.Context, domain.WorkspaceID, string) error
}

// GitEvidencePruner optionally reclaims unreachable objects after a packet's
// evidence ref is removed. Production git engines implement this maintenance
// path; narrow test doubles and compatibility engines may omit it.
type GitEvidencePruner interface {
	PruneEvidence(context.Context, domain.WorkspaceID) error
}

// TranscriptExporter opens a finite export of a run's PTY transcript.
// The production adapter exposes Host.Replay without its attach-only byte
// count; evidence owns the bounded artifact copy below.
type TranscriptExporter interface {
	Replay(domain.RunID) (io.ReadCloser, error)
}

// RunLookup resolves the immutable run identity needed by a packet.
type RunLookup interface {
	GetRun(context.Context, domain.RunID) (*domain.Run, error)
}

// EventLookup provides the event boundary at capture time. events.EventLog
// satisfies this interface through LastSeq.
type EventLookup interface {
	LastSeq(context.Context) (uint64, error)
}

// File is the small writable-file surface used by the atomic transcript copy.
type File interface {
	io.Writer
	io.Closer
	Sync() error
}

type namedFile interface {
	File
	Name() string
}

// FileSystem is the filesystem seam used for private evidence storage.
// Implementations must make Rename atomic when source and destination share a
// directory, and SyncDir must flush directory entries before metadata is
// published. The default implementation uses os operations.
type FileSystem interface {
	MkdirAll(string, fs.FileMode) error
	CreateTemp(string, string) (File, error)
	Rename(string, string) error
	SyncDir(string) error
	Remove(string) error
	Stat(string) (fs.FileInfo, error)
	Open(string) (io.ReadCloser, error)
}

type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (osFileSystem) CreateTemp(dir, pattern string) (File, error) { return os.CreateTemp(dir, pattern) }
func (osFileSystem) Rename(oldPath, newPath string) error         { return os.Rename(oldPath, newPath) }
func (osFileSystem) SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
func (osFileSystem) Remove(path string) error                { return os.Remove(path) }
func (osFileSystem) Stat(path string) (fs.FileInfo, error)   { return os.Stat(path) }
func (osFileSystem) Open(path string) (io.ReadCloser, error) { return os.Open(path) }

// Config wires the service. Store, Git, Runs, and EvidenceDir are required.
// Transcript and Events are optional sources: an unavailable optional source
// is represented in Sources and UnresolvedFacts rather than making a packet
// claim that it was observed.
type Config struct {
	Store       Store
	Git         GitEvidence
	Transcript  TranscriptExporter
	Runs        RunLookup
	Events      EventLookup
	EvidenceDir string
	Retention   time.Duration
	Now         func() time.Time
	FS          FileSystem
}

// Request describes one automatic evidence capture. Objective and provenance
// are bounded and treated as untrusted text. Legacy CreatorID requests are
// interpreted as human-origin captures; new callers should set Origin.
type Request struct {
	RunID                 domain.RunID
	Origin                store.EvidenceOrigin
	OwnerID               domain.MemberID
	CreatorID             domain.MemberID
	PublicationOwner      store.EvidencePublicationOwner
	Trigger               store.EvidenceTrigger
	Objective             string
	IdempotencyKey        string
	RelatedRoomMessageIDs []string
	// SourceFacts are server-observed availability facts supplied by the
	// scheduler for sources not owned by this service, such as Run Room pages.
	SourceFacts     []store.EvidenceSourceFact
	UnresolvedFacts []string
	NextAction      string
	Provenance      string
}

type Service struct {
	store      Store
	git        GitEvidence
	transcript TranscriptExporter
	runs       RunLookup
	events     EventLookup
	root       string
	retention  time.Duration
	now        func() time.Time
	fs         FileSystem

	mu      sync.Mutex
	locks   map[domain.RunID]*sync.Mutex
	packets map[string]protocol.EvidencePacket
	cleaned map[string]struct{}
}

// New builds an evidence service and creates the private storage directory.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Git == nil || cfg.Runs == nil || cfg.EvidenceDir == "" {
		return nil, errors.New("evidence: Store, Git, Runs, and EvidenceDir are required")
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.FS == nil {
		cfg.FS = osFileSystem{}
	}
	if err := cfg.FS.MkdirAll(cfg.EvidenceDir, 0o700); err != nil {
		return nil, errors.New("evidence: create private evidence directory")
	}
	svc := &Service{
		store: cfg.Store, git: cfg.Git, transcript: cfg.Transcript, runs: cfg.Runs,
		events: cfg.Events, root: cfg.EvidenceDir, retention: cfg.Retention,
		now: cfg.Now, fs: cfg.FS, locks: make(map[domain.RunID]*sync.Mutex),
		packets: make(map[string]protocol.EvidencePacket),
		cleaned: make(map[string]struct{}),
	}
	if err := svc.reapStaging(context.Background(), svc.now().UTC()); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) runLock(run domain.RunID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[run]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[run] = lock
	}
	return lock
}

// Capture retains the run's Git tree and transcript before persisting a
// packet. The idempotency key is scoped to the run and creator by the store;
// its hash is also the private Git ref and transcript filename, so arbitrary
// client keys never become path components.
func (s *Service) Capture(ctx context.Context, req Request) (protocol.EvidencePacket, error) {
	if err := validateRequest(req); err != nil {
		return protocol.EvidencePacket{}, err
	}
	lock := s.runLock(req.RunID)
	lock.Lock()
	defer lock.Unlock()
	return s.captureLocked(ctx, req)
}

// CaptureBeforeCleanup serializes required evidence preservation with the
// caller's checkout cleanup. cleanup is called only after Git retention,
// transcript staging, and metadata persistence have all succeeded.
func (s *Service) CaptureBeforeCleanup(ctx context.Context, req Request, cleanup func(context.Context) error) (protocol.EvidencePacket, error) {
	if err := validateRequest(req); err != nil {
		return protocol.EvidencePacket{}, err
	}
	lock := s.runLock(req.RunID)
	lock.Lock()
	defer lock.Unlock()
	packet, err := s.captureLocked(ctx, req)
	if err != nil {
		return protocol.EvidencePacket{}, err
	}
	if cleanup != nil {
		cleanupCtx, cancel := s.cleanupContext(ctx)
		err := cleanup(cleanupCtx)
		cancel()
		if err != nil {
			return packet, fmt.Errorf("evidence: cleanup run %s: %w", req.RunID, err)
		}
	}
	return packet, nil
}
func validateRequest(req Request) error {
	if req.RunID == "" || req.IdempotencyKey == "" {
		return fmt.Errorf("%w: run id and idempotency key are required", ErrInvalidRequest)
	}
	if len(req.IdempotencyKey) > 512 {
		return fmt.Errorf("%w: idempotency key is too long", ErrInvalidRequest)
	}
	if req.Trigger != "" && !req.Trigger.Valid() {
		return fmt.Errorf("%w: invalid trigger %q", ErrInvalidRequest, req.Trigger)
	}
	if req.Origin.Kind != "" && !req.Origin.Valid() {
		return fmt.Errorf("%w: invalid evidence origin", ErrInvalidRequest)
	}
	if req.PublicationOwner != "" && !req.PublicationOwner.Valid() {
		return fmt.Errorf("%w: invalid publication owner", ErrInvalidRequest)
	}
	return nil
}

func (s *Service) captureLocked(ctx context.Context, req Request) (protocol.EvidencePacket, error) {
	if err := ctx.Err(); err != nil {
		return protocol.EvidencePacket{}, err
	}
	run, err := s.runs.GetRun(ctx, req.RunID)
	if err != nil {
		return protocol.EvidencePacket{}, fmt.Errorf("evidence: lookup run: %w", err)
	}
	if run == nil || run.ID == "" || run.ID != req.RunID {
		return protocol.EvidencePacket{}, fmt.Errorf("%w: run lookup returned no matching run", ErrInvalidRequest)
	}
	origin := req.Origin
	creator := req.CreatorID
	if origin.Kind == "" {
		if creator == "" {
			creator = run.MemberID
		}
		origin = store.EvidenceOrigin{Kind: store.EvidenceOriginHuman, ID: string(creator)}
	}
	if !origin.Valid() {
		return protocol.EvidencePacket{}, fmt.Errorf("%w: invalid evidence origin", ErrInvalidRequest)
	}
	if origin.Kind == store.EvidenceOriginHuman {
		creator = domain.MemberID(origin.ID)
	} else {
		creator = ""
	}
	owner := req.OwnerID
	if owner == "" {
		owner = run.MemberID
	}
	key := captureOriginKey(req.RunID, origin, req.IdempotencyKey)

	// The packet row is the idempotency authority. Check it before staging,
	// opening a transcript, or asking Git to inspect a checkout that a prior
	// attempt may already have cleaned up.
	existing, lookupErr := s.lookupPacketByOrigin(ctx, origin, creator, req.RunID, req.IdempotencyKey)
	if lookupErr == nil {
		if existing == nil {
			return protocol.EvidencePacket{}, errors.New("evidence: durable idempotency lookup returned an empty packet")
		}
		if existing.RunID != req.RunID || existing.Origin != origin ||
			existing.IdempotencyKey != req.IdempotencyKey {
			return protocol.EvidencePacket{}, errors.New("evidence: durable idempotency lookup returned a mismatched packet")
		}
		packet := safePacket(protocol.EvidencePacketFromStore(existing))
		s.mu.Lock()
		s.packets[key] = clonePacket(packet)
		s.mu.Unlock()
		return packet, nil
	}
	if !errors.Is(lookupErr, store.ErrNotFound) {
		return protocol.EvidencePacket{}, fmt.Errorf("evidence: lookup packet idempotency: %w", lookupErr)
	}
	now := s.now().UTC()
	journaled := s.stagingStore() != nil
	if stagingErr := s.beginStaging(ctx, run, origin, creator, key, req.IdempotencyKey, now); stagingErr != nil {
		return protocol.EvidencePacket{}, stagingErr
	}

	// Git is required: metadata claiming a retained revision is never written
	// unless the exact tree/commit has already been copied to the workspace's
	// private evidence ref.
	revision, err := s.git.CaptureEvidence(ctx, req.RunID, key)
	gitCreated := revision.RetainedRefCreated
	if err != nil {
		rollbackErr := s.rollbackCapture(ctx, run.WorkspaceID, key, gitCreated, false, journaled)
		if rollbackErr != nil {
			return protocol.EvidencePacket{}, fmt.Errorf("evidence: retain git state: %w (rollback: %v)", err, rollbackErr)
		}
		return protocol.EvidencePacket{}, fmt.Errorf("evidence: retain git state: %w", err)
	}
	if revision.WorkspaceID == "" || revision.Commit == "" || revision.Tree == "" {
		rollbackErr := s.rollbackCapture(ctx, run.WorkspaceID, key, gitCreated, false, journaled)
		if rollbackErr != nil {
			return protocol.EvidencePacket{}, fmt.Errorf("evidence: git retention returned incomplete revision (rollback: %v)", rollbackErr)
		}
		return protocol.EvidencePacket{}, errors.New("evidence: git retention returned incomplete revision")
	}
	if revision.WorkspaceID != run.WorkspaceID {
		rollbackErr := s.rollbackCapture(ctx, run.WorkspaceID, key, gitCreated, false, journaled)
		if rollbackErr != nil {
			return protocol.EvidencePacket{}, fmt.Errorf("evidence: retained revision workspace does not match run (rollback: %v)", rollbackErr)
		}
		return protocol.EvidencePacket{}, errors.New("evidence: retained revision workspace does not match run")
	}

	transcriptFact, transcriptUnresolved, transcriptStaged, err := s.retainTranscript(ctx, req.RunID, key)
	if err != nil {
		rollbackErr := s.rollbackCapture(ctx, run.WorkspaceID, key, gitCreated, transcriptStaged, journaled)
		if rollbackErr != nil {
			return protocol.EvidencePacket{}, fmt.Errorf("%w (rollback: %v)", err, rollbackErr)
		}
		return protocol.EvidencePacket{}, err
	}
	boundaryFact, boundary, boundaryUnresolved := s.eventBoundary(ctx)

	objective := req.Objective
	if objective == "" {
		objective = run.Task
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = store.EvidenceReport
	}
	publicationOwner := req.PublicationOwner
	if publicationOwner == "" {
		publicationOwner = store.EvidencePublicationOwnerGeneric
	}
	nextAction := req.NextAction
	if nextAction == "" {
		nextAction = "review retained evidence"
	}
	provenance := req.Provenance
	if provenance == "" {
		provenance = "server-observed"
	}
	unresolved := boundedFacts(req.UnresolvedFacts)
	unresolved = appendFacts(unresolved, transcriptUnresolved...)
	unresolved = appendFacts(unresolved, boundaryUnresolved...)
	changedFilesSource := store.EvidenceSourceFact{Name: "changed_files", Available: true}
	if revision.ChangedFilesTruncated {
		changedFilesSource.Truncated = true
		changedFilesSource.Reason = "changed-file metadata truncated"
	}
	sources := make([]store.EvidenceSourceFact, 0, minInt(MaxSourceFacts, 4+len(req.SourceFacts)))
	sources = appendSourceFacts(sources, store.EvidenceSourceFact{Name: "git", Available: true})
	sources = appendSourceFacts(sources, changedFilesSource)
	sources = appendSourceFacts(sources, req.SourceFacts...)
	sources = appendSourceFacts(sources, transcriptFact, boundaryFact)
	p := &store.EvidencePacket{
		ID: key, WorkspaceID: run.WorkspaceID, RunID: req.RunID, Origin: origin, OwnerID: owner, CreatorID: creator,
		PublicationOwner: publicationOwner,
		Availability:     store.EvidenceAvailable, Trigger: trigger, Objective: cleanText(objective, MaxObjectiveBytes), CapturedAt: now,
		ExpiresAt: new(now.Add(s.retention)), EventBoundary: boundary,
		BaseRevision: cleanObjectID(revision.BaseCommit), RetainedRevision: cleanObjectID(revision.Commit),
		ChangedFiles: changedFacts(revision.ChangedFiles), Sources: sources,
		RelatedRoomMessageIDs: boundedIDs(req.RelatedRoomMessageIDs), UnresolvedFacts: unresolved,
		NextAction: cleanText(nextAction, MaxNextActionBytes), Provenance: cleanText(provenance, MaxProvenanceBytes),
		IdempotencyKey: req.IdempotencyKey, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateEvidencePacket(ctx, p); err != nil {
		rollbackErr := s.rollbackCapture(ctx, run.WorkspaceID, key, gitCreated, transcriptStaged, journaled)
		if rollbackErr != nil {
			return protocol.EvidencePacket{}, fmt.Errorf("evidence: persist packet: %w (rollback: %v)", err, rollbackErr)
		}
		return protocol.EvidencePacket{}, fmt.Errorf("evidence: persist packet: %w", err)
	}
	if p.ID == "" {
		p.ID = key
	}
	if journaled {
		cleanupCtx, cancel := s.cleanupContext(ctx)
		clearErr := s.clearStaging(cleanupCtx, key)
		cancel()
		if clearErr != nil {
			return protocol.EvidencePacket{}, clearErr
		}
	}
	packet := safePacket(protocol.EvidencePacketFromStore(p))
	s.mu.Lock()
	s.packets[key] = clonePacket(packet)
	s.mu.Unlock()
	return packet, nil
}

func (s *Service) rollbackCapture(ctx context.Context, workspace domain.WorkspaceID, key string, gitCreated, transcriptStaged, journaled bool) error {
	err := s.rollbackArtifacts(ctx, workspace, key, gitCreated, transcriptStaged)
	if err == nil && journaled {
		cleanupCtx, cancel := s.cleanupContext(ctx)
		err = s.clearStaging(cleanupCtx, key)
		cancel()
	}
	return err
}

func captureKey(run domain.RunID, creator domain.MemberID, idempotency string) string {
	return captureOriginKey(run, store.EvidenceOrigin{Kind: store.EvidenceOriginHuman, ID: string(creator)}, idempotency)
}

func captureOriginKey(run domain.RunID, origin store.EvidenceOrigin, idempotency string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, string(run))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, string(origin.Kind))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, origin.ID)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, idempotency)
	return "evidence-" + hex.EncodeToString(h.Sum(nil))
}

func (s *Service) lookupPacketByOrigin(ctx context.Context, origin store.EvidenceOrigin, creator domain.MemberID, run domain.RunID, key string) (*store.EvidencePacket, error) {
	if origin.Valid() {
		if lookup, ok := s.store.(store.EvidenceOriginLookup); ok {
			return lookup.GetEvidencePacketByOrigin(ctx, origin, run, key)
		}
	}
	return s.store.GetEvidencePacketByIdempotency(ctx, creator, run, key)
}

func packetCaptureKey(p *store.EvidencePacket) string {
	if p == nil {
		return ""
	}
	if p.Origin.Valid() {
		return captureOriginKey(p.RunID, p.Origin, p.IdempotencyKey)
	}
	return captureKey(p.RunID, p.CreatorID, p.IdempotencyKey)
}

func (s *Service) stagingStore() StagingStore {
	staging, _ := s.store.(StagingStore)
	return staging
}

func (s *Service) beginStaging(ctx context.Context, run *domain.Run, origin store.EvidenceOrigin, creator domain.MemberID, key, idempotencyKey string, now time.Time) error {
	staging := s.stagingStore()
	if staging == nil {
		return nil
	}
	if err := staging.CreateEvidenceStaging(ctx, &store.EvidenceStaging{
		ID: key, WorkspaceID: run.WorkspaceID, RunID: run.ID, Origin: origin, CreatorID: creator,
		IdempotencyKey: idempotencyKey, CreatedAt: now, ExpiresAt: now.Add(s.retention),
	}); err != nil {
		return fmt.Errorf("evidence: journal staging: %w", err)
	}
	return nil
}

func (s *Service) clearStaging(ctx context.Context, key string) error {
	staging := s.stagingStore()
	if staging == nil {
		return nil
	}
	if err := staging.DeleteEvidenceStaging(ctx, key); err != nil {
		return fmt.Errorf("evidence: clear staging journal: %w", err)
	}
	return nil
}

func (s *Service) reapStaging(ctx context.Context, before time.Time) error {
	staging := s.stagingStore()
	if staging == nil {
		return nil
	}
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	rows, err := staging.ListEvidenceStaging(cleanupCtx, before, MaxPageSize)
	if err != nil {
		return fmt.Errorf("evidence: list staging journal: %w", err)
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		// Capture holds this lock from the initial packet recheck through
		// staging, retention, and metadata commit. Reap must hold the same
		// lock before rechecking metadata or deleting the staged artifacts.
		lock := s.runLock(row.RunID)
		lock.Lock()
		packet, packetErr := s.lookupPacketByOrigin(cleanupCtx, row.Origin, row.CreatorID, row.RunID, row.IdempotencyKey)
		if packetErr == nil && packet != nil {
			if err := staging.DeleteEvidenceStaging(cleanupCtx, row.ID); err != nil {
				lock.Unlock()
				return fmt.Errorf("evidence: clear committed staging row %s: %w", row.ID, err)
			}
			lock.Unlock()
			continue
		}
		if packetErr != nil && !errors.Is(packetErr, store.ErrNotFound) {
			lock.Unlock()
			return fmt.Errorf("evidence: inspect staging packet: %w", packetErr)
		}
		if err := s.removeStagedArtifacts(cleanupCtx, row.WorkspaceID, row.ID); err != nil {
			lock.Unlock()
			return fmt.Errorf("evidence: reap staging row %s: %w", row.ID, err)
		}
		if err := staging.DeleteEvidenceStaging(cleanupCtx, row.ID); err != nil {
			lock.Unlock()
			return fmt.Errorf("evidence: clear staging row %s: %w", row.ID, err)
		}
		lock.Unlock()
	}
	return nil
}

func (s *Service) removeStagedArtifacts(ctx context.Context, workspace domain.WorkspaceID, key string) error {
	path, err := s.artifactPath(key)
	if err != nil {
		return err
	}
	if removeErr := s.fs.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("evidence: reap transcript: %w", removeErr)
	}
	marker, err := s.truncationMarkerPath(key)
	if err != nil {
		return err
	}
	if err := s.fs.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("evidence: reap transcript state: %w", err)
	}
	if err := s.git.RemoveEvidence(ctx, workspace, key); err != nil {
		return fmt.Errorf("evidence: reap git evidence: %w", err)
	}
	return nil
}

func (s *Service) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
}
func (s *Service) artifactPath(id string) (string, error) {
	const prefix = "evidence-"
	if s.root == "" || len(id) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(id, prefix) {
		return "", errors.New("evidence: invalid transcript artifact")
	}
	for i := len(prefix); i < len(id); i++ {

		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errors.New("evidence: invalid transcript artifact")
		}
	}
	root := filepath.Clean(s.root)
	path := filepath.Join(root, id+".transcript")
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("evidence: invalid transcript artifact")
	}
	return path, nil
}

func changedFacts(files []events.FileDiffStat) []store.ChangedFileFact {
	limit := minInt(len(files), MaxChangedFiles)
	out := make([]store.ChangedFileFact, 0, limit)
	for _, file := range files[:limit] {
		out = append(out, store.ChangedFileFact{
			Path: cleanRepoPath(file.Path), Status: "changed",
			Additions: nonNegative(file.Additions), Deletions: nonNegative(file.Deletions),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (s *Service) eventBoundary(ctx context.Context) (store.EvidenceSourceFact, uint64, []string) {
	if s.events == nil {
		return store.EvidenceSourceFact{Name: "event_log", Available: false, Reason: "event boundary unavailable"}, 0, []string{"event boundary unavailable"}
	}
	boundary, err := s.events.LastSeq(ctx)
	if err != nil {
		return store.EvidenceSourceFact{Name: "event_log", Available: false, Reason: "event boundary unavailable"}, 0, []string{"event boundary unavailable"}
	}
	return store.EvidenceSourceFact{Name: "event_log", Available: true}, boundary, nil
}

func (s *Service) retainTranscript(ctx context.Context, run domain.RunID, key string) (store.EvidenceSourceFact, []string, bool, error) {
	path, err := s.artifactPath(key)
	if err != nil {
		return store.EvidenceSourceFact{}, nil, false, err
	}
	markerPath, err := s.truncationMarkerPath(key)
	if err != nil {
		return store.EvidenceSourceFact{}, nil, false, err
	}
	// A retry after restart reuses only a transcript with an explicit,
	// atomically-written complete/truncated marker. Size alone cannot tell
	// whether an exact 16 MiB source ended cleanly at the boundary.
	if info, statErr := s.fs.Stat(path); statErr == nil && info.Mode().IsRegular() {
		truncated, known := s.readTruncationMarker(markerPath)
		if known {
			fact := store.EvidenceSourceFact{Name: "transcript", Available: true, Truncated: truncated}
			if truncated {
				fact.Reason = "transcript capped at 16 MiB"
			}
			return fact, nil, false, nil
		}
	}
	if s.transcript == nil {
		return store.EvidenceSourceFact{Name: "transcript", Available: false, Reason: "transcript exporter unavailable"}, []string{"transcript unavailable: exporter unavailable"}, false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		reason := safeReason(ctxErr)
		return store.EvidenceSourceFact{Name: "transcript", Available: false, Reason: reason}, []string{"transcript unavailable: " + reason}, false, nil
	}
	reader, err := s.transcript.Replay(run)
	if err != nil || reader == nil {
		reason := "transcript unavailable"
		if err != nil {
			reason = safeReason(err)
		}
		return store.EvidenceSourceFact{Name: "transcript", Available: false, Reason: reason}, []string{"transcript unavailable: " + reason}, false, nil
	}
	defer func() { _ = reader.Close() }()
	tmp, err := s.fs.CreateTemp(s.root, ".transcript-*")
	if err != nil {
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: stage transcript")
	}
	tmpName := tempName(tmp)
	if tmpName == "" {
		_ = tmp.Close()
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: transcript temporary file has no name")
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = s.fs.Remove(tmpName)
		}
	}()
	truncated, sourceErr, writeErr := copyBounded(reader, tmp, MaxTranscriptBytes)
	if writeErr != nil {
		_ = tmp.Close()
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: write transcript")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: sync transcript")
	}
	if err := tmp.Close(); err != nil {
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: close transcript")
	}
	if sourceErr != nil {
		return store.EvidenceSourceFact{Name: "transcript", Available: false, Reason: "transcript read unavailable"}, []string{"transcript unavailable: read error"}, false, nil
	}
	if err := s.writeTruncationMarker(markerPath, truncated); err != nil {
		return store.EvidenceSourceFact{}, nil, false, err
	}
	if err := s.fs.Rename(tmpName, path); err != nil {
		return store.EvidenceSourceFact{}, nil, false, errors.New("evidence: retain transcript")
	}
	removeTemp = false
	if err := s.fs.SyncDir(s.root); err != nil {
		return store.EvidenceSourceFact{}, nil, true, fmt.Errorf("evidence: sync transcript directory: %w", err)
	}
	fact := store.EvidenceSourceFact{Name: "transcript", Available: true, Truncated: truncated}
	if truncated {
		fact.Reason = "transcript capped at 16 MiB"
	}
	return fact, nil, true, nil
}

func (s *Service) truncationMarkerPath(key string) (string, error) {
	path, err := s.artifactPath(key)
	if err != nil {
		return "", err
	}
	return path + ".meta", nil
}

func (s *Service) readTruncationMarker(path string) (bool, bool) {
	f, err := s.fs.Open(path)
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 32))
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(b)) {
	case "complete":
		return false, true
	case "truncated":
		return true, true
	default:
		return false, false
	}
}

func (s *Service) writeTruncationMarker(path string, truncated bool) error {
	tmp, err := s.fs.CreateTemp(s.root, ".transcript-meta-*")
	if err != nil {
		return errors.New("evidence: stage transcript marker")
	}
	tmpName := tempName(tmp)
	if tmpName == "" {
		_ = tmp.Close()
		return errors.New("evidence: transcript marker temporary file has no name")
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = s.fs.Remove(tmpName)
		}
	}()
	state := "complete\n"
	if truncated {
		state = "truncated\n"
	}
	if _, err := io.WriteString(tmp, state); err != nil {
		_ = tmp.Close()
		return errors.New("evidence: write transcript marker")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.New("evidence: sync transcript marker")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("evidence: close transcript marker")
	}
	if err := s.fs.Rename(tmpName, path); err != nil {
		return errors.New("evidence: retain transcript marker")
	}
	removeTemp = false
	return nil
}
func (s *Service) rollbackArtifacts(ctx context.Context, workspace domain.WorkspaceID, key string, gitCreated, transcriptStaged bool) error {
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	var errs []error
	if transcriptStaged {
		if path, err := s.artifactPath(key); err != nil {
			errs = append(errs, err)
		} else if err := s.fs.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove transcript: %w", err))
		}
		if marker, err := s.truncationMarkerPath(key); err != nil {
			errs = append(errs, err)
		} else if err := s.fs.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove transcript marker: %w", err))
		}
	}
	if gitCreated {
		if err := s.git.RemoveEvidence(cleanupCtx, workspace, key); err != nil {
			errs = append(errs, fmt.Errorf("remove git evidence: %w", err))
		}
	}
	return errors.Join(errs...)
}

func tempName(file File) string {
	if f, ok := file.(namedFile); ok {
		return f.Name()
	}
	return ""
}
