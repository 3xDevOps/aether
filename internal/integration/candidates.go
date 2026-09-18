package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func newCandidateID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("integration: generate candidate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Service) Prepare(ctx context.Context, actor Actor, p protocol.IntegrationPrepareParams) (protocol.Candidate, error) {
	var zero protocol.Candidate
	if s.store == nil || s.git == nil || s.evidence == nil {
		return zero, ErrUnavailable
	}
	if p.WorkspaceID == "" || len(p.Submissions) == 0 || len(p.Submissions) > protocol.IntegrationMaxSubmissions || !validRef(p.TargetRef) || !validObjectIDLoose(p.ExpectedTargetRevision) || !cleanID(p.IdempotencyKey) {
		return zero, fmt.Errorf("%w: invalid prepare parameters", ErrInvalidRequest)
	}
	if len(p.RequiredSources) > protocol.IntegrationMaxSubmissions {
		return zero, fmt.Errorf("%w: too many required sources", ErrInvalidRequest)
	}
	wsID := domain.WorkspaceID(p.WorkspaceID)
	release, err := s.authorizeWorkspace(ctx, actor, wsID, protocol.MethodIntegrationPrepare, p.MissionID, nil, p.Submissions)
	if err != nil {
		return zero, err
	}
	defer release()
	key := actorKey(actor)
	dig := digest(p)
	if existing, e := s.store.GetIntegrationCandidateByKey(ctx, wsID, key, p.IdempotencyKey); e == nil && existing != nil {
		if existing.Digest != dig {
			return zero, ErrConflict
		}
		l := s.lock(existing.ID)
		l.Lock()
		defer l.Unlock()
		rec, c, le := s.load(ctx, p.WorkspaceID, existing.ID)
		if le != nil {
			return zero, le
		}
		return s.resumePrepare(ctx, p, rec, c)
	} else if e != nil && !errors.Is(e, store.ErrNotFound) {
		return zero, e
	}
	now := s.nowTime()
	candidateID, err := newCandidateID()
	if err != nil {
		return zero, err
	}
	candidate := protocol.Candidate{CandidateID: candidateID, WorkspaceID: p.WorkspaceID, MissionID: p.MissionID,
		Submissions: append([]protocol.SubmissionRef(nil), p.Submissions...), Inputs: make([]protocol.CandidateInput, 0, len(p.Submissions)),
		RequiredSources: append([]string(nil), p.RequiredSources...), TargetRef: p.TargetRef, ExpectedTargetRevision: p.ExpectedTargetRevision, State: protocol.CandidatePreparing,
		Verifications: []protocol.Verification{}, Mutations: []protocol.CandidateMutation{}, CreatedAt: now, ExpiresAt: now.Add(protocol.IntegrationCandidateLifetime)}
	payload, _ := json.Marshal(candidate)
	rec := &store.IntegrationCandidate{ID: candidate.CandidateID, WorkspaceID: wsID, ActorKey: key, IdempotencyKey: p.IdempotencyKey, Digest: dig,
		State: string(candidate.State), Version: 1, Payload: payload, CreatedAt: now, ExpiresAt: candidate.ExpiresAt}
	if e := s.store.CreateIntegrationCandidate(ctx, rec); e != nil {
		if errors.Is(e, store.ErrConflict) {
			if old, ge := s.store.GetIntegrationCandidateByKey(ctx, wsID, key, p.IdempotencyKey); ge == nil && old != nil {
				if old.Digest != dig {
					return zero, ErrConflict
				}
				l := s.lock(old.ID)
				l.Lock()
				defer l.Unlock()
				current, c, le := s.load(ctx, p.WorkspaceID, old.ID)
				if le != nil {
					return zero, le
				}
				return s.resumePrepare(ctx, p, current, c)
			}
		}
		return zero, e
	}
	l := s.lock(candidate.CandidateID)
	l.Lock()
	defer l.Unlock()
	current, c, le := s.load(ctx, p.WorkspaceID, candidate.CandidateID)
	if le != nil {
		return zero, le
	}
	return s.resumePrepare(ctx, p, current, c)
}

