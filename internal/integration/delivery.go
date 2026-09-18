package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

var (
	errDeliveryRequestConflict = errors.New("integration: delivery request idempotency conflict")
	errDeliveryRequestExpired  = errors.New("integration: delivery request expired")
	errDeliveryVerification    = errors.New("integration: required verification is not valid")
)

// RequestDelivery records an immutable delivery request against the exact
// candidate revision and verification observations supplied by the caller.
// The request is intentionally persisted as part of the candidate aggregate;
// replacing it is the only way to change any reviewed input.
func (s *Service) RequestDelivery(ctx context.Context, actor Actor, p protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Candidate{}, err
	}
	if err := validateDeliveryIdempotencyKey(p.IdempotencyKey); err != nil {
		return protocol.Candidate{}, err
	}
	if len(p.VerificationIDs) == 0 || len(p.VerificationIDs) > protocol.IntegrationMaxVerifications {
		return protocol.Candidate{}, fmt.Errorf("integration: verification_ids must contain 1-%d items", protocol.IntegrationMaxVerifications)
	}
	if p.Action != protocol.DeliveryActionUpdateRef && p.Action != protocol.DeliveryActionProposal {
		return protocol.Candidate{}, fmt.Errorf("integration: unsupported delivery action %q", p.Action)
	}

	mu := s.lock(p.CandidateID)
	mu.Lock()
	defer mu.Unlock()

	record, candidate, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.Candidate{}, err
	}
	release, err := s.authorize(ctx, actor, candidate, protocol.MethodIntegrationRequestDelivery)
	if err != nil {
		return protocol.Candidate{}, err
	}
	defer release()
	if candidate.WorkspaceID != p.WorkspaceID || candidate.CandidateID != p.CandidateID {
		return protocol.Candidate{}, errors.New("integration: candidate identity mismatch")
	}
	digest, err := deliveryRequestDigest(p)
	if err != nil {
		return protocol.Candidate{}, err
	}
	if _, found, e := mutationFor(candidate, actor, protocol.MethodIntegrationRequestDelivery, p.IdempotencyKey, digest); e != nil {
		return protocol.Candidate{}, errors.Join(errDeliveryRequestConflict, e)
	} else if found {
		return *candidate, nil
	}
	if candidate.State != protocol.CandidateFrozen {
		return protocol.Candidate{}, fmt.Errorf("integration: candidate is not frozen")
	}
	if p.CandidateRevision == "" || p.CandidateRevision != candidate.CandidateRevision {
		return protocol.Candidate{}, errors.New("integration: candidate revision changed")
	}
	if e := validateCandidateDeliveryBinding(candidate, p.Action); e != nil {
		return protocol.Candidate{}, e
	}
	if existing := candidate.DeliveryRequest; existing != nil {
		switch existing.State {
		case protocol.DeliveryDelivering, protocol.DeliveryDelivered:
			return protocol.Candidate{}, errors.New("integration: delivery request is already executing")
		case protocol.DeliveryPending, protocol.DeliveryApproved, protocol.DeliveryDenied:
			if existing.RequestVersion <= 0 {
				return protocol.Candidate{}, errors.New("integration: invalid delivery request version")
			}
		default:
			return protocol.Candidate{}, errors.New("integration: invalid delivery request state")
		}
	}
	if e := s.validateOwned(ctx, candidate); e != nil {
		return protocol.Candidate{}, e
	}
	if verificationErr := s.validateDeliveryVerifications(ctx, candidate, p.CandidateRevision, p.VerificationIDs, s.nowTime()); verificationErr != nil {
		return protocol.Candidate{}, verificationErr
	}

	requestID, err := newDeliveryID()
	if err != nil {
		return protocol.Candidate{}, err
	}
	now := s.nowTime()
	expires := now.Add(protocol.IntegrationVerificationLifetime)
	if candidate.ExpiresAt.Before(expires) {
		expires = candidate.ExpiresAt
	}
	for _, id := range p.VerificationIDs {
		for _, v := range candidate.Verifications {
			if v.VerificationID == id && v.ExpiresAt.Before(expires) {
				expires = v.ExpiresAt
			}
		}
	}
	if !expires.After(now) {
		return protocol.Candidate{}, errDeliveryRequestExpired
	}
	requestVersion := int64(1)
	if existing := candidate.DeliveryRequest; existing != nil {
		requestVersion = existing.RequestVersion + 1
	}
	candidate.DeliveryRequest = &protocol.DeliveryRequest{
		RequestID:              requestID,
		RequestVersion:         requestVersion,
		CandidateRevision:      candidate.CandidateRevision,
		VerificationIDs:        append([]string(nil), p.VerificationIDs...),
		TargetRef:              candidate.TargetRef,
		ExpectedTargetRevision: candidate.ExpectedTargetRevision,
		Action:                 p.Action,
		State:                  protocol.DeliveryPending,
		RequestedBy:            string(actor.MemberID),
		CreatedAt:              now,
		ExpiresAt:              expires,
	}
	if err := appendMutation(candidate, actor, protocol.MethodIntegrationRequestDelivery, p.IdempotencyKey, digest, requestID); err != nil {
		return protocol.Candidate{}, err
	}
	if err := s.save(ctx, record, candidate); err != nil {
		return protocol.Candidate{}, err
	}
	return *candidate, nil
}

