package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const verificationConcurrency = 16

var verificationSlots = make(chan struct{}, verificationConcurrency)

// Verify records a running verification before starting any runtime work. The
// returned aggregate is therefore useful to callers even when the command
// takes minutes to complete or the gateway request is cancelled.
func (s *Service) Verify(ctx context.Context, actor Actor, p protocol.IntegrationVerifyParams) (protocol.Candidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.store == nil || s.git == nil || s.runtime == nil || s.environment == nil {
		return protocol.Candidate{}, ErrUnavailable
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.CandidateRevision == "" || p.IdempotencyKey == "" {
		return protocol.Candidate{}, fmt.Errorf("%w: workspace, candidate, revision, and idempotency key are required", ErrInvalidRequest)
	}
	if len(p.Argv) == 0 || len(p.Argv) > protocol.IntegrationMaxArgv {
		return protocol.Candidate{}, fmt.Errorf("%w: argv must contain 1..%d arguments", ErrInvalidRequest, protocol.IntegrationMaxArgv)
	}
	argv := make([]string, len(p.Argv))
	for i, arg := range p.Argv {
		if arg == "" || len(arg) > protocol.IntegrationMaxVerificationArgBytes || strings.IndexByte(arg, 0) >= 0 {
			return protocol.Candidate{}, fmt.Errorf("%w: invalid argv[%d]", ErrInvalidRequest, i)
		}
		argv[i] = arg
	}
	if p.TimeoutSeconds < 0 {
		return protocol.Candidate{}, fmt.Errorf("%w: timeout must not be negative", ErrInvalidRequest)
	}
	timeoutSeconds := p.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = int(protocol.IntegrationDefaultVerificationTimeout / time.Second)
	}
	if timeoutSeconds <= 0 || timeoutSeconds > int(protocol.IntegrationMaxVerificationTimeout/time.Second) {
		return protocol.Candidate{}, fmt.Errorf("%w: timeout must be between one second and 30 minutes", ErrInvalidRequest)
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if err := ctx.Err(); err != nil {
		return protocol.Candidate{}, err
	}

	candidateLock := s.lock(p.CandidateID)
	candidateLock.Lock()
	record, candidate, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		candidateLock.Unlock()
		return protocol.Candidate{}, err
	}
	release, err := s.authorize(ctx, actor, candidate, protocol.MethodIntegrationVerify)
	if err != nil {
		candidateLock.Unlock()
		return protocol.Candidate{}, err
	}
	semanticDigest := digest(struct {
		Revision       string
		Argv           []string
		TimeoutSeconds int
		ActorKey       string
	}{p.CandidateRevision, argv, timeoutSeconds, actorKey(actor)})
	for _, mutation := range candidate.Mutations {
		if mutation.Operation != protocol.MethodIntegrationVerify || mutation.ActorKey != actorKey(actor) || mutation.IdempotencyKey != p.IdempotencyKey {
			continue
		}
		if mutation.Digest != semanticDigest {
			release()
			candidateLock.Unlock()
			return protocol.Candidate{}, fmt.Errorf("%w: idempotency key has different verification inputs", ErrConflict)
		}
		for _, existing := range candidate.Verifications {
			if existing.VerificationID == mutation.ResultID {
				out := *candidate
				release()
				candidateLock.Unlock()
				return out, nil
			}
		}
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, fmt.Errorf("%w: verification result is unavailable", ErrConflict)
	}
	if candidate.WorkspaceID != p.WorkspaceID || candidate.CandidateRevision != p.CandidateRevision {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, fmt.Errorf("%w: candidate revision mismatch", ErrConflict)
	}
	if candidate.State != protocol.CandidateFrozen {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, fmt.Errorf("%w: candidate is not frozen", ErrConflict)
	}
	if !s.nowTime().Before(candidate.ExpiresAt) {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, ErrExpired
	}
	if len(candidate.Verifications) >= protocol.IntegrationMaxVerifications {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, fmt.Errorf("%w: verification limit reached", ErrInvalidRequest)
	}
	if len(candidate.Mutations) >= protocol.IntegrationMaxCandidateMutations {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, fmt.Errorf("%w: mutation history limit reached", ErrInvalidRequest)
	}
	if e := s.validateOwned(ctx, candidate); e != nil {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, e
	}
	creationKey := verificationCreationKey(candidate.CandidateID, actor, p.IdempotencyKey)
	verificationID, err := randomVerificationID()
	if err != nil {
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, err
	}
	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	workCtx, cancel := context.WithTimeout(baseCtx, timeout)
	if !s.registerVerification(verificationID, cancel) {
		cancel()
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, ErrUnavailable
	}
	created := s.nowTime()
	expires := created.Add(24 * time.Hour)
	if candidate.ExpiresAt.Before(expires) {
		expires = candidate.ExpiresAt
	}
	verification := protocol.Verification{
		VerificationID: verificationID,
		Argv:           append([]string(nil), argv...),
		Status:         protocol.VerificationRunning,
		CreatedAt:      created,
		ExpiresAt:      expires,
		TimeoutSeconds: timeoutSeconds,
		CreationKey:    creationKey,
	}
	candidate.Verifications = append(candidate.Verifications, verification)
	candidate.Mutations = append(candidate.Mutations, protocol.CandidateMutation{
		ActorKey: actorKey(actor), Operation: protocol.MethodIntegrationVerify,
		IdempotencyKey: p.IdempotencyKey, Digest: semanticDigest, ResultID: verificationID,
	})
	if e := s.save(ctx, record, candidate); e != nil {
		s.unregisterVerification(verificationID)
		cancel()
		release()
		candidateLock.Unlock()
		return protocol.Candidate{}, e
	}
	// Admission protects durable queueing only. The asynchronous worker
	// rechecks the current actor at each runtime boundary below.
	release()
	out := *candidate
	candidateLock.Unlock()
	go func() {
		defer s.unregisterVerification(verificationID)
		defer cancel()
		s.runVerification(workCtx, actor, domain.WorkspaceID(p.WorkspaceID), p.CandidateID, verificationID, argv, timeout)
	}()
	return out, nil
}

func randomVerificationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("integration: generate verification id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func verificationCreationKey(candidateID string, actor Actor, idem string) string {
	// The actor key and idempotency key are authenticated/service-owned values;
	// keeping the key opaque prevents a runtime label from disclosing argv.
	return "aether-verification-" + digest(struct {
		Candidate string
		Actor     string
		Key       string
	}{candidateID, actorKey(actor), idem})
}
func (s *Service) runVerification(ctx context.Context, actor Actor, workspaceID domain.WorkspaceID, candidateID, verificationID string, argv []string, timeout time.Duration) {
	if err := ctx.Err(); err != nil {
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationCancelled, nil, nil, false, err)
		return
	}
	select {
	case verificationSlots <- struct{}{}:
		defer func() { <-verificationSlots }()
	case <-ctx.Done():
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, classifyContext(ctx, protocol.VerificationCancelled), nil, nil, false, ctx.Err())
		return
	}
	candidateLock := s.lock(candidateID)
	candidateLock.Lock()
	claimedLock := true
	releaseClaim := func() {
		if claimedLock {
			claimedLock = false
			candidateLock.Unlock()
		}
	}
	defer releaseClaim()
	record, candidate, err := s.load(ctx, string(workspaceID), candidateID)
	if err == nil {
		if candidate.State != protocol.CandidateFrozen || candidate.CandidateRevision == "" || !s.nowTime().Before(candidate.ExpiresAt) {
			err = ErrExpired
		}
	}
	if err != nil {
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}

	workspace, err := s.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}
	checkout, err := s.git.CandidateVerificationCheckout(ctx, workspaceID, candidateID, verificationID, candidate.CandidateRevision)
	if err != nil {
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}

	spec, err := s.environment(ctx, actor, workspace, checkout)
	if err != nil {
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, removeErr))
			return
		}
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}
	// The client owns argv only; every other runtime setting comes from the
	// server environment callback. Force the exact command and creation key.
	spec.Command = append([]string(nil), argv...)
	if spec.WorktreeHostPath != "" && spec.WorktreeHostPath != checkout {
		mismatchErr := fmt.Errorf("%w: environment returned a different worktree", ErrUnavailable)
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(mismatchErr, removeErr))
			return
		}
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, mismatchErr)
		return
	}
	spec.WorktreeHostPath = checkout
	spec.CreationKey = currentCreationKey(candidate, verificationID)
	creationKey := spec.CreationKey
	if e := spec.Validate(); e != nil {
		if cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, ""); cleanupErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
			return
		}
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
		return
	}
	if s.prepareRuntime != nil {
		if e := s.prepareRuntime(ctx, &spec); e != nil {
			if cleanupErr := s.cleanupVerificationRuntime(context.Background(), creationKey, ""); cleanupErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
				return
			}
			if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
				return
			}
			releaseClaim()
			s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
			return
		}
		if spec.CreationKey != creationKey {
			e := fmt.Errorf("%w: runtime preparation changed creation key", ErrUnavailable)
			if cleanupErr := s.cleanupVerificationRuntime(context.Background(), creationKey, ""); cleanupErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
				return
			}
			if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
				return
			}
			releaseClaim()
			s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
			return
		}
		if e := spec.Validate(); e != nil {
			if cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, ""); cleanupErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
				return
			}
			if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
				releaseClaim()
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
				return
			}
			releaseClaim()
			s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
			return
		}
	}
	environmentSHA256 := digest(spec.Env)
	setupScriptSHA256 := digest(spec.SetupScript)

	// Recheck authorization immediately before materializing the runtime.
	// Candidate lock is already held, preserving lock order with Show.
	createRelease, err := s.authorize(ctx, actor, candidate, protocol.MethodIntegrationVerify)
	if err != nil {
		if cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, ""); cleanupErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, removeErr))
			return
		}
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}
	containerID, err := s.runtime.Create(ctx, spec)
	if err != nil {
		createRelease()
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		if cleanupErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			releaseClaim()
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, removeErr))
			return
		}
		releaseClaim()
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, classifyContext(ctx, protocol.VerificationError), nil, nil, false, err)
		return
	}
	for i := range candidate.Verifications {
		if candidate.Verifications[i].VerificationID == verificationID {
			candidate.Verifications[i].ContainerID = string(containerID)
			break
		}
	}
	if e := s.save(ctx, record, candidate); e != nil {
		createRelease()
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		releaseClaim()
		if cleanupErr == nil {
			if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
				s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
				return
			}
			s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
		} else {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
		}
		return
	}
	createRelease()
	claimedLock = false
	candidateLock.Unlock()
	info, inspectErr := s.runtime.Inspect(ctx, containerID)
	if inspectErr != nil {
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		if cleanupErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(inspectErr, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(inspectErr, removeErr))
			return
		}
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, inspectErr)
		return
	}
	observedImage := info.Image
	if e := s.updateVerificationMetadata(context.Background(), workspaceID, candidateID, verificationID, spec.Image, observedImage, info.User, spec.WorkingDir, int(timeout/time.Second), spec.CPULimit, spec.MemoryLimitBytes, environmentSHA256, setupScriptSHA256); e != nil {
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		if cleanupErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(e, removeErr))
			return
		}
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, e)
		return
	}

	attachment, err := s.runtime.Attach(ctx, containerID)
	if err != nil {
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		if cleanupErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(err, removeErr))
			return
		}
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, err)
		return
	}
	var output boundedOutput
	var drains sync.WaitGroup
	drains.Add(2)
	go func() {
		defer drains.Done()
		_, err := io.Copy(&output, attachment.Stdout())
		output.setReadError(err)
	}()
	go func() {
		defer drains.Done()
		_, err := io.Copy(&output, attachment.Stderr())
		output.setReadError(err)
	}()
	var exit *int
	var runErr error
	startLock := s.lock(candidateID)
	startLock.Lock()
	_, startCandidate, claimErr := s.load(ctx, string(workspaceID), candidateID)
	if claimErr == nil && (startCandidate.State != protocol.CandidateFrozen ||
		startCandidate.CandidateRevision != candidate.CandidateRevision ||
		!s.nowTime().Before(startCandidate.ExpiresAt)) {
		claimErr = ErrExpired
	}
	if claimErr == nil {
		claimErr = s.ensureRunningVerification(startCandidate, verificationID)
	}
	var startRelease func()
	var startErr error
	if claimErr == nil {
		startRelease, claimErr = s.authorize(ctx, actor, startCandidate, protocol.MethodIntegrationVerify)
	}
	if claimErr == nil {
		startErr = s.runtime.Start(ctx, containerID)
	}
	if startRelease != nil {
		startRelease()
	}
	startLock.Unlock()
	if claimErr != nil {
		_ = attachment.Close()
		cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
		if cleanupErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(claimErr, cleanupErr))
			return
		}
		if removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID); removeErr != nil {
			s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, errors.Join(claimErr, removeErr))
			return
		}
		s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, protocol.VerificationError, nil, nil, false, claimErr)
		return
	}
	if startErr != nil {
		runErr = startErr
	} else {
		status, waitErr := s.runtime.Wait(ctx, containerID)
		if waitErr != nil {
			runErr = waitErr
		} else {
			code := status.Code
			exit = &code
		}
	}
	drainDone := make(chan struct{})
	go func() {
		drains.Wait()
		close(drainDone)
	}()
	var drainErr error
	drained := false
	if runErr == nil {
		drainTimer := time.NewTimer(15 * time.Second)
		select {
		case <-drainDone:
			drained = true
		case <-drainTimer.C:
			drainErr = errors.New("verification output drain timed out")
		}
		if !drainTimer.Stop() {
			select {
			case <-drainTimer.C:
			default:
			}
		}
	}
	// On a normal exit, wait for both streams to reach EOF before detaching.
	// On failure/timeout, detach first so cleanup can stop blocked readers.
	_ = attachment.Close()
	cleanupErr := s.cleanupVerificationRuntime(context.Background(), spec.CreationKey, containerID)
	if !drained {
		drainTimer := time.NewTimer(15 * time.Second)
		select {
		case <-drainDone:
		case <-drainTimer.C:
			if drainErr == nil {
				drainErr = errors.New("verification output drain did not stop")
			}
		}
		if !drainTimer.Stop() {
			select {
			case <-drainTimer.C:
			default:
			}
		}
	}
	if cleanupErr != nil {
		if drainErr != nil {
			cleanupErr = errors.Join(cleanupErr, drainErr)
		}
		s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, cleanupErr)
		return
	}
	if runErr == nil {
		if drainErr != nil {
			runErr = drainErr
		} else if readErr := output.ReadError(); readErr != nil {
			runErr = fmt.Errorf("verification output: %w", readErr)
		}
	}

	// All container processes are stopped/destroyed before the hardened source
	sourceErr := s.git.CheckCandidateVerification(context.Background(), workspaceID, candidateID, verificationID, candidate.CandidateRevision)
	removeErr := s.git.RemoveCandidateVerification(context.Background(), workspaceID, candidateID, verificationID)
	if removeErr != nil {
		s.markVerificationCleanupFailure(context.Background(), workspaceID, candidateID, verificationID, removeErr)
		return
	}
	status := protocol.VerificationPassed
	if sourceErr != nil {
		status = protocol.VerificationError
		if errors.Is(sourceErr, gitengine.ErrCandidateVerificationChanged) {
			status = protocol.VerificationSourceChanged
		}
		runErr = sourceErr
	} else if runErr != nil {
		status = classifyVerificationFailure(ctx, runErr)
	}
	if status == protocol.VerificationPassed && exit != nil && *exit != 0 {
		status = protocol.VerificationFailed
	}
	if status == protocol.VerificationPassed {
		lock := s.lock(candidateID)
		lock.Lock()

		_, fresh, validateErr := s.load(context.Background(), string(workspaceID), candidateID)
		if validateErr == nil {
			validateErr = s.validateOwned(context.Background(), fresh)
		}
		lock.Unlock()
		if validateErr != nil {
			status = protocol.VerificationError
			runErr = validateErr
		}
	}
	s.finishVerification(context.Background(), workspaceID, candidateID, verificationID, status, exit, output.Bytes(), output.Truncated(), runErr)
}

