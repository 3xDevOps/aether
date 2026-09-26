package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

var (
	ErrInvalidRequest = errors.New("integration: invalid request")
	ErrUnauthorized   = errors.New("integration: unauthorized")
	ErrUnavailable    = errors.New("integration: unavailable")
	ErrExpired        = errors.New("integration: expired")
	ErrConflict       = errors.New("integration: conflict")
)

// Actor is supplied by the authenticated adapter. It is never decoded from a wire request.
type Actor struct {
	MemberID domain.MemberID
	RunID    domain.RunID
}

type Admission struct {
	Operation   string
	Actor       Actor
	WorkspaceID domain.WorkspaceID
	MissionID   string
	Candidate   *protocol.Candidate
	Submissions []protocol.SubmissionRef
	// NewCandidate is true only for Prepare before the first durable row.
	NewCandidate bool
}
type AdmissionFunc func(context.Context, Admission) (release func(), err error)

type EvidenceSource interface {
	WithCandidateSource(context.Context, domain.WorkspaceID, string, func(*store.EvidencePacket, string, io.ReadCloser) error) error
}

type Store interface {
	GetMember(context.Context, domain.MemberID) (*domain.Member, error)
	GetWorkspace(context.Context, domain.WorkspaceID) (*domain.Workspace, error)
	GetRun(context.Context, domain.RunID) (*domain.Run, error)
	CreateIntegrationCandidate(context.Context, *store.IntegrationCandidate) error
	GetIntegrationCandidate(context.Context, string) (*store.IntegrationCandidate, error)
	GetIntegrationCandidateByKey(context.Context, domain.WorkspaceID, string, string) (*store.IntegrationCandidate, error)
	UpdateIntegrationCandidate(context.Context, *store.IntegrationCandidate, int64) error
	ListIntegrationCandidates(context.Context, domain.WorkspaceID, int) ([]*store.IntegrationCandidateSummary, error)
	ListIntegrationCleanupCandidatesAfter(context.Context, time.Time, string, int) ([]*store.IntegrationCandidate, error)
	ListIntegrationCandidateIDs(context.Context, domain.WorkspaceID) ([]string, error)
	DeleteIntegrationCandidate(context.Context, string, int64) error
}

type Git interface {
	RetainCandidateInput(context.Context, domain.WorkspaceID, string, int, string, string, string) error
	CandidateCheckout(context.Context, domain.WorkspaceID, string, string) (string, error)
	AssembleCandidate(context.Context, domain.WorkspaceID, string, []gitengine.CandidateRevisionInput) (gitengine.CandidateAssembly, error)
	ResolveCandidate(context.Context, domain.WorkspaceID, string, []gitengine.CandidateResolution) (gitengine.CandidateAssembly, error)
	CheckCandidate(context.Context, domain.WorkspaceID, string, string) error
	CandidateVerificationCheckout(context.Context, domain.WorkspaceID, string, string, string) (string, error)
	CheckCandidateVerification(context.Context, domain.WorkspaceID, string, string, string) error
	RemoveCandidateVerification(context.Context, domain.WorkspaceID, string, string) error
	DeliverCandidate(context.Context, domain.WorkspaceID, string, string, string, string, string, string) (gitengine.CandidateGitReceipt, error)
	LookupCandidateDelivery(context.Context, domain.WorkspaceID, string, string, string, string, string, string) (gitengine.CandidateGitReceipt, bool, error)
	RenderCandidatePatch(context.Context, domain.WorkspaceID, string, string, int) (gitengine.Patch, error)
	RemoveCandidate(context.Context, domain.WorkspaceID, string) error
}