func (s *Service) Decide(ctx context.Context, actor Actor, p protocol.IntegrationDecideParams) (protocol.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Candidate{}, err
	}
	if actor.RunID != "" || actor.MemberID == "" {
		return protocol.Candidate{}, errors.New("integration: delivery decisions require a human actor")
	}
	mu := s.lock(p.CandidateID)
	mu.Lock()
	defer mu.Unlock()

	record, candidate, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.Candidate{}, err
	}
	release, err := s.authorize(ctx, actor, candidate, protocol.MethodIntegrationDecide)
	if err != nil {
		return protocol.Candidate{}, err
	}
	defer release()
	r := candidate.DeliveryRequest
	if r == nil || r.RequestID != p.RequestID {
		return protocol.Candidate{}, errors.New("integration: delivery request not found")
	}
	if p.RequestVersion != r.RequestVersion {
		return protocol.Candidate{}, errors.New("integration: stale delivery request")
	}
	if e := validateDeliveryRequestBinding(candidate, r); e != nil {
		return protocol.Candidate{}, e
	}
	if r.State != protocol.DeliveryPending {
		if (r.State == protocol.DeliveryApproved && p.Approve) || (r.State == protocol.DeliveryDenied && !p.Approve) {
			return *candidate, nil
		}
		return protocol.Candidate{}, errors.New("integration: delivery request already decided")
	}
	if candidate.State != protocol.CandidateFrozen {
		return protocol.Candidate{}, errors.New("integration: candidate is not frozen")
	}
	if err := s.validateOwned(ctx, candidate); err != nil {
		return protocol.Candidate{}, err
	}
	if !r.ExpiresAt.After(s.nowTime()) {
		return protocol.Candidate{}, errDeliveryRequestExpired
	}
	if err := s.validateDeliveryVerifications(ctx, candidate, r.CandidateRevision, r.VerificationIDs, s.nowTime()); err != nil {
		return protocol.Candidate{}, err
	}
	now := s.nowTime()
	if p.Approve {
		r.State = protocol.DeliveryApproved
	} else {
		r.State = protocol.DeliveryDenied
	}
	r.DecidedBy = string(actor.MemberID)
	r.DecidedAt = &now
	if err := s.save(ctx, record, candidate); err != nil {
		return protocol.Candidate{}, err
	}
	return *candidate, nil
}

