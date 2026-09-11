// Package mirror owns server-side upstream mirror configuration and the
// deploy-key files used to fetch it. Private key bytes never cross this API.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/store"
)

// Store is the intentionally narrow persistence contract needed by Service.
// Workspace and member homes are not consulted by mirror operations.
type Store interface {
	GetWorkspaceMirror(context.Context, domain.WorkspaceID) (*domain.WorkspaceMirror, error)
	SetWorkspaceMirror(context.Context, *domain.WorkspaceMirror) error
	DeleteWorkspaceMirror(context.Context, domain.WorkspaceID) error
}

type Git interface {
	ConfigureWorkspaceMirror(context.Context, domain.WorkspaceID, gitengine.MirrorRequest) (gitengine.MirrorResult, error)
	RefreshWorkspaceMirror(context.Context, domain.WorkspaceID, gitengine.MirrorRequest) (gitengine.MirrorResult, error)
	AdoptWorkspaceMirror(context.Context, domain.WorkspaceID, int64) (gitengine.MirrorResult, error)
	DisableWorkspaceMirror(context.Context, domain.WorkspaceID) error
	WorkspaceBranchCommit(context.Context, domain.WorkspaceID, string) (string, error)
}
type mirrorGenerationReader interface {
	MirrorGeneration(context.Context, domain.WorkspaceID) (int64, error)
}

// workspaceMirrorLister is implemented by stores that can enumerate persisted
// mirror rows during startup recovery. It remains optional so narrow test and
// in-memory stores need not expose unrelated workspace data.
type workspaceMirrorLister interface {
	ListWorkspaceMirrors(context.Context) ([]domain.WorkspaceMirror, error)
}

const mirrorRollbackTimeout = 5 * time.Second

// Config wires the service.
type Config struct {
	Root  string
	Store Store
	Git   Git
	Now   func() time.Time
}

// Service serializes all operations for one workspace while allowing
// unrelated workspaces to proceed concurrently.
type Service struct {
	root  string
	store Store
	git   Git
	now   func() time.Time

	locksMu sync.Mutex
	locks   map[domain.WorkspaceID]*sync.Mutex
}

// ConfigureRequest is the administrator-supplied mirror source.
type ConfigureRequest struct {
	SourceURL  string
	Branch     string
	Auth       domain.MirrorAuth
	KnownHosts string
}

// Result is the public mirror state returned by lifecycle methods.
type Result struct {
	Mirror    domain.WorkspaceMirror
	PublicKey string
	Warning   string
}

// CaptureResult is immutable base provenance suitable for pinning to a run.
type CaptureResult struct {
	WorkspaceID domain.WorkspaceID
	Commit      string
	Branch      string
	Source      string
	CheckedAt   time.Time
	Configured  bool
	Cached      bool
}

// New constructs a Service and creates its private parent directory with
// restrictive permissions. It accepts only the Config form so the store and
// git seams remain explicit and testable.
func New(cfg Config) (*Service, error) {
	root := cfg.Root
	if root == "" || cfg.Store == nil || cfg.Git == nil {
		return nil, errors.New("mirror: Root, Store, and Git are required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: create root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: protect root: %w", err)
	}
	if err := sweepKeyQuarantines(root); err != nil {
		return nil, err
	}
	svc := &Service{root: root, store: cfg.Store, git: cfg.Git, now: cfg.Now, locks: make(map[domain.WorkspaceID]*sync.Mutex)}
	if lister, ok := cfg.Store.(workspaceMirrorLister); ok {
		if err := svc.reconcileDisablingRows(lister); err != nil {
			return nil, err
		}
	}
	return svc, nil
}

func (s *Service) workspaceLock(id domain.WorkspaceID) func() {
	s.locksMu.Lock()
	lock := s.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[id] = lock
	}
	s.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}
func (s *Service) reconcileDisablingRows(lister workspaceMirrorLister) error {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorRollbackTimeout)
	defer cancel()
	rows, err := lister.ListWorkspaceMirrors(ctx)
	if err != nil {
		return fmt.Errorf("mirror: list disabling rows: %w", err)
	}
	var reconcileErr error
	for _, row := range rows {
		if row.Status != domain.MirrorStatusDisabling {
			continue
		}
		if err := s.reconcileDisabling(ctx, row.WorkspaceID); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("workspace %q: %w", row.WorkspaceID, err))
		}
	}
	if reconcileErr != nil {
		return fmt.Errorf("mirror: reconcile disabling rows: %w", reconcileErr)
	}
	return nil
}