func currentCreationKey(c *protocol.Candidate, verificationID string) string {
	for _, verification := range c.Verifications {
		if verification.VerificationID == verificationID {
			return verification.CreationKey
		}
	}
	return ""
}
func (s *Service) ensureRunningVerification(c *protocol.Candidate, id string) error {
	if c == nil {
		return ErrConflict
	}
	for _, v := range c.Verifications {
		if v.VerificationID == id && v.Status == protocol.VerificationRunning {
			return nil
		}
	}
	return ErrConflict
}

func classifyContext(ctx context.Context, fallback protocol.VerificationStatus) protocol.VerificationStatus {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return protocol.VerificationTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return protocol.VerificationCancelled
	}
	return fallback
}

func classifyVerificationFailure(ctx context.Context, err error) protocol.VerificationStatus {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return protocol.VerificationTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return protocol.VerificationCancelled
	}
	return protocol.VerificationError
}

func destroyVerificationContainer(id runtime.ID, rt runtime.Runtime) error {
	if id == "" || rt == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stopErr := rt.Stop(cleanupCtx, id, 2*time.Second)
	if stopErr != nil && !errors.Is(stopErr, runtime.ErrNotFound) {
		// Destroy is still attempted: stopping and destruction are independent
		// runtime operations, and a stopped/absent container is safe to remove.
		_ = rt.Destroy(cleanupCtx, id)
		return stopErr
	}
	if err := rt.Destroy(cleanupCtx, id); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	return nil
}