type Config struct {
	Store       Store
	Git         Git
	Evidence    EvidenceSource
	Runtime     runtime.Runtime
	Root        string
	Environment func(context.Context, Actor, *domain.Workspace, string) (runtime.Spec, error)
	// PrepareRuntime stages any auxiliary runtime resources before Create.
	// The hook receives the persisted verification creation key in Spec.
	PrepareRuntime func(context.Context, *runtime.Spec) error
	// ReleaseRuntime removes resources staged by PrepareRuntime. It is called
	// only after the runtime container is proven absent.
	ReleaseRuntime func(context.Context, string) error
	Admission      AdmissionFunc
	Now            func() time.Time
}
type Service struct {
	store             Store
	git               Git
	evidence          EvidenceSource
	runtime           runtime.Runtime
	root              string
	environment       func(context.Context, Actor, *domain.Workspace, string) (runtime.Spec, error)
	prepareRuntime    func(context.Context, *runtime.Spec) error
	releaseRuntime    func(context.Context, string) error
	admission         AdmissionFunc
	now               func() time.Time
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	life              sync.Mutex
	closed            bool
	active            map[string]context.CancelFunc
	locks             sync.Map
	workspaceDeleteMu sync.RWMutex
	asyncMu           sync.Mutex
	asyncErr          error
}

func (s *Service) recordAsyncError(err error) {
	if err == nil {
		return
	}
	s.asyncMu.Lock()
	if s.asyncErr == nil {
		s.asyncErr = err
	}
	s.asyncMu.Unlock()
}

func (s *Service) asynchronousError() error {
	s.asyncMu.Lock()
	defer s.asyncMu.Unlock()
	return s.asyncErr
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Git == nil || cfg.Evidence == nil || cfg.Runtime == nil || cfg.Environment == nil || strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("%w: store, git, evidence, runtime, environment, and root are required", ErrInvalidRequest)
	}
	if (cfg.PrepareRuntime == nil) != (cfg.ReleaseRuntime == nil) {
		return nil, fmt.Errorf("%w: prepare and release runtime hooks must be configured together", ErrInvalidRequest)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{store: cfg.Store, git: cfg.Git, evidence: cfg.Evidence, runtime: cfg.Runtime, root: cfg.Root,
		environment: cfg.Environment, prepareRuntime: cfg.PrepareRuntime, releaseRuntime: cfg.ReleaseRuntime,
		admission: cfg.Admission, now: now, ctx: ctx, cancel: cancel, active: make(map[string]context.CancelFunc)}, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.life.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		for _, cancel := range s.active {
			if cancel != nil {
				cancel()
			}
		}
	}
	s.life.Unlock()
	s.wg.Wait()
	return s.asynchronousError()
}

func (s *Service) registerVerification(id string, cancel context.CancelFunc) bool {
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed || id == "" {
		return false
	}
	if s.active == nil {
		s.active = make(map[string]context.CancelFunc)
	}
	s.active[id] = cancel
	s.wg.Add(1)
	return true
}
func (s *Service) unregisterVerification(id string) {
	s.life.Lock()
	delete(s.active, id)
	s.life.Unlock()
	s.wg.Done()
}
func (s *Service) verificationActive(id string) bool {
	s.life.Lock()
	defer s.life.Unlock()
	_, ok := s.active[id]
	return ok
}
func (s *Service) cancelVerification(id string) {
	s.life.Lock()
	cancel := s.active[id]
	s.life.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *Service) nowTime() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}