func (s *Service) reconcileDisabling(ctx context.Context, workspace domain.WorkspaceID) error {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	row, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status != domain.MirrorStatusDisabling {
		return nil
	}
	lifecycleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
	defer cancel()
	return s.finishDisabling(lifecycleCtx, *row)
}

func (s *Service) finishDisabling(ctx context.Context, row domain.WorkspaceMirror) error {
	if err := s.git.DisableWorkspaceMirror(ctx, row.WorkspaceID); err != nil {
		return serviceGitError(row.WorkspaceID, err)
	}
	if err := retireWorkspaceSecrets(s.root, string(row.WorkspaceID)); err != nil {
		return fmt.Errorf("retire workspace mirror keys failed: %w", err)
	}
	if err := s.store.DeleteWorkspaceMirror(ctx, row.WorkspaceID); err != nil {
		return fmt.Errorf("delete workspace mirror failed: %w", err)
	}
	return nil
}

func (s *Service) Configure(ctx context.Context, workspace domain.WorkspaceID, req ConfigureRequest) (Result, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()

	source, err := CanonicalizeSource(req.SourceURL, req.Auth, req.KnownHosts)
	if err != nil {
		return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace, Cause: err}
	}
	if !domain.ValidMirrorBranch(req.Branch) || strings.HasPrefix(req.Branch, "-") {
		return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace, Cause: errors.New("invalid mirror branch")}
	}
	old, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Result{}, err
	}
	if errors.Is(err, store.ErrNotFound) {
		old = nil
	}
	if old != nil && old.Status == domain.MirrorStatusDisabling {
		return s.result(*old, s.publicKey(workspace, *old)), disablingMirrorError(workspace)
	}
	generation := int64(1)
	if old != nil {
		if old.Generation == int64(^uint64(0)>>1) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace}
		}
		generation = old.Generation + 1
	}
	if reader, ok := s.git.(mirrorGenerationReader); ok {
		durable, readErr := reader.MirrorGeneration(ctx, workspace)
		if readErr != nil {
			return Result{}, readErr
		}
		if durable < 0 || durable == int64(^uint64(0)>>1) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace, Cause: errors.New("invalid durable mirror generation")}
		}
		if durable >= generation {
			generation = durable + 1
		}
	}

	var material keyMaterial
	if req.Auth == domain.MirrorAuthDeployKey {
		material, err = generateKeyMaterial(s.root, string(workspace), generation, source.KnownHosts)
		if err != nil {
			return Result{}, err
		}
	}
	request := gitengine.MirrorRequest{SourceURL: source.URL, Branch: req.Branch, Generation: generation, Auth: req.Auth}
	if req.Auth == domain.MirrorAuthDeployKey {
		request.PrivateKeyPath, request.KnownHostsPath = material.Paths.Private, material.Paths.KnownHosts
	}
	if _, gitErr := s.git.ConfigureWorkspaceMirror(ctx, workspace, request); gitErr != nil {
		var retireErr error
		if req.Auth == domain.MirrorAuthDeployKey {
			retireErr = retireGeneration(s.root, string(workspace), generation)
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
		restoreErr := s.restoreGitPolicy(rollbackCtx, workspace, old)
		cancel()
		errs := []error{serviceGitError(workspace, gitErr)}
		if retireErr != nil {
			errs = append(errs, fmt.Errorf("retire generated mirror keys failed: %w", retireErr))
		}
		if restoreErr != nil {
			errs = append(errs, fmt.Errorf("restore previous git policy failed: %w", restoreErr))
		}
		return Result{}, errors.Join(errs...)
	}

	pending := domain.WorkspaceMirror{
		WorkspaceID: workspace, SourceURL: source.URL, SourceIdentity: source.Identity,
		Branch: req.Branch, Auth: req.Auth, Generation: generation,
		Status: domain.MirrorStatusPending, KeyFingerprint: material.Fingerprint,
	}
	if old != nil {
		pending.CreatedAt = old.CreatedAt
	}
	if err := s.store.SetWorkspaceMirror(ctx, &pending); err != nil {
		var retireErr error
		if req.Auth == domain.MirrorAuthDeployKey {
			retireErr = retireGeneration(s.root, string(workspace), generation)
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
		restoreErr := s.restoreGitPolicy(rollbackCtx, workspace, old)
		cancel()
		errs := []error{err}
		if retireErr != nil {
			errs = append(errs, fmt.Errorf("retire generated mirror keys failed: %w", retireErr))
		}
		if restoreErr != nil {
			errs = append(errs, fmt.Errorf("restore previous git policy failed: %w", restoreErr))
		}
		return Result{}, errors.Join(errs...)
	}
	if old != nil {
		if retireErr := retireGeneration(s.root, string(workspace), old.Generation); retireErr != nil {
			return s.result(pending, material.PublicKey), fmt.Errorf("retire previous mirror keys failed: %w", retireErr)
		}
	}
	return s.result(pending, material.PublicKey), nil
}

func (s *Service) Status(ctx context.Context, workspace domain.WorkspaceID) (Result, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	m, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorNotConfigured, WorkspaceID: workspace}
		}
		return Result{}, err
	}
	return s.result(*m, s.publicKey(workspace, *m)), nil
}