// cleanupVerificationRuntime proves that the container is absent before
// releasing any resources staged by PrepareRuntime. An unknown lookup or
// failed destroy deliberately leaves the durable verification running so
// recovery can retry without losing ownership of staged resources.
func (s *Service) cleanupVerificationRuntime(ctx context.Context, creationKey string, containerID runtime.ID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if containerID != "" {
		if err := destroyVerificationContainer(containerID, s.runtime); err != nil {
			return err
		}
	} else if creationKey != "" {
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		foundID, err := s.runtime.FindByCreationKey(lookupCtx, creationKey)
		cancel()
		switch {
		case err == nil && foundID == "":
			return errors.New("runtime returned an empty container id for creation key")
		case err == nil:
			if err := destroyVerificationContainer(foundID, s.runtime); err != nil {
				return err
			}
		case !errors.Is(err, runtime.ErrNotFound):
			return fmt.Errorf("find verification container: %w", err)
		}
	}
	if s.releaseRuntime != nil && creationKey != "" {
		if err := s.releaseRuntime(ctx, creationKey); err != nil {
			return fmt.Errorf("release verification runtime resources: %w", err)
		}
	}
	return nil
}

type boundedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
	readErr   error
}

func (o *boundedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	remaining := protocol.IntegrationMaxOutputBytes - o.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = o.buf.Write(p)
		} else {
			_, _ = o.buf.Write(p[:remaining])
			o.truncated = true
		}
	} else if len(p) != 0 {
		o.truncated = true
	}
	return len(p), nil
}