// resumePrepare reconciles a durable preparing aggregate. Input retention and
// transcript paths are deterministic, so retrying an input whose append save
// was lost safely verifies/reuses the already-acquired artifacts.
func (s *Service) resumePrepare(ctx context.Context, p protocol.IntegrationPrepareParams, rec *store.IntegrationCandidate, candidate *protocol.Candidate) (protocol.Candidate, error) {
	var zero protocol.Candidate
	if rec == nil || candidate == nil {
		return zero, ErrInvalidRequest
	}
	if !candidate.ExpiresAt.IsZero() && !s.nowTime().Before(candidate.ExpiresAt) {
		return zero, ErrExpired
	}
	if candidate.State == protocol.CandidateDeleting || candidate.State == protocol.CandidateExpired {
		return zero, ErrExpired
	}
	if candidate.State != protocol.CandidatePreparing {
		return *candidate, nil
	}
	if len(candidate.Submissions) != len(p.Submissions) {
		return zero, ErrConflict
	}
	for i := range candidate.Submissions {
		if candidate.Submissions[i] != p.Submissions[i] {
			return zero, ErrConflict
		}
	}
	if len(candidate.Inputs) > len(candidate.Submissions) {
		return zero, ErrConflict
	}
	for i := range candidate.Inputs {
		if candidate.Inputs[i].Submission != candidate.Submissions[i] {
			return zero, ErrConflict
		}
	}
	candidate.Error = ""
	for i := len(candidate.Inputs); i < len(p.Submissions); i++ {
		sub := p.Submissions[i]
		if sub.WorkspaceID != p.WorkspaceID || sub.RunID == "" || sub.EvidenceRef == "" || sub.RetainedRevision == "" {
			candidate.Error = "invalid submission"
			return zero, s.prepareFailure(ctx, rec, candidate, fmt.Errorf("%w: submission %d", ErrInvalidRequest, i))
		}
		if !validObjectIDLoose(sub.RetainedRevision) {
			candidate.Error = "invalid retained revision"
			return zero, s.prepareFailure(ctx, rec, candidate, fmt.Errorf("%w: retained revision", ErrInvalidRequest))
		}
		input, e := s.prepareInput(ctx, domain.WorkspaceID(p.WorkspaceID), candidate, i, sub, p.RequiredSources)
		if e != nil {
			candidate.Error = e.Error()
			return zero, s.prepareFailure(ctx, rec, candidate, e)
		}
		candidate.Inputs = append(candidate.Inputs, input)
		if e := s.save(ctx, rec, candidate); e != nil {
			return zero, e
		}
	}
	if _, e := s.git.CandidateCheckout(ctx, domain.WorkspaceID(p.WorkspaceID), candidate.CandidateID, candidate.ExpectedTargetRevision); e != nil {
		candidate.Error = e.Error()
		return zero, s.prepareFailure(ctx, rec, candidate, e)
	}
	revInputs := make([]gitengine.CandidateRevisionInput, len(candidate.Inputs))
	for i, in := range candidate.Inputs {
		revInputs[i] = gitengine.CandidateRevisionInput{Revision: in.Submission.RetainedRevision, Base: in.BaseRevision}
	}
	assembled, err := s.git.AssembleCandidate(ctx, domain.WorkspaceID(p.WorkspaceID), candidate.CandidateID, revInputs)
	if err != nil && !errors.Is(err, gitengine.ErrCandidateConflict) {
		candidate.Error = err.Error()
		return zero, s.prepareFailure(ctx, rec, candidate, err)
	}
	candidate.CandidateRevision = assembled.Revision
	candidate.Conflicts = append([]string(nil), assembled.Conflicts...)
	candidate.AppliedInputs = assembled.AppliedInputs
	if len(candidate.Conflicts) > 0 {
		candidate.State = protocol.CandidateConflicted
	} else if candidate.AppliedInputs == len(candidate.Inputs) {
		candidate.State = protocol.CandidateFrozen
	} else {
		candidate.State = protocol.CandidatePreparing
	}
	candidate.Error = ""
	if err := s.save(ctx, rec, candidate); err != nil {
		return zero, err
	}
	return *candidate, nil
}

func (s *Service) prepareFailure(ctx context.Context, rec *store.IntegrationCandidate, candidate *protocol.Candidate, original error) error {
	if original == nil {
		return nil
	}
	if saveErr := s.save(ctx, rec, candidate); saveErr != nil {
		return errors.Join(original, fmt.Errorf("integration: persist prepare failure: %w", saveErr))
	}
	return original
}