// Deliver executes an approved request through Git's atomic receipt
// transaction. The delivering state is durable before Git is called, so a
// lost database response can be reconciled by replaying the private receipt.
func (s *Service) Deliver(ctx context.Context, actor Actor, p protocol.IntegrationDeliverParams) (protocol.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Candidate{}, err
	}
	mu := s.lock(p.CandidateID)
	mu.Lock()
	defer mu.Unlock()

	record, candidate, err := s.load(ctx, p.WorkspaceID, p.CandidateID)
	if err != nil {
		return protocol.Candidate{}, err
	}
	// Authenticate the current caller before inspecting any replay path.
	release, err := s.authorize(ctx, actor, candidate, protocol.MethodIntegrationDeliver)
	if err != nil {
		return protocol.Candidate{}, err
	}
	defer release()
	r := candidate.DeliveryRequest
	if r == nil || r.RequestID != p.RequestID {
		return protocol.Candidate{}, errors.New("integration: delivery request not found")
	}
	if p.RequestVersion != r.RequestVersion {
		return protocol.Candidate{}, errors.New("integration: stale delivery request")
	}
	if e := validateDeliveryRequestBinding(candidate, r); e != nil {
		return protocol.Candidate{}, e
	}

	// Once the aggregate receipt is durable, it is the authoritative result.
	// Caller authorization above is still required for every replay.
	if r.State == protocol.DeliveryDelivered {
		if candidate.DeliveryReceipt == nil {
			return protocol.Candidate{}, errors.New("integration: delivered request has no receipt")
		}
		if e := validateDeliveryAggregateReceipt(r, candidate.DeliveryReceipt); e != nil {
			return protocol.Candidate{}, e
		}
		return *candidate, nil
	}

	// A delivering request may have committed Git before its aggregate save.
	// Probe the private receipt read-only; a miss must proceed through all
	// current approval and source gates before a new Git action.
	if r.State == protocol.DeliveryDelivering {
		if s.git == nil {
			return protocol.Candidate{}, fmt.Errorf("%w: git delivery unavailable", ErrUnavailable)
		}
		gitReceipt, found, lookupErr := s.git.LookupCandidateDelivery(
			ctx,
			domain.WorkspaceID(candidate.WorkspaceID),
			candidate.CandidateID,
			r.RequestID,
			string(r.Action),
			r.TargetRef,
			r.ExpectedTargetRevision,
			r.CandidateRevision,
		)
		if lookupErr != nil {
			return protocol.Candidate{}, lookupErr
		}
		if found {
			if e := validateGitDeliveryReceipt(r, gitReceipt); e != nil {
				return protocol.Candidate{}, e
			}
			return s.finishDelivery(ctx, record, candidate, gitReceipt)
		}
	} else if r.State != protocol.DeliveryApproved {
		return protocol.Candidate{}, errors.New("integration: delivery request is not approved")
	}

	if r.State == protocol.DeliveryDelivering {
		r.State = protocol.DeliveryApproved
		if saveErr := s.save(ctx, record, candidate); saveErr != nil {
			return protocol.Candidate{}, saveErr
		}
	}

	if candidate.State != protocol.CandidateFrozen {
		return protocol.Candidate{}, errors.New("integration: candidate is not frozen")
	}

	// No durable Git receipt exists, so the original approval is not enough:
	// every mutable source, expiry, verification, target, and approver gate is
	// checked again before invoking the mutating Git operation.
	if ownedErr := s.validateOwned(ctx, candidate); ownedErr != nil {
		return protocol.Candidate{}, ownedErr
	}
	if !r.ExpiresAt.After(s.nowTime()) {
		return protocol.Candidate{}, errDeliveryRequestExpired
	}
	if verificationErr := s.validateDeliveryVerifications(ctx, candidate, r.CandidateRevision, r.VerificationIDs, s.nowTime()); verificationErr != nil {
		return protocol.Candidate{}, verificationErr
	}
	if approverErr := s.validateCurrentApprover(ctx, candidate, r.DecidedBy); approverErr != nil {
		return protocol.Candidate{}, approverErr
	}
	if s.git == nil {
		return protocol.Candidate{}, fmt.Errorf("%w: git delivery unavailable", ErrUnavailable)
	}

	if r.State == protocol.DeliveryApproved {
		r.State = protocol.DeliveryDelivering
		if saveErr := s.save(ctx, record, candidate); saveErr != nil {
			return protocol.Candidate{}, saveErr
		}
	}
	gitReceipt, err := s.git.DeliverCandidate(
		ctx,
		domain.WorkspaceID(candidate.WorkspaceID),
		candidate.CandidateID,
		r.RequestID,
		string(r.Action),
		r.TargetRef,
		r.ExpectedTargetRevision,
		r.CandidateRevision,
	)
	if err != nil {
		// A Git error is ambiguous until the exact private receipt is read.
		// Never mutate Git again while resolving this outcome.
		receipt, found, lookupErr := s.git.LookupCandidateDelivery(
			ctx,
			domain.WorkspaceID(candidate.WorkspaceID),
			candidate.CandidateID,
			r.RequestID,
			string(r.Action),
			r.TargetRef,
			r.ExpectedTargetRevision,
			r.CandidateRevision,
		)
		if lookupErr != nil {
			return protocol.Candidate{}, errors.Join(err, lookupErr)
		}
		if found {
			if receiptErr := validateGitDeliveryReceipt(r, receipt); receiptErr != nil {
				return protocol.Candidate{}, errors.Join(err, receiptErr)
			}
			return s.finishDelivery(ctx, record, candidate, receipt)
		}
		// A clean, exact receipt miss is a definitive pre-transaction
		// failure. Release the durable delivering fence without changing the
		// request version; a replacement request can then advance it.
		r.State = protocol.DeliveryApproved
		if saveErr := s.save(ctx, record, candidate); saveErr != nil {
			return protocol.Candidate{}, errors.Join(err, saveErr)
		}
		return protocol.Candidate{}, err
	}
	return s.finishDelivery(ctx, record, candidate, gitReceipt)
}