func (s *Service) lock(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func candidateActive(c *protocol.Candidate, active func(string) bool) bool {
	if c == nil {
		return false
	}
	for _, v := range c.Verifications {
		if v.Status == protocol.VerificationRunning && active(v.VerificationID) {
			return true
		}
	}
	return false
}
func (s *Service) cancelCandidate(c *protocol.Candidate) {
	if c == nil {
		return
	}
	for _, v := range c.Verifications {
		if v.Status == protocol.VerificationRunning {
			s.cancelVerification(v.VerificationID)
		}
	}
}

func defaultAdmission(_ context.Context, a Admission) (func(), error) {
	if a.NewCandidate && a.Candidate == nil {
		return nil, fmt.Errorf("%w: new candidate admission requires candidate", ErrInvalidRequest)
	}
	if a.Actor.RunID != "" || a.MissionID != "" {
		return nil, ErrUnauthorized
	}
	return func() {}, nil
}

func (s *Service) admit(ctx context.Context, a Admission) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.NewCandidate && a.Candidate == nil {
		return nil, fmt.Errorf("%w: new candidate admission requires candidate", ErrInvalidRequest)
	}
	if s.admission == nil {
		return defaultAdmission(ctx, a)
	}
	release, err := s.admission(ctx, a)
	if err != nil {
		return nil, err
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

func (s *Service) authorize(ctx context.Context, actor Actor, c *protocol.Candidate, operation string) (func(), error) {
	if c == nil || c.WorkspaceID == "" {
		return nil, fmt.Errorf("%w: candidate", ErrInvalidRequest)
	}
	return s.authorizeWorkspace(ctx, actor, domain.WorkspaceID(c.WorkspaceID), operation, c.MissionID, c, c.Submissions)
}

func (s *Service) authorizeWorkspace(ctx context.Context, actor Actor, wsID domain.WorkspaceID, operation, missionID string, c *protocol.Candidate, submissions []protocol.SubmissionRef, newCandidate ...bool) (func(), error) {
	if actor.MemberID == "" && actor.RunID == "" {
		return nil, ErrUnauthorized
	}
	ws, err := s.store.GetWorkspace(ctx, wsID)
	if err != nil || ws == nil {
		if err == nil {
			err = store.ErrNotFound
		}
		return nil, err
	}
	effective := actor
	var target *domain.Run
	if actor.RunID != "" {
		run, e := s.store.GetRun(ctx, actor.RunID)
		if e != nil || run == nil {
			if e == nil {
				e = store.ErrNotFound
			}
			return nil, e
		}
		if run.WorkspaceID != wsID || (actor.MemberID != "" && actor.MemberID != run.MemberID) {
			return nil, ErrUnauthorized
		}
		effective.MemberID = run.MemberID
		target = run
	}
	m, err := s.store.GetMember(ctx, effective.MemberID)
	if err != nil || m == nil {
		if err == nil {
			err = store.ErrNotFound
		}
		return nil, err
	}
	if m.Pending {
		return nil, ErrUnauthorized
	}
	cap := permissions.Push
	if operation == protocol.MethodIntegrationShow || operation == protocol.MethodIntegrationList || operation == protocol.MethodIntegrationPatch {
		cap = permissions.View
	}
	pt := permissions.Target{Workspace: wsID, SteerOthers: ws.SteerOthers}
	if target != nil {
		pt.Owner, pt.Protected = target.MemberID, target.Protected
	}
	if err := permissions.Check(cap, permissions.Actor{ID: effective.MemberID, Role: m.Role}, pt); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	subs := append([]protocol.SubmissionRef(nil), submissions...)
	isNew := len(newCandidate) > 0 && newCandidate[0]
	return s.admit(ctx, Admission{Operation: operation, Actor: effective, WorkspaceID: wsID, MissionID: missionID, Candidate: c, Submissions: subs, NewCandidate: isNew})
}

func (s *Service) validateOwned(ctx context.Context, c *protocol.Candidate) error {
	if c == nil {
		return ErrInvalidRequest
	}
	now := s.nowTime()
	if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
		return ErrExpired
	}
	switch c.State {
	case protocol.CandidateDeleting, protocol.CandidateExpired:
		return ErrExpired
	case protocol.CandidateUnavailable:
		return ErrUnavailable
	}
	if s.git == nil {
		return fmt.Errorf("%w: git ownership unavailable", ErrUnavailable)
	}
	if c.State == protocol.CandidateFrozen {
		if c.CandidateRevision == "" {
			return fmt.Errorf("%w: missing frozen revision", ErrUnavailable)
		}
		if err := s.git.CheckCandidate(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID, c.CandidateRevision); err != nil {
			return fmt.Errorf("%w: candidate source: %v", ErrUnavailable, err)
		}
	}
	if s.root == "" && len(c.Inputs) > 0 {
		return fmt.Errorf("%w: artifact root unavailable", ErrUnavailable)
	}
	for i, in := range c.Inputs {
		if in.TranscriptID == "" {
			continue
		}
		path := filepath.Join(s.root, "candidates", c.CandidateID, in.TranscriptID)
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("%w: transcript %d: %v", ErrUnavailable, i, err)
		}
		sum, e := checksumReader(f)
		_ = f.Close()
		if e != nil || (in.TranscriptChecksum != "" && sum != in.TranscriptChecksum) {
			return fmt.Errorf("%w: transcript %d checksum", ErrUnavailable, i)
		}
	}
	return nil
}