func (o *boundedOutput) Bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buf.Bytes()...)
}
func (o *boundedOutput) Truncated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.truncated
}
func (o *boundedOutput) setReadError(err error) {
	if err == nil || errors.Is(err, io.EOF) {
		return
	}
	o.mu.Lock()
	if o.readErr == nil {
		o.readErr = err
	}
	o.mu.Unlock()
}

func (o *boundedOutput) ReadError() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.readErr
}

func (s *Service) updateVerificationMetadata(ctx context.Context, workspaceID domain.WorkspaceID, candidateID, verificationID, image, observedImage, user, workingDir string, timeoutSeconds int, cpuLimit float64, memoryLimit int64, environmentSHA256, setupScriptSHA256 string) error {
	lock := s.lock(candidateID)
	lock.Lock()
	defer lock.Unlock()
	record, candidate, err := s.load(ctx, string(workspaceID), candidateID)
	if err != nil {
		return err
	}
	if candidate.State != protocol.CandidateFrozen || !s.nowTime().Before(candidate.ExpiresAt) {
		return ErrExpired
	}
	for i := range candidate.Verifications {
		if candidate.Verifications[i].VerificationID != verificationID || candidate.Verifications[i].Status != protocol.VerificationRunning {
			continue
		}
		v := &candidate.Verifications[i]
		v.Image, v.ObservedImage = image, observedImage
		v.User, v.WorkingDir = user, workingDir
		v.TimeoutSeconds, v.CPULimit, v.MemoryLimitBytes = timeoutSeconds, cpuLimit, memoryLimit
		v.EnvironmentSHA256, v.SetupScriptSHA256 = environmentSHA256, setupScriptSHA256
		return s.save(ctx, record, candidate)
	}
	return ErrConflict
}