func (s *Service) Refresh(ctx context.Context, workspace domain.WorkspaceID) (Result, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	m, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorNotConfigured, WorkspaceID: workspace}
		}
		return Result{}, err
	}
	if m.Status == domain.MirrorStatusDisabling {
		return s.result(*m, s.publicKey(workspace, *m)), disablingMirrorError(workspace)
	}
	now := s.now().UTC()
	m.Status, m.LastError, m.LastAttemptAt = domain.MirrorStatusRefreshing, "", now
	if err := s.store.SetWorkspaceMirror(ctx, m); err != nil {
		return Result{}, err
	}
	request := gitengine.MirrorRequest{SourceURL: m.SourceURL, Branch: m.Branch, Generation: m.Generation, Auth: m.Auth}
	if m.Auth == domain.MirrorAuthDeployKey {
		paths, pathErr := generationPaths(s.root, string(workspace), m.Generation)
		if pathErr != nil {
			return s.persistFailure(ctx, *m, now, gitengine.MirrorErrorAuthFailed, pathErr)
		}
		if _, statErr := os.Stat(paths.Private); statErr != nil {
			return s.persistFailure(ctx, *m, now, gitengine.MirrorErrorAuthFailed, statErr)
		}
		if _, statErr := os.Stat(paths.KnownHosts); statErr != nil {
			return s.persistFailure(ctx, *m, now, gitengine.MirrorErrorAuthFailed, statErr)
		}
		request.PrivateKeyPath, request.KnownHostsPath = paths.Private, paths.KnownHosts
	}
	gitResult, gitErr := s.git.RefreshWorkspaceMirror(ctx, workspace, request)
	if gitErr != nil {
		return s.persistFailureResult(ctx, *m, now, gitErrorKind(gitErr), gitResult, gitErr)
	}
	applyGitResult(m, gitResult)
	m.Status = gitResult.Status
	if !m.Status.Valid() || m.Status == domain.MirrorStatusRefreshing || m.Status == domain.MirrorStatusError {
		m.Status = domain.MirrorStatusReady
	}
	m.LastError = ""
	m.LastSuccessAt = now
	if err := s.store.SetWorkspaceMirror(ctx, m); err != nil {
		return Result{}, err
	}
	return s.result(*m, s.publicKey(workspace, *m)), nil
}

func (s *Service) Adopt(ctx context.Context, workspace domain.WorkspaceID, generation int64) (Result, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	m, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorNotConfigured, WorkspaceID: workspace}
		}
		return Result{}, err
	}
	if m.Status == domain.MirrorStatusDisabling {
		return s.result(*m, s.publicKey(workspace, *m)), disablingMirrorError(workspace)
	}
	if generation != m.Generation {
		return s.result(*m, s.publicKey(workspace, *m)), &gitengine.MirrorError{
			Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace,
			Cause: errors.New("generation does not match configured mirror"),
		}
	}
	gitResult, gitErr := s.git.AdoptWorkspaceMirror(ctx, workspace, generation)
	now := s.now().UTC()
	if gitErr != nil {
		return s.persistFailureResult(ctx, *m, now, gitErrorKind(gitErr), gitResult, gitErr)
	}
	applyGitResult(m, gitResult)
	if gitResult.Status.Valid() {
		m.Status = gitResult.Status
	} else {
		m.Status = domain.MirrorStatusReady
	}
	m.LastError = ""
	m.LastAttemptAt, m.LastSuccessAt = now, now
	if err := s.store.SetWorkspaceMirror(ctx, m); err != nil {
		return Result{}, err
	}
	return s.result(*m, s.publicKey(workspace, *m)), nil
}