func validObjectIDLoose(v string) bool {
	return len(v) >= 7 && len(v) <= 128 && !strings.ContainsAny(v, "/\\\x00")
}

func (s *Service) prepareInput(ctx context.Context, ws domain.WorkspaceID, c *protocol.Candidate, index int, sub protocol.SubmissionRef, required []string) (protocol.CandidateInput, error) {
	var out protocol.CandidateInput
	err := s.evidence.WithCandidateSource(ctx, ws, sub.EvidenceRef, func(packet *store.EvidencePacket, captureKey string, transcript io.ReadCloser) error {
		if packet == nil || packet.WorkspaceID != ws || packet.RunID != domain.RunID(sub.RunID) || packet.ID != sub.EvidenceRef {
			return fmt.Errorf("%w: authoritative evidence mismatch", ErrConflict)
		}
		if packet.Availability != store.EvidenceAvailable || packet.RetainedRevision != sub.RetainedRevision {
			return fmt.Errorf("%w: evidence revision unavailable", ErrUnavailable)
		}
		pp := protocol.EvidencePacketFromStore(packet)
		snapshot, _ := json.Marshal(pp)
		if len(required) == 0 {
			found := false
			for _, source := range pp.Sources {
				if source.Name == "git" {
					found = true
					if !source.Available || source.Truncated {
						return fmt.Errorf("%w: git source unavailable", ErrUnavailable)
					}
				}
			}
			if !found {
				return fmt.Errorf("%w: git source missing", ErrUnavailable)
			}
		}
		total := len(snapshot)
		for _, prior := range c.Inputs {
			b, _ := json.Marshal(prior.Packet)
			total += len(b)
		}
		if total > protocol.IntegrationMaxSourceSnapshotBytes {
			return fmt.Errorf("%w: source snapshots exceed bound", ErrInvalidRequest)
		}
		for _, name := range required {
			found := false
			for _, source := range pp.Sources {
				if source.Name == name {
					found = true
					if !source.Available || source.Truncated {
						return fmt.Errorf("%w: source %s unavailable", ErrUnavailable, name)
					}
				}
			}
			if !found {
				return fmt.Errorf("%w: source %s missing", ErrUnavailable, name)
			}
		}
		out = protocol.CandidateInput{Submission: sub, BaseRevision: packet.BaseRevision, Packet: pp}
		if packet.BaseRevision == "" || !validObjectIDLoose(packet.BaseRevision) {
			return fmt.Errorf("%w: missing base revision", ErrInvalidRequest)
		}
		if e := s.git.RetainCandidateInput(ctx, ws, c.CandidateID, index, captureKey, sub.RetainedRevision, packet.BaseRevision); e != nil {
			return e
		}
		if transcript != nil {
			checksum, e := s.writeTranscriptReader(c.CandidateID, index, transcript)
			if e != nil {
				return e
			}
			out.TranscriptChecksum = checksum
			out.TranscriptID = fmt.Sprintf("%d.transcript", index)
		}
		return nil
	})
	if err != nil {
		return protocol.CandidateInput{}, err
	}
	return out, nil
}

func (s *Service) writeTranscriptReader(id string, index int, src io.Reader) (string, error) {
	if s.root == "" {
		return "", fmt.Errorf("%w: artifact root unavailable", ErrUnavailable)
	}
	path := candidateArtifactPath(s.root, id, index)
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, copyErr := io.CopyN(io.MultiWriter(f, h), src, evidence.MaxTranscriptBytes+1)
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if n > evidence.MaxTranscriptBytes {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("%w: transcript exceeds limit", ErrInvalidRequest)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return "", err
	}
	if err = d.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) Show(ctx context.Context, actor Actor, p protocol.IntegrationShowParams) (protocol.Candidate, error) {
	l := s.lock(p.CandidateID)
	l.Lock()
	defer l.Unlock()
	rec, c, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.Candidate{}, err
	}
	release, err := s.authorize(ctx, actor, c, protocol.MethodIntegrationShow)
	if err != nil {
		return protocol.Candidate{}, err
	}
	defer release()
	if sourceErr := s.validateOwned(ctx, c); sourceErr != nil && !errors.Is(sourceErr, ErrExpired) &&
		c.State != protocol.CandidateDeleting && c.State != protocol.CandidateExpired {
		c.State = protocol.CandidateUnavailable
		c.Error = sourceErr.Error()
		if se := s.save(ctx, rec, c); se != nil {
			return protocol.Candidate{}, se
		}
	}
	return *c, nil
}