// markVerificationCleanupFailure intentionally leaves the record running so
// restart cleanup can rediscover its CreationKey and retry resource removal.
func (s *Service) markVerificationCleanupFailure(ctx context.Context, workspaceID domain.WorkspaceID, candidateID, verificationID string, cleanupErr error) {
	lock := s.lock(candidateID)
	lock.Lock()
	defer lock.Unlock()
	record, candidate, err := s.load(ctx, string(workspaceID), candidateID)
	if err != nil || candidate.State != protocol.CandidateFrozen {
		return
	}
	for i := range candidate.Verifications {
		if candidate.Verifications[i].VerificationID != verificationID || candidate.Verifications[i].Status != protocol.VerificationRunning {
			continue
		}
		candidate.Verifications[i].Error = cleanupErr.Error()
		_ = s.save(ctx, record, candidate)
		return
	}
}

func (s *Service) finishVerification(ctx context.Context, workspaceID domain.WorkspaceID, candidateID, verificationID string, status protocol.VerificationStatus, exit *int, output []byte, truncated bool, runErr error) {
	lock := s.lock(candidateID)
	lock.Lock()
	defer lock.Unlock()
	record, candidate, err := s.load(ctx, string(workspaceID), candidateID)
	if err != nil || candidate.State != protocol.CandidateFrozen || !s.nowTime().Before(candidate.ExpiresAt) {
		return
	}
	for i := range candidate.Verifications {
		if candidate.Verifications[i].VerificationID != verificationID || candidate.Verifications[i].Status != protocol.VerificationRunning {
			continue
		}
		v := &candidate.Verifications[i]
		v.Status = status
		v.ExitCode = exit
		v.Output = string(output)
		v.OutputTruncated = truncated
		v.ContainerID = ""
		finished := s.nowTime()
		v.FinishedAt = &finished
		if runErr != nil {
			v.Error = runErr.Error()
		}
		_ = s.save(ctx, record, candidate)
		return
	}
}

// recoverVerifications is used by retention/restart recovery. It only destroys
// containers carrying a persisted creation key and marks records interrupted;
// it never re-runs a command or fabricates an exit code.
func (s *Service) recoverVerifications(ctx context.Context, c *protocol.Candidate) error {
	if c == nil || s.runtime == nil {
		return nil
	}
	recovered := make(map[string]struct{})
	for _, v := range c.Verifications {
		if v.Status != protocol.VerificationRunning || s.verificationActive(v.VerificationID) {
			continue
		}
		if err := s.cleanupVerificationRuntime(ctx, v.CreationKey, ""); err != nil {
			return err
		}
		if s.git != nil {
			if err := s.git.RemoveCandidateVerification(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID, v.VerificationID); err != nil {
				return err
			}
		}
		recovered[v.VerificationID] = struct{}{}
	}
	if len(recovered) == 0 {
		return nil
	}
	record, fresh, err := s.load(ctx, c.WorkspaceID, c.CandidateID)
	if err != nil {
		return err
	}
	if fresh.State == protocol.CandidateExpired {
		return nil
	}
	changed := false
	finished := s.nowTime()
	for i := range fresh.Verifications {
		if fresh.Verifications[i].Status != protocol.VerificationRunning {
			continue
		}
		if _, ok := recovered[fresh.Verifications[i].VerificationID]; !ok {
			continue
		}
		fresh.Verifications[i].Status = protocol.VerificationError
		fresh.Verifications[i].Error = "verification interrupted during recovery"
		fresh.Verifications[i].FinishedAt = &finished
		fresh.Verifications[i].ContainerID = ""
		changed = true
	}
	if changed {
		return s.save(ctx, record, fresh)
	}
	return nil
}