func (s *Service) Disable(ctx context.Context, workspace domain.WorkspaceID) (Result, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	m, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorNotConfigured, WorkspaceID: workspace}
		}
		return Result{}, err
	}
	previous := *m
	publicKey := s.publicKey(workspace, previous)
	transition := previous
	alreadyDisabling := previous.Status == domain.MirrorStatusDisabling
	if !alreadyDisabling {
		transition.Status = domain.MirrorStatusDisabling
		transition.LastError = ""
		transition.LastAttemptAt = s.now().UTC()
		if err := s.store.SetWorkspaceMirror(ctx, &transition); err != nil {
			return s.result(previous, publicKey), err
		}
	}

	lifecycleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
	defer cancel()
	if disableErr := s.git.DisableWorkspaceMirror(lifecycleCtx, workspace); disableErr != nil {
		if alreadyDisabling {
			return s.result(transition, publicKey), serviceGitError(workspace, disableErr)
		}
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
		rowErr := s.store.SetWorkspaceMirror(rollbackCtx, &previous)
		policyErr := s.restoreGitPolicy(rollbackCtx, workspace, &previous)
		rollbackCancel()
		errs := []error{serviceGitError(workspace, disableErr)}
		if rowErr != nil {
			errs = append(errs, fmt.Errorf("restore workspace mirror failed: %w", rowErr))
		}
		if policyErr != nil {
			errs = append(errs, fmt.Errorf("restore previous git policy failed: %w", policyErr))
		}
		return s.result(previous, publicKey), errors.Join(errs...)
	}
	if retireErr := retireWorkspaceSecrets(s.root, string(workspace)); retireErr != nil {
		return s.result(transition, s.publicKey(workspace, transition)), fmt.Errorf("retire workspace mirror keys failed: %w", retireErr)
	}
	if deleteErr := s.store.DeleteWorkspaceMirror(lifecycleCtx, workspace); deleteErr != nil {
		return s.result(transition, ""), fmt.Errorf("delete workspace mirror failed: %w", deleteErr)
	}
	result := s.result(previous, "")
	if previous.Auth == domain.MirrorAuthDeployKey {
		result.Warning = "remote deploy-key revocation remains external"
	}
	return result, nil
}

// Capture strictly refreshes a configured mirror unless cachedCommit is
// supplied. A local-only workspace reads its normal base branch directly and
// never makes a network claim. Cached mode verifies both persisted consent and
// the current immutable branch object before returning it.
func (s *Service) Capture(ctx context.Context, workspace domain.WorkspaceID, cachedCommit string) (CaptureResult, error) {
	unlock := s.workspaceLock(workspace)
	defer unlock()
	m, err := s.store.GetWorkspaceMirror(ctx, workspace)
	if errors.Is(err, store.ErrNotFound) {
		if cachedCommit != "" {
			return CaptureResult{WorkspaceID: workspace, Cached: true}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace}
		}
		branch := s.workspaceBranch(ctx, workspace)
		commit, commitErr := s.git.WorkspaceBranchCommit(ctx, workspace, branch)
		if commitErr != nil {
			return CaptureResult{WorkspaceID: workspace, Branch: branch}, commitErr
		}
		checked := s.now().UTC()
		return captureResult(workspace, commit, branch, "", checked, false, false), nil
	}
	if err != nil {
		return CaptureResult{WorkspaceID: workspace}, err
	}
	if m.Status == domain.MirrorStatusDisabling {
		result := CaptureResult{
			WorkspaceID: workspace,
			Commit:      m.AcceptedCommit,
			Branch:      m.Branch,
			Source:      m.SourceURL,
			Configured:  true,
			Cached:      cachedCommit != "",
		}
		return result, &gitengine.MirrorError{
			Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace,
			Cause: errors.New("mirror is disabling"),
		}
	}
	if cachedCommit != "" {
		if !domain.ValidMirrorSHA(cachedCommit) || cachedCommit != m.AcceptedCommit {
			return CaptureResult{WorkspaceID: workspace, Commit: m.AcceptedCommit, Branch: m.Branch, Source: m.SourceURL, Configured: true, Cached: true}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace}
		}
		current, currentErr := s.git.WorkspaceBranchCommit(ctx, workspace, m.Branch)
		if currentErr != nil || current != cachedCommit {
			if currentErr == nil {
				currentErr = errors.New("cached mirror base changed")
			}
			return CaptureResult{WorkspaceID: workspace, Commit: m.AcceptedCommit, Branch: m.Branch, Source: m.SourceURL, Configured: true, Cached: true}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorDiverged, WorkspaceID: workspace, Cause: currentErr}
		}
		checked := s.now().UTC()
		return captureResult(workspace, cachedCommit, m.Branch, m.SourceURL, checked, true, true), nil
	}
	// Refresh is called while this workspace lock is held; use the unlocked
	// implementation to avoid recursively taking the same non-reentrant lock.
	result, refreshErr := s.refreshLocked(ctx, workspace, *m)
	if refreshErr != nil {
		return CaptureResult{WorkspaceID: workspace, Commit: m.AcceptedCommit, Branch: m.Branch, Source: m.SourceURL, Configured: true}, refreshErr
	}
	commit := result.Mirror.AcceptedCommit
	if commit == "" {
		return CaptureResult{WorkspaceID: workspace, Commit: m.AcceptedCommit, Branch: m.Branch, Source: m.SourceURL, Configured: true}, &gitengine.MirrorError{Kind: gitengine.MirrorErrorNoCandidate, WorkspaceID: workspace}
	}
	checked := s.now().UTC()
	return captureResult(workspace, commit, m.Branch, m.SourceURL, checked, true, false), nil
}