func (s *Service) List(ctx context.Context, actor Actor, p protocol.IntegrationListParams) (protocol.IntegrationListResult, error) {
	var out protocol.IntegrationListResult
	if p.WorkspaceID == "" {
		return out, fmt.Errorf("%w: workspace", ErrInvalidRequest)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = protocol.IntegrationDefaultPageSize
	}
	if limit > protocol.IntegrationMaxPageSize {
		limit = protocol.IntegrationMaxPageSize
	}
	ws := domain.WorkspaceID(p.WorkspaceID)
	release, err := s.authorizeWorkspace(ctx, actor, ws, protocol.MethodIntegrationList, "", nil, nil)
	if err != nil {
		return out, err
	}
	defer release()
	rows, err := s.store.ListIntegrationCandidates(ctx, ws, limit)
	if err != nil {
		return out, err
	}
	out.Candidates = make([]protocol.CandidateSummary, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		var req *protocol.DeliveryRequest
		if len(r.DeliveryRequest) > 0 {
			var v protocol.DeliveryRequest
			if json.Unmarshal(r.DeliveryRequest, &v) == nil {
				req = &v
			}
		}
		var receipt *protocol.DeliveryReceipt
		if len(r.DeliveryReceipt) > 0 {
			var v protocol.DeliveryReceipt
			if json.Unmarshal(r.DeliveryReceipt, &v) == nil {
				receipt = &v
			}
		}
		out.Candidates = append(out.Candidates, protocol.CandidateSummary{CandidateID: r.CandidateID, WorkspaceID: string(r.WorkspaceID), State: protocol.CandidateState(r.State), CandidateRevision: r.CandidateRevision, TargetRef: r.TargetRef, ExpectedTargetRevision: r.ExpectedTargetRevision, DeliveryRequest: req, DeliveryReceipt: receipt, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt})
	}
	return out, nil
}