func (s *Service) load(ctx context.Context, workspaceID, candidateID string) (*store.IntegrationCandidate, *protocol.Candidate, error) {
	if workspaceID == "" || candidateID == "" {
		return nil, nil, fmt.Errorf("%w: workspace and candidate are required", ErrInvalidRequest)
	}
	r, err := s.store.GetIntegrationCandidate(ctx, candidateID)
	if err != nil {
		return nil, nil, err
	}
	if r == nil || r.WorkspaceID != domain.WorkspaceID(workspaceID) {
		return nil, nil, store.ErrNotFound
	}
	var c protocol.Candidate
	if err := json.Unmarshal(r.Payload, &c); err != nil {
		return nil, nil, fmt.Errorf("integration: decode candidate: %w", err)
	}
	c.Version = r.Version
	return r, &c, nil
}

func (s *Service) save(ctx context.Context, r *store.IntegrationCandidate, c *protocol.Candidate) error {
	if r == nil || c == nil {
		return ErrInvalidRequest
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(payload) > protocol.IntegrationMaxCandidatePayloadBytes {
		return fmt.Errorf("%w: aggregate payload exceeds bound", ErrInvalidRequest)
	}
	expected := r.Version
	r.Payload = payload
	r.Version = expected + 1
	r.State = string(c.State)
	r.ExpiresAt = c.ExpiresAt
	c.Version = r.Version
	return s.store.UpdateIntegrationCandidate(ctx, r, expected)
}
func mutationFor(c *protocol.Candidate, actor Actor, operation, key, dig string) (protocol.CandidateMutation, bool, error) {
	if c == nil {
		return protocol.CandidateMutation{}, false, ErrInvalidRequest
	}
	akey := actorKey(actor)
	for _, m := range c.Mutations {
		if m.ActorKey == akey && m.Operation == operation && m.IdempotencyKey == key {
			if m.Digest != dig {
				return protocol.CandidateMutation{}, false, ErrConflict
			}
			return m, true, nil
		}
	}
	return protocol.CandidateMutation{}, false, nil
}

func appendMutation(c *protocol.Candidate, actor Actor, operation, key, dig, resultID string) error {
	if c == nil || key == "" {
		return ErrInvalidRequest
	}
	if _, found, err := mutationFor(c, actor, operation, key, dig); err != nil {
		return err
	} else if found {
		return nil
	}
	if len(c.Mutations) >= protocol.IntegrationMaxCandidateMutations {
		return fmt.Errorf("%w: mutation history limit reached", ErrInvalidRequest)
	}
	c.Mutations = append(c.Mutations, protocol.CandidateMutation{ActorKey: actorKey(actor), Operation: operation, IdempotencyKey: key, Digest: dig, ResultID: resultID})
	return nil
}

func actorKey(a Actor) string {
	if a.RunID != "" {
		return "run:" + string(a.RunID)
	}
	return "member:" + string(a.MemberID)
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func cleanID(v string) bool {
	if v == "" || len(v) > 256 {
		return false
	}
	for _, r := range v {
		if r == '/' || r == '\\' || r == 0 {
			return false
		}
	}
	return true
}
func validRef(v string) bool {
	return v != "" && strings.HasPrefix(v, "refs/heads/") && !strings.Contains(v, "..") && !strings.ContainsAny(v, "\\\x00")
}
func checksumReader(r io.Reader) (string, error) {
	h := sha256.New()
	n, err := io.CopyN(h, r, evidence.MaxTranscriptBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n > evidence.MaxTranscriptBytes {
		return "", fmt.Errorf("%w: transcript exceeds limit", ErrInvalidRequest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func ensureDir(path string) error { return os.MkdirAll(path, 0o700) }
func candidateArtifactPath(root, id string, index int) string {
	return filepath.Join(root, "candidates", id, fmt.Sprintf("%d.transcript", index))
}