func (s *Service) refreshLocked(ctx context.Context, workspace domain.WorkspaceID, current domain.WorkspaceMirror) (Result, error) {
	now := s.now().UTC()
	current.Status, current.LastError, current.LastAttemptAt = domain.MirrorStatusRefreshing, "", now
	if err := s.store.SetWorkspaceMirror(ctx, &current); err != nil {
		return Result{}, err
	}
	request := gitengine.MirrorRequest{SourceURL: current.SourceURL, Branch: current.Branch, Generation: current.Generation, Auth: current.Auth}
	if current.Auth == domain.MirrorAuthDeployKey {
		paths, pathErr := generationPaths(s.root, string(workspace), current.Generation)
		if pathErr != nil {
			return s.persistFailure(ctx, current, now, gitengine.MirrorErrorAuthFailed, pathErr)
		}
		if _, statErr := os.Stat(paths.Private); statErr != nil {
			return s.persistFailure(ctx, current, now, gitengine.MirrorErrorAuthFailed, statErr)
		}
		if _, statErr := os.Stat(paths.KnownHosts); statErr != nil {
			return s.persistFailure(ctx, current, now, gitengine.MirrorErrorAuthFailed, statErr)
		}
		request.PrivateKeyPath, request.KnownHostsPath = paths.Private, paths.KnownHosts
	}
	gitResult, gitErr := s.git.RefreshWorkspaceMirror(ctx, workspace, request)
	if gitErr != nil {
		return s.persistFailureResult(ctx, current, now, gitErrorKind(gitErr), gitResult, gitErr)
	}
	applyGitResult(&current, gitResult)
	current.Status = gitResult.Status
	if !current.Status.Valid() || current.Status == domain.MirrorStatusRefreshing || current.Status == domain.MirrorStatusError {
		current.Status = domain.MirrorStatusReady
	}
	current.LastError, current.LastSuccessAt = "", now
	if err := s.store.SetWorkspaceMirror(ctx, &current); err != nil {
		return Result{}, err
	}
	return s.result(current, s.publicKey(workspace, current)), nil
}

func (s *Service) persistFailure(ctx context.Context, current domain.WorkspaceMirror, now time.Time, kind gitengine.MirrorErrorKind, cause error) (Result, error) {
	failure := &gitengine.MirrorError{
		Kind: kind, WorkspaceID: current.WorkspaceID, SourceURL: current.SourceURL,
		Branch: current.Branch, Cause: cause,
	}
	current.Status, current.LastError, current.LastAttemptAt = mirrorStatus(kind), failure.Error(), now
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorRollbackTimeout)
	defer cancel()
	if err := s.store.SetWorkspaceMirror(persistCtx, &current); err != nil {
		return Result{}, err
	}
	return s.result(current, s.publicKey(current.WorkspaceID, current)), failure
}