func (s *Service) Resolve(ctx context.Context, actor Actor, p protocol.IntegrationResolveParams) (protocol.Candidate, error) {
	if p.IdempotencyKey == "" {
		return protocol.Candidate{}, fmt.Errorf("%w: idempotency key required", ErrInvalidRequest)
	}
	if len(p.Files) > protocol.IntegrationMaxSubmissions {
		return protocol.Candidate{}, fmt.Errorf("%w: too many resolutions", ErrInvalidRequest)
	}
	files := make([]gitengine.CandidateResolution, len(p.Files))
	for i, f := range p.Files {
		if len(f.Path) > protocol.IntegrationMaxPathBytes {
			return protocol.Candidate{}, fmt.Errorf("%w: resolution path too long", ErrInvalidRequest)
		}
		files[i] = gitengine.CandidateResolution{Path: f.Path, Content: f.Content, Delete: f.Delete}
	}
	if err := gitengine.ValidateCandidateResolutions(files); err != nil {
		return protocol.Candidate{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	lock := s.lock(p.CandidateID)
	lock.Lock()
	defer lock.Unlock()
	rec, c, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.Candidate{}, err
	}
	release, err := s.authorize(ctx, actor, c, protocol.MethodIntegrationResolve)
	if err != nil {
		return protocol.Candidate{}, err
	}
	defer release()
	dig := digest(p)
	mutation, found, me := mutationFor(c, actor, protocol.MethodIntegrationResolve, p.IdempotencyKey, dig)
	if me != nil {
		return protocol.Candidate{}, me
	}
	if found && mutation.ResultID != "" {
		return *c, nil
	}
	if c.State != protocol.CandidateConflicted {
		return protocol.Candidate{}, fmt.Errorf("%w: candidate is not conflicted", ErrConflict)
	}
	if err := s.validateOwned(ctx, c); err != nil {
		return protocol.Candidate{}, err
	}
	if !found {
		for _, m := range c.Mutations {
			if m.Operation == protocol.MethodIntegrationResolve && m.ResultID == "" {
				return protocol.Candidate{}, fmt.Errorf("%w: resolution already pending", ErrConflict)
			}
		}
		if err := appendMutation(c, actor, protocol.MethodIntegrationResolve, p.IdempotencyKey, dig, ""); err != nil {
			return protocol.Candidate{}, err
		}
		if err := s.save(ctx, rec, c); err != nil {
			return protocol.Candidate{}, err
		}
	}
	a, gitErr := s.git.ResolveCandidate(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID, files)
	if gitErr != nil && !errors.Is(gitErr, gitengine.ErrCandidateConflict) && !errors.Is(gitErr, gitengine.ErrCandidateFrozen) {
		if errors.Is(gitErr, gitengine.ErrCandidateNotFound) {
			for i := range c.Mutations {
				m := c.Mutations[i]
				if m.ActorKey == actorKey(actor) && m.Operation == protocol.MethodIntegrationResolve &&
					m.IdempotencyKey == p.IdempotencyKey && m.Digest == dig {
					c.Mutations = append(c.Mutations[:i], c.Mutations[i+1:]...)
					_ = s.save(ctx, rec, c)
					break
				}
			}
		}
		return protocol.Candidate{}, gitErr
	}
	c.CandidateRevision = a.Revision
	c.Conflicts = append([]string(nil), a.Conflicts...)
	c.AppliedInputs = a.AppliedInputs
	if len(c.Conflicts) == 0 && c.AppliedInputs == len(c.Inputs) {
		c.State = protocol.CandidateFrozen
	} else {
		c.State = protocol.CandidateConflicted
	}
	for i := range c.Mutations {
		m := &c.Mutations[i]
		if m.ActorKey == actorKey(actor) && m.Operation == protocol.MethodIntegrationResolve &&
			m.IdempotencyKey == p.IdempotencyKey && m.Digest == dig {
			m.ResultID = c.CandidateRevision
			break
		}
	}
	if err := s.save(ctx, rec, c); err != nil {
		return protocol.Candidate{}, err
	}
	return *c, nil
}

func (s *Service) Delete(ctx context.Context, actor Actor, p protocol.IntegrationDeleteParams) error {
	lock := s.lock(p.CandidateID)
	lock.Lock()
	defer lock.Unlock()
	rec, c, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return err
	}
	release, err := s.authorize(ctx, actor, c, protocol.MethodIntegrationDelete)
	if err != nil {
		return err
	}
	defer release()
	if c.State == protocol.CandidateExpired || c.State == protocol.CandidateDeleting {
		return nil
	}
	c.State = protocol.CandidateDeleting
	if e := s.save(ctx, rec, c); e != nil {
		return e
	} // durable fence first
	if candidateActive(c, s.verificationActive) {
		s.cancelCandidate(c)
		return nil
	}
	if e := s.recoverVerifications(ctx, c); e != nil {
		return e
	}
	rec, c, err = s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return err
	}
	if err := s.git.RemoveCandidate(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID); err != nil {
		return err
	}
	if s.root != "" {
		if err := os.RemoveAll(filepath.Join(s.root, "candidates", c.CandidateID)); err != nil {
			return err
		}
	}
	c.State = protocol.CandidateExpired
	c.Error = "candidate deleted"
	c.ExpiresAt = s.nowTime().Add(protocol.IntegrationTombstoneRetention)
	return s.save(ctx, rec, c)
}
func (s *Service) Patch(ctx context.Context, actor Actor, p protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error) {
	_, c, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.IntegrationPatchResult{}, err
	}
	release, err := s.authorize(ctx, actor, c, protocol.MethodIntegrationPatch)
	if err != nil {
		return protocol.IntegrationPatchResult{}, err
	}
	defer release()
	if e := s.validateOwned(ctx, c); e != nil {
		return protocol.IntegrationPatchResult{}, e
	}
	if c.State != protocol.CandidateFrozen || c.CandidateRevision == "" {
		return protocol.IntegrationPatchResult{}, fmt.Errorf("%w: candidate is not frozen", ErrConflict)
	}
	patch, err := s.git.RenderCandidatePatch(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID, c.CandidateRevision, gitengine.DefaultPatchBytes)
	if err != nil {
		return protocol.IntegrationPatchResult{}, err
	}
	return protocol.IntegrationPatchResult{Patch: patch.Text, Truncated: patch.Truncated}, nil
}