func (s *Service) finishDelivery(ctx context.Context, record *store.IntegrationCandidate, candidate *protocol.Candidate, gitReceipt gitengine.CandidateGitReceipt) (protocol.Candidate, error) {
	r := candidate.DeliveryRequest
	if r == nil {
		return protocol.Candidate{}, errors.New("integration: delivery request not found")
	}
	if err := validateGitDeliveryReceipt(r, gitReceipt); err != nil {
		return protocol.Candidate{}, err
	}
	result := protocol.DeliveryResultLanded
	if gitReceipt.Result == "proposed" {
		result = protocol.DeliveryResultProposed
	}
	candidate.DeliveryReceipt = &protocol.DeliveryReceipt{
		ReceiptID:         r.RequestID,
		RequestID:         r.RequestID,
		CandidateRevision: r.CandidateRevision,
		TargetRef:         r.TargetRef,
		PreviousRevision:  gitReceipt.PreviousRevision,
		Action:            r.Action,
		Result:            result,
		ProposalRef:       gitReceipt.ProposalRef,
		CreatedAt:         s.nowTime(),
	}
	r.State = protocol.DeliveryDelivered
	if err := s.save(ctx, record, candidate); err != nil {
		// The private Git receipt is durable. A retry loads delivering and
		// replays that receipt without issuing a second mutation.
		return protocol.Candidate{}, err
	}
	return *candidate, nil
}

func validateDeliveryIdempotencyKey(key string) error {
	if key == "" || len([]byte(key)) > protocol.IntegrationMaxIdempotencyKeyBytes || strings.TrimSpace(key) != key {
		return errors.New("integration: invalid idempotency key")
	}
	return nil
}