func (s *Service) persistFailureResult(ctx context.Context, current domain.WorkspaceMirror, now time.Time, kind gitengine.MirrorErrorKind, gitResult gitengine.MirrorResult, cause error) (Result, error) {
	applyGitResult(&current, gitResult)
	return s.persistFailure(ctx, current, now, kind, cause)
}

func (s *Service) result(m domain.WorkspaceMirror, publicKey string) Result {
	return Result{Mirror: m, PublicKey: publicKey}
}

func disablingMirrorError(workspace domain.WorkspaceID) *gitengine.MirrorError {
	return &gitengine.MirrorError{
		Kind: gitengine.MirrorErrorInvalidRequest, WorkspaceID: workspace,
		Cause: errors.New("mirror is disabling"),
	}
}

func (s *Service) publicKey(workspace domain.WorkspaceID, m domain.WorkspaceMirror) string {
	if m.Auth != domain.MirrorAuthDeployKey {
		return ""
	}
	paths, err := generationPaths(s.root, string(workspace), m.Generation)
	if err != nil {
		return ""
	}
	key, err := readPublicKey(paths)
	if err != nil {
		return ""
	}
	return key
}

func (s *Service) restoreGitPolicy(ctx context.Context, workspace domain.WorkspaceID, old *domain.WorkspaceMirror) error {
	if old == nil {
		return s.git.DisableWorkspaceMirror(ctx, workspace)
	}
	req := gitengine.MirrorRequest{SourceURL: old.SourceURL, Branch: old.Branch, Generation: old.Generation, Auth: old.Auth}
	if old.Auth == domain.MirrorAuthDeployKey {
		if paths, err := generationPaths(s.root, string(workspace), old.Generation); err == nil {
			req.PrivateKeyPath, req.KnownHostsPath = paths.Private, paths.KnownHosts
		}
	}
	_, err := s.git.ConfigureWorkspaceMirror(ctx, workspace, req)
	return err
}

func (s *Service) workspaceBranch(ctx context.Context, workspace domain.WorkspaceID) string {
	if provider, ok := s.store.(interface {
		GetWorkspace(context.Context, domain.WorkspaceID) (*domain.Workspace, error)
	}); ok {
		if w, err := provider.GetWorkspace(ctx, workspace); err == nil && w.BaseBranch != "" {
			return w.BaseBranch
		}
	}
	return domain.DefaultBaseBranch
}

func captureResult(workspace domain.WorkspaceID, commit, branch, source string, checked time.Time, configured, cached bool) CaptureResult {
	return CaptureResult{WorkspaceID: workspace, Commit: commit, Branch: branch, Source: source, CheckedAt: checked, Configured: configured, Cached: cached}
}

func gitErrorKind(err error) gitengine.MirrorErrorKind {
	var ge *gitengine.MirrorError
	if errors.As(err, &ge) && ge != nil && ge.Kind != "" {
		return ge.Kind
	}
	return gitengine.MirrorErrorFailed
}

func serviceGitError(workspace domain.WorkspaceID, err error) error {
	var ge *gitengine.MirrorError
	if errors.As(err, &ge) {
		return err
	}
	return &gitengine.MirrorError{Kind: gitErrorKind(err), WorkspaceID: workspace, Cause: err}
}

func mirrorStatus(kind gitengine.MirrorErrorKind) domain.MirrorStatus {
	switch kind {
	case gitengine.MirrorErrorAuthFailed:
		return domain.MirrorStatusAuthFailed
	case gitengine.MirrorErrorOffline:
		return domain.MirrorStatusOffline
	case gitengine.MirrorErrorSourceMissing:
		return domain.MirrorStatusSourceMissing
	case gitengine.MirrorErrorNoCandidate:
		return domain.MirrorStatusPending
	case gitengine.MirrorErrorRewritten:
		return domain.MirrorStatusRewritten
	case gitengine.MirrorErrorDiverged:
		return domain.MirrorStatusDiverged
	default:
		return domain.MirrorStatusError
	}
}

func applyGitResult(m *domain.WorkspaceMirror, r gitengine.MirrorResult) {
	observed := r.ObservedCommit
	accepted := r.AcceptedCommit
	if domain.ValidMirrorSHA(observed) {
		m.ObservedCommit = observed
	}
	if domain.ValidMirrorSHA(accepted) {
		m.AcceptedCommit = accepted
	}
}

var _ Store = (*store.DB)(nil)
var _ Git = (*gitengine.Engine)(nil)