func deliveryRequestDigest(p protocol.IntegrationRequestDeliveryParams) (string, error) {
	b, err := json.Marshal(struct {
		WorkspaceID       string                  `json:"workspace_id"`
		CandidateID       string                  `json:"candidate_id"`
		CandidateRevision string                  `json:"candidate_revision"`
		VerificationIDs   []string                `json:"verification_ids"`
		Action            protocol.DeliveryAction `json:"action"`
	}{p.WorkspaceID, p.CandidateID, p.CandidateRevision, p.VerificationIDs, p.Action})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func validateCandidateDeliveryBinding(c *protocol.Candidate, action protocol.DeliveryAction) error {
	if c == nil {
		return errors.New("integration: candidate is missing")
	}
	if c.CandidateRevision == "" || !validObjectIDLoose(c.CandidateRevision) {
		return errors.New("integration: invalid candidate revision")
	}
	if !validRef(c.TargetRef) {
		return errors.New("integration: invalid delivery target")
	}
	if c.ExpectedTargetRevision != "" && !validObjectIDLoose(c.ExpectedTargetRevision) {
		return errors.New("integration: invalid expected target revision")
	}
	if action != protocol.DeliveryActionUpdateRef && action != protocol.DeliveryActionProposal {
		return fmt.Errorf("integration: unsupported delivery action %q", action)
	}
	return nil
}

func validateDeliveryRequestBinding(c *protocol.Candidate, r *protocol.DeliveryRequest) error {
	if c == nil || r == nil {
		return errors.New("integration: delivery request not found")
	}
	if err := validateCandidateDeliveryBinding(c, r.Action); err != nil {
		return err
	}
	if r.RequestID == "" || r.RequestVersion <= 0 {
		return errors.New("integration: invalid delivery request")
	}
	if r.CandidateRevision != c.CandidateRevision ||
		r.TargetRef != c.TargetRef ||
		r.ExpectedTargetRevision != c.ExpectedTargetRevision {
		return errors.New("integration: delivery request binding changed")
	}
	if len(r.VerificationIDs) == 0 || len(r.VerificationIDs) > protocol.IntegrationMaxVerifications {
		return fmt.Errorf("integration: verification_ids must contain 1-%d items", protocol.IntegrationMaxVerifications)
	}
	return nil
}

func validateGitDeliveryReceipt(r *protocol.DeliveryRequest, receipt gitengine.CandidateGitReceipt) error {
	if r == nil {
		return errors.New("integration: delivery request not found")
	}
	if receipt.Revision != r.CandidateRevision {
		return errors.New("integration: Git receipt revision does not match request")
	}
	if r.ExpectedTargetRevision != "" && receipt.PreviousRevision != r.ExpectedTargetRevision {
		return errors.New("integration: Git receipt previous revision does not match request")
	}
	switch r.Action {
	case protocol.DeliveryActionUpdateRef:
		if receipt.Result != "landed" || receipt.ProposalRef != "" {
			return errors.New("integration: invalid landed Git receipt")
		}
	case protocol.DeliveryActionProposal:
		if receipt.Result != "proposed" || receipt.ProposalRef == "" {
			return errors.New("integration: invalid proposed Git receipt")
		}
	default:
		return fmt.Errorf("integration: unsupported delivery action %q", r.Action)
	}
	return nil
}

func validateDeliveryAggregateReceipt(r *protocol.DeliveryRequest, receipt *protocol.DeliveryReceipt) error {
	if r == nil || receipt == nil {
		return errors.New("integration: delivered request has no receipt")
	}
	if receipt.ReceiptID != r.RequestID ||
		receipt.RequestID != r.RequestID ||
		receipt.CandidateRevision != r.CandidateRevision ||
		receipt.TargetRef != r.TargetRef ||
		receipt.Action != r.Action ||
		(r.ExpectedTargetRevision != "" && receipt.PreviousRevision != r.ExpectedTargetRevision) {
		return errors.New("integration: delivery receipt binding changed")
	}
	switch r.Action {
	case protocol.DeliveryActionUpdateRef:
		if receipt.Result != protocol.DeliveryResultLanded || receipt.ProposalRef != "" {
			return errors.New("integration: invalid landed delivery receipt")
		}
	case protocol.DeliveryActionProposal:
		if receipt.Result != protocol.DeliveryResultProposed || receipt.ProposalRef == "" {
			return errors.New("integration: invalid proposed delivery receipt")
		}
	default:
		return fmt.Errorf("integration: unsupported delivery action %q", r.Action)
	}
	return nil
}

func (s *Service) validateCurrentApprover(ctx context.Context, c *protocol.Candidate, memberID string) error {
	if c == nil || memberID == "" {
		return errors.New("integration: delivery request has no approver")
	}
	ws, err := s.store.GetWorkspace(ctx, domain.WorkspaceID(c.WorkspaceID))
	if err != nil {
		return err
	}
	if ws == nil {
		return store.ErrNotFound
	}
	member, err := s.store.GetMember(ctx, domain.MemberID(memberID))
	if err != nil {
		return err
	}
	if member == nil || member.Pending {
		return errors.New("integration: approver no longer has delivery permission")
	}
	if err := permissions.Check(
		permissions.Push,
		permissions.Actor{ID: member.ID, Role: member.Role},
		permissions.Target{Workspace: ws.ID, SteerOthers: ws.SteerOthers},
	); err != nil {
		return errors.New("integration: approver no longer has delivery permission")
	}
	return nil
}

func validateVerificationSelection(c *protocol.Candidate, revision string, ids []string, now time.Time) error {
	if c == nil {
		return fmt.Errorf("%w: candidate is missing", errDeliveryVerification)
	}
	if len(ids) == 0 || len(ids) > protocol.IntegrationMaxVerifications {
		return fmt.Errorf("%w: verification selection must contain 1-%d items", errDeliveryVerification, protocol.IntegrationMaxVerifications)
	}
	seen := make(map[string]struct{}, len(ids))
	type selectedVerification struct {
		id    string
		index int
	}
	selected := make([]selectedVerification, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: empty verification id", errDeliveryVerification)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate verification id %q", errDeliveryVerification, id)
		}
		seen[id] = struct{}{}
		found := -1
		for i := range c.Verifications {
			if c.Verifications[i].VerificationID == id {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("%w: %s not found", errDeliveryVerification, id)
		}
		v := c.Verifications[found]
		if v.CandidateRevision != revision ||
			v.Status != protocol.VerificationPassed ||
			v.ExitCode == nil ||
			*v.ExitCode != 0 ||
			!v.ExpiresAt.After(now) {
			return fmt.Errorf("%w: %s is not a current passing verification", errDeliveryVerification, id)
		}
		selected = append(selected, selectedVerification{id: id, index: found})
	}
	// The durable slice order is the verification attempt sequence. Any
	// later failed or in-flight attempt for this candidate revision invalidates
	// an older selected pass, regardless of argv or wall-clock timestamp.
	for _, selected := range selected {
		for i := selected.index + 1; i < len(c.Verifications); i++ {
			later := c.Verifications[i]
			if later.CandidateRevision != revision {
				continue
			}
			switch later.Status {
			case protocol.VerificationRunning, protocol.VerificationFailed,
				protocol.VerificationTimedOut, protocol.VerificationCancelled,
				protocol.VerificationError, protocol.VerificationSourceChanged:
				return fmt.Errorf("%w: newer verification %s invalidates %s", errDeliveryVerification, later.VerificationID, selected.id)
			}
		}
	}
	return nil
}

func (s *Service) validateDeliveryVerifications(_ context.Context, c *protocol.Candidate, revision string, ids []string, now time.Time) error {
	return validateVerificationSelection(c, revision, ids, now)
}

func newDeliveryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("integration: generate delivery request id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
