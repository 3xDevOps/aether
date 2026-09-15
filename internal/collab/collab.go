// Package collab owns the durable run-room service. Room messages are stored
// before any attempt to reach a live PTY; comments, replies, questions, and
// system messages are room-only, while steer requests may be delivered to a
// run's controller or released after the overlap grace period.
package collab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// SteeringDelay is the overlap grace period for steering from a session
	// that does not currently hold the controller lease.
	SteeringDelay = 45 * time.Second
	// DefaultBodyLimit bounds one room message body in bytes.
	DefaultBodyLimit = 64 << 10
	// DefaultAttachmentLimit bounds the number of image references on a
	// message. Individual reference limits are enforced as well.
	DefaultAttachmentLimit  = 8
	DefaultAttachmentBytes  = 512
	DefaultIdempotencyBytes = 256
	DefaultAnchorBytes      = 2048
	DefaultPageSize         = store.DefaultCollaborationPageSize
)

var (
	ErrInvalidRequest    = errors.New("collab: invalid request")
	ErrNotMember         = errors.New("collab: member is not in the workspace")
	ErrInvalidAttachment = errors.New("collab: invalid attachment reference")
	ErrInvalidAnchor     = errors.New("collab: invalid anchor")
	ErrQuestionNotFound  = errors.New("collab: question not found")
	ErrNoInjector        = errors.New("collab: no PTY injector")
	// ErrUnauthorizedDecision means the caller did not present the current
	// connected controller's member, session, and generation together.
	ErrUnauthorizedDecision = errors.New("collab: moderation requires the current controller lease")
	// ErrClosed is returned after the service has been shut down.
	ErrClosed = errors.New("collab: service closed")
)

// Receipt is the durable result of an attempted steer delivery.
type Receipt string

const (
	ReceiptSent      Receipt = "sent"
	ReceiptNotSent   Receipt = "not_sent"
	ReceiptUncertain Receipt = "uncertain"
)

// Injector is the scheduler's actor-aware canonical steering seam. It owns
// PTY acceptance and records the timeline/co-author effects exactly once after
// acceptance.
type Injector func(context.Context, domain.RunID, domain.MemberID, string) error

// AttachmentValidator accepts only an opaque reference to an image already
// known to the image service. A validator is required whenever attachments
// are supplied; the room service never interprets a reference as a filesystem
// path.
type AttachmentValidator func(context.Context, domain.WorkspaceID, domain.RunID, string) error

// AnchorValidator performs any repository/transcript existence check for an
// anchor. The service still applies cheap shape and host-path checks first.
type AnchorValidator func(context.Context, domain.WorkspaceID, domain.RunID, *store.RoomAnchor) error

// Runs is the lookup half of the existing store API.
type Runs interface {
	GetRun(context.Context, domain.RunID) (*domain.Run, error)
	GetMember(context.Context, domain.MemberID) (*domain.Member, error)
	GetWorkspace(context.Context, domain.WorkspaceID) (*domain.Workspace, error)
}

// Workspaces is optional and is used by the overdue worker after a restart.
type Workspaces interface {
	ListWorkspaces(context.Context) ([]*domain.Workspace, error)
}

// ProtectedStore is implemented by store.Store. It is kept separate so a
// narrow in-memory fake can exercise room delivery without protection tests.
type ProtectedStore interface {
	SetRunProtected(context.Context, domain.RunID, bool) error
}

// ClassifyReceipt maps PTY outcomes to the wire contract. Only the two
// explicit no-session errors prove no write occurred; every other write error
// is uncertain.
func ClassifyReceipt(err error) Receipt {
	if err == nil {
		return ReceiptSent
	}
	if errors.Is(err, ptyhost.ErrNoSession) || errors.Is(err, ptyhost.ErrSessionEnded) {
		return ReceiptNotSent
	}
	return ReceiptUncertain
}

// Config wires one room service. Store and Runs are required; Bus, Control,
// Inject, and validators are optional only for the corresponding degraded
// paths. Production delivery uses Inject, which is the scheduler's actor-aware
// canonical seam.
type Config struct {
	Store          store.CollaborationStore
	Runs           Runs
	Workspaces     Workspaces
	Bus            events.Bus
	Control        *control.Service
	Inject         Injector
	Attachments    AttachmentValidator
	Anchor         AnchorValidator
	Now            func() time.Time
	WorkerInterval time.Duration
	// Ready, when supplied, closes only after scheduler recovery has restored
	// persisted runs and their PTY sessions. The overdue worker waits for it
	// before claiming any steer, leaving messages durably queued during boot.
	Ready            <-chan struct{}
	BodyLimit        int
	AttachmentLimit  int
	AttachmentBytes  int
	IdempotencyBytes int
	AnchorBytes      int
}

// MessageInput is the transport-neutral shape of one room mutation.
type MessageInput struct {
	WorkspaceID    domain.WorkspaceID
	RunID          domain.RunID
	ActorID        domain.MemberID
	Kind           store.RoomMessageKind
	Body           string
	Attachments    []string
	Anchor         *store.RoomAnchor
	CorrelationID  string
	IdempotencyKey string
	// ControllerSessionID and ControllerGeneration are required together to
	// prove that a steer came from the current controller session.
	ControllerSessionID  string
	ControllerGeneration uint64
}

// Result contains the persisted message and, for steering, the delivery
// receipt. The zero receipt means the message was room-only.
type Result struct {
	Message *store.RoomMessage
	Receipt Receipt
}

// Status is a bounded room snapshot suitable for transport responses.
type Status struct {
	WorkspaceID   domain.WorkspaceID
	RunID         domain.RunID
	Protected     bool
	Controller    control.Snapshot
	HasController bool
	QueuedSteers  int
}

// Service is restart-safe because all queued state is in CollaborationStore;
// only the overdue worker goroutine is ephemeral.
type Service struct {
	cfg        Config
	now        func() time.Time
	mu         sync.Mutex
	running    bool
	closed     bool
	stop       context.CancelFunc
	workerDone chan struct{}
	overdueErr error
}

// OverdueDeliveryError reports the latest failure observed by the overdue
// worker. The value remains available after the worker retries, so callers
// can observe an asynchronous delivery failure without relying on logs.
func (s *Service) OverdueDeliveryError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overdueErr
}
func (s *Service) recordOverdueError(err error) {
	s.mu.Lock()
	s.overdueErr = err
	s.mu.Unlock()
	slog.Warn("collab: overdue delivery failed", "error", err)
}

// New creates a room service. It does not start the overdue worker; call Start
// once the process has attached its lifecycle context.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Runs == nil {
		return nil, fmt.Errorf("%w: store and runs are required", ErrInvalidRequest)
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.WorkerInterval <= 0 {
		cfg.WorkerInterval = time.Second
	}
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = DefaultBodyLimit
	}
	if cfg.AttachmentLimit <= 0 {
		cfg.AttachmentLimit = DefaultAttachmentLimit
	}
	if cfg.AttachmentBytes <= 0 {
		cfg.AttachmentBytes = DefaultAttachmentBytes
	}
	if cfg.IdempotencyBytes <= 0 {
		cfg.IdempotencyBytes = DefaultIdempotencyBytes
	}
	if cfg.AnchorBytes <= 0 {
		cfg.AnchorBytes = DefaultAnchorBytes
	}
	return &Service{cfg: cfg, now: cfg.Now}, nil
}

// Start performs an immediate overdue sweep and then keeps sweeping until ctx
// is cancelled. Calling Start more than once is harmless.
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.running {
		s.mu.Unlock()
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.running = true
	s.stop = cancel
	s.workerDone = done
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.stop = nil
			if s.workerDone == done {
				s.workerDone = nil
			}
			close(done)
			s.mu.Unlock()
		}()
		if ready := s.cfg.Ready; ready != nil {
			select {
			case <-ready:
			case <-workerCtx.Done():
				return
			}
		}
		if _, err := s.DeliverDue(workerCtx, store.MaxCollaborationPageSize); err != nil {
			s.recordOverdueError(fmt.Errorf("initial overdue delivery: %w", err))
		}
		interval := s.cfg.WorkerInterval
		if interval <= 0 {
			interval = time.Second
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-t.C:
				if _, err := s.DeliverDue(workerCtx, store.MaxCollaborationPageSize); err != nil {
					s.recordOverdueError(fmt.Errorf("periodic overdue delivery: %w", err))
				}
			}
		}
	}()
	return nil
}

// Close stops the overdue worker and waits for it to finish. It is safe to
// call before Start and from multiple callers.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.workerDone
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	s.closed = true
	stop := s.stop
	done := s.workerDone
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Post persists a room message and delivers steering when it is eligible.
func (s *Service) Post(ctx context.Context, in MessageInput) (Result, error) {
	if err := s.validateInput(ctx, &in); err != nil {
		return Result{}, err
	}
	run, actor, ws, err := s.scope(ctx, in.WorkspaceID, in.RunID, in.ActorID)
	if err != nil {
		return Result{}, err
	}
	if in.Kind == store.RoomMessageReply {
		if err := s.validateReply(ctx, in, ws); err != nil {
			return Result{}, err
		}
	}
	// Idempotency is an observation path: a retry must still return its
	// durable result after the author's mutable permission has been revoked.
	if lookup, ok := s.cfg.Store.(interface {
		GetRoomMessageByIdempotency(context.Context, domain.MemberID, domain.RunID, string) (*store.RoomMessage, error)
	}); ok {
		if prior, lookupErr := lookup.GetRoomMessageByIdempotency(ctx, actor.ID, run.ID, in.IdempotencyKey); lookupErr == nil {
			return s.observeExisting(ctx, prior)
		}
	}
	if in.Kind != store.RoomMessageSteerRequest {
		msg := &store.RoomMessage{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: actor.ID, Kind: in.Kind,
			Body: in.Body, Attachments: append([]string(nil), in.Attachments...),
			Anchor: cloneAnchor(in.Anchor), CorrelationID: in.CorrelationID,
			IdempotencyKey: in.IdempotencyKey,
		}
		now := s.now()
		msg.State = store.RoomMessageSent
		msg.DeliveredAt = &now
		if err := s.cfg.Store.CreateRoomMessage(ctx, msg); err != nil {
			return Result{}, err
		}
		if err := s.publishMessage(ctx, msg); err != nil {
			return Result{}, err
		}
		return Result{Message: msg, Receipt: ReceiptSent}, nil
	}

	var (
		msg           *store.RoomMessage
		deliveryProof *control.Snapshot
	)
	createSteer := func(current control.Snapshot, hasController bool) error {
		freshRun, freshActor, freshWS, err := s.scope(ctx, in.WorkspaceID, in.RunID, in.ActorID)
		if err != nil {
			return err
		}
		if freshRun.Protected {
			return fmt.Errorf("%w: protected runs do not accept steering requests", permissions.ErrDenied)
		}
		if err := permissions.Check(permissions.Steer,
			permissions.Actor{ID: freshActor.ID, Role: freshActor.Role},
			permissions.Target{Workspace: freshWS.ID, Owner: freshRun.MemberID, Protected: freshRun.Protected, SteerOthers: freshWS.SteerOthers}); err != nil {
			return err
		}
		run, actor, ws = freshRun, freshActor, freshWS
		msg = &store.RoomMessage{
			WorkspaceID: ws.ID, RunID: run.ID, ActorID: actor.ID, Kind: in.Kind,
			Body: in.Body, Attachments: append([]string(nil), in.Attachments...),
			Anchor: cloneAnchor(in.Anchor), CorrelationID: in.CorrelationID,
			IdempotencyKey: in.IdempotencyKey,
		}
		session := in.ControllerSessionID
		generation := in.ControllerGeneration
		if hasController && current.Connected && current.MemberID == actor.ID &&
			current.SessionID == session && current.Generation == generation {
			proof := current
			deliveryProof = &proof
		} else {
			due := s.now().Add(SteeringDelay)
			msg.DeliverAfter = &due
		}
		return s.cfg.Store.CreateRoomMessage(ctx, msg)
	}
	if s.cfg.Control != nil {
		if err := s.cfg.Control.AdmitSnapshot(string(run.ID), createSteer); err != nil {
			return Result{}, err
		}
	} else if err := createSteer(control.Snapshot{}, false); err != nil {
		return Result{}, err
	}
	if err := s.publishMessage(ctx, msg); err != nil {
		return Result{}, err
	}
	if msg.DeliverAfter != nil {
		return Result{Message: msg}, nil
	}
	return s.deliverResult(ctx, msg, actor, run, false, "", deliveryProof, nil)
}

func (s *Service) observeExisting(ctx context.Context, msg *store.RoomMessage) (Result, error) {
	if msg.Kind != store.RoomMessageSteerRequest && msg.State == store.RoomMessageQueued {
		now := s.now()
		if err := s.cfg.Store.TransitionRoomMessage(ctx, msg.ID, store.RoomMessageSent, &now, nil); err != nil && !errors.Is(err, store.ErrConflict) {
			return Result{}, err
		}
		stored, err := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
		if err != nil {
			return Result{}, err
		}
		msg = stored
		if err := s.publishMessage(ctx, msg); err != nil {
			return Result{}, err
		}
	}
	return Result{Message: msg, Receipt: receiptForState(msg.State)}, nil
}

func (s *Service) validateInput(ctx context.Context, in *MessageInput) error {
	if in.WorkspaceID == "" || in.RunID == "" || in.ActorID == "" || !in.Kind.Valid() || strings.TrimSpace(in.IdempotencyKey) == "" {
		return fmt.Errorf("%w: workspace, run, actor, kind, and idempotency key are required", ErrInvalidRequest)
	}
	if len(in.Body) == 0 || len(in.Body) > s.cfg.BodyLimit {
		return fmt.Errorf("%w: body must be between 1 and %d bytes", ErrInvalidRequest, s.cfg.BodyLimit)
	}
	if len(in.IdempotencyKey) > s.cfg.IdempotencyBytes {
		return fmt.Errorf("%w: idempotency key is too long", ErrInvalidRequest)
	}
	if len(in.Attachments) > s.cfg.AttachmentLimit {
		return fmt.Errorf("%w: too many attachments", ErrInvalidAttachment)
	}
	for _, ref := range in.Attachments {
		if ref == "" || len(ref) > s.cfg.AttachmentBytes || strings.ContainsAny(ref, "\x00\r\n") || s.cfg.Attachments == nil {
			return ErrInvalidAttachment
		}
		if err := s.cfg.Attachments(ctx, in.WorkspaceID, in.RunID, ref); err != nil {
			return ErrInvalidAttachment
		}
	}
	if len(serializeAgentMessage(in.Body, in.Attachments)) > s.agentMessageLimit() {
		return fmt.Errorf("%w: serialized message is too large", ErrInvalidRequest)
	}
	if in.Anchor != nil {
		if err := validateAnchor(in.Anchor, s.cfg.AnchorBytes); err != nil {
			return err
		}
		if s.cfg.Anchor != nil {
			if err := s.cfg.Anchor(ctx, in.WorkspaceID, in.RunID, in.Anchor); err != nil {
				return ErrInvalidAnchor
			}
		}
	}
	return nil
}

func receiptForState(state store.RoomMessageState) Receipt {
	switch state {
	case store.RoomMessageSent:
		return ReceiptSent
	case store.RoomMessageNotSent:
		return ReceiptNotSent
	case store.RoomMessageUncertain:
		return ReceiptUncertain
	default:
		return ""
	}
}
func validateAnchor(a *store.RoomAnchor, max int) error {
	if a == nil || len(a.Kind) > 64 || len(a.Path) > max || strings.ContainsRune(a.Kind, '\x00') || strings.ContainsRune(a.Path, '\x00') || a.StartLine < 0 || a.EndLine < 0 || a.EndLine != 0 && a.StartLine > a.EndLine || a.TranscriptOffset < 0 {
		return ErrInvalidAnchor
	}
	if strings.HasPrefix(a.Path, "/") || strings.HasPrefix(a.Path, "\\") || path.IsAbs(a.Path) {
		return ErrInvalidAnchor
	}
	for _, part := range strings.FieldsFunc(a.Path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return ErrInvalidAnchor
		}
	}
	return nil
}

func cloneAnchor(a *store.RoomAnchor) *store.RoomAnchor {
	if a == nil {
		return nil
	}
	copy := *a
	return &copy
}

func (s *Service) scope(ctx context.Context, workspace domain.WorkspaceID, runID domain.RunID, actorID domain.MemberID) (*domain.Run, *domain.Member, *domain.Workspace, error) {
	run, err := s.cfg.Runs.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	if run.WorkspaceID != workspace {
		return nil, nil, nil, ErrNotMember
	}
	ws, err := s.cfg.Runs.GetWorkspace(ctx, workspace)
	if err != nil {
		return nil, nil, nil, err
	}
	actor, err := s.cfg.Runs.GetMember(ctx, actorID)
	if err != nil {
		return nil, nil, nil, err
	}
	if actor.Pending {
		return nil, nil, nil, ErrNotMember
	}
	return run, actor, ws, nil
}

func (s *Service) validateReply(ctx context.Context, in MessageInput, ws *domain.Workspace) error {
	if in.CorrelationID == "" {
		return ErrQuestionNotFound
	}
	q, err := s.cfg.Store.GetRoomMessage(ctx, in.CorrelationID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQuestionNotFound, err)
	}
	if q.WorkspaceID != ws.ID || q.RunID != in.RunID || q.Kind != store.RoomMessageQuestion {
		return ErrQuestionNotFound
	}
	return nil
}

func (s *Service) authorizeDecision(run domain.RunID, actor domain.MemberID, session string, generation uint64) error {
	if s.cfg.Control == nil || s.cfg.Control.ValidateMember(string(run), actor, session, generation) != nil {
		return ErrUnauthorizedDecision
	}
	return nil
}

func (s *Service) settleRoomMessage(ctx context.Context, msg *store.RoomMessage, receipt Receipt) error {
	var (
		state     store.RoomMessageState
		delivered *time.Time
		failure   *store.RoomMessageFailure
	)
	switch receipt {
	case ReceiptSent:
		state = store.RoomMessageSent
		now := s.now()
		delivered = &now
	case ReceiptNotSent:
		state = store.RoomMessageNotSent
		failure = &store.RoomMessageFailure{Code: "session_unavailable", Message: "run session unavailable"}
	case ReceiptUncertain:
		state = store.RoomMessageUncertain
		failure = &store.RoomMessageFailure{Code: "write_uncertain", Message: "delivery outcome uncertain", Retryable: true}
	default:
		state = store.RoomMessageSent
		failure = &store.RoomMessageFailure{Code: "write_uncertain", Message: "delivery outcome uncertain", Retryable: true}
	}
	if err := s.cfg.Store.TransitionRoomMessage(ctx, msg.ID, state, delivered, failure); err != nil {
		return err
	}
	stored, err := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		return err
	}
	if err := s.publishMessage(ctx, stored); err != nil {
		return err
	}
	return nil
}

// List returns a bounded, newest-first room page after validating run scope.
func (s *Service) List(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, before string, limit int) (*store.RoomMessagePage, error) {
	if workspace == "" {
		return nil, ErrInvalidRequest
	}
	if _, err := s.cfg.Runs.GetWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	if run != "" {
		r, err := s.cfg.Runs.GetRun(ctx, run)
		if err != nil {
			return nil, err
		}
		if r.WorkspaceID != workspace {
			return nil, ErrNotMember
		}
	}
	if limit <= 0 || limit > store.MaxCollaborationPageSize {
		limit = DefaultPageSize
	}
	return s.cfg.Store.ListRoomMessages(ctx, workspace, run, before, limit)
}

// Status returns a bounded summary and never includes storage paths.
func (s *Service) Status(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) (Status, error) {
	r, err := s.cfg.Runs.GetRun(ctx, run)
	if err != nil {
		return Status{}, err
	}
	if r.WorkspaceID != workspace {
		return Status{}, ErrNotMember
	}
	if _, err := s.cfg.Runs.GetWorkspace(ctx, workspace); err != nil {
		return Status{}, err
	}
	result := Status{WorkspaceID: workspace, RunID: run, Protected: r.Protected}
	if s.cfg.Control != nil {
		result.Controller, result.HasController = s.cfg.Control.Status(string(run))
	}
	before := ""
	seen := make(map[string]struct{})
	for {
		if before != "" {
			if _, exists := seen[before]; exists {
				break
			}
			seen[before] = struct{}{}
		}
		page, err := s.cfg.Store.ListRoomMessages(ctx, workspace, run, before, store.MaxCollaborationPageSize)
		if err != nil {
			return Status{}, err
		}
		for _, msg := range page.Items {
			if msg.Kind == store.RoomMessageSteerRequest && msg.State == store.RoomMessageQueued {
				result.QueuedSteers++
			}
		}
		next := page.NextBefore
		if next == "" || len(page.Items) == 0 || next == before {
			break
		}
		before = next
	}
	return result, nil
}

func (s *Service) scopeRunActor(ctx context.Context, runID domain.RunID, actorID domain.MemberID) (*domain.Run, *domain.Member, *domain.Workspace, error) {
	run, err := s.cfg.Runs.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	actor, err := s.cfg.Runs.GetMember(ctx, actorID)
	if err != nil {
		return nil, nil, nil, err
	}
	if actor.Pending {
		return nil, nil, nil, ErrNotMember
	}
	ws, err := s.cfg.Runs.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	return run, actor, ws, nil
}

// Protect fences the live controller and cancels queued steering requests at
// one durable/admission boundary. Publication occurs after that safety
// boundary; publication failures are returned after the durable change.
func (s *Service) Protect(ctx context.Context, runID domain.RunID, by domain.MemberID) error {
	run, actor, ws, err := s.scopeRunActor(ctx, runID, by)
	if err != nil {
		return err
	}
	if err := permissions.Check(permissions.Protect, permissions.Actor{ID: actor.ID, Role: actor.Role}, permissions.Target{Workspace: ws.ID, Owner: run.MemberID}); err != nil {
		return err
	}
	var cancelled []*store.RoomMessage
	commit := func() error {
		if atomic, ok := s.cfg.Store.(store.RunProtectionStore); ok {
			var err error
			cancelled, err = atomic.SetRunProtectedAndCancelQueuedSteerRequests(ctx, runID, true, by, s.now())
			return err
		}
		ps, ok := s.cfg.Store.(ProtectedStore)
		if !ok {
			return fmt.Errorf("%w: store cannot protect runs", ErrInvalidRequest)
		}
		if err := ps.SetRunProtected(ctx, runID, true); err != nil {
			return err
		}
		var err error
		cancelled, err = s.cfg.Store.CancelQueuedSteerRequests(ctx, runID, by, s.now())
		return err
	}
	if s.cfg.Control != nil {
		if _, err := s.cfg.Control.AdmitRevoke(string(runID), commit); err != nil {
			return err
		}
	} else if err := commit(); err != nil {
		return err
	}
	var publicationErrs []error
	if err := s.publishProtected(ctx, run, by); err != nil {
		publicationErrs = append(publicationErrs, err)
	}
	for _, msg := range cancelled {
		if err := s.publishMessage(ctx, msg); err != nil {
			publicationErrs = append(publicationErrs, err)
		}
	}
	if len(publicationErrs) != 0 {
		return errors.Join(publicationErrs...)
	}
	return nil
}

// Purge is the explicit cancellation operation used by cleanup. It fences
// delivery admission while the queued-only CAS runs, then publishes winners.
func (s *Service) Purge(ctx context.Context, runID domain.RunID, by domain.MemberID) error {
	run, actor, ws, err := s.scopeRunActor(ctx, runID, by)
	if err != nil {
		return err
	}
	if err := permissions.Check(permissions.Protect, permissions.Actor{ID: actor.ID, Role: actor.Role}, permissions.Target{Workspace: ws.ID, Owner: run.MemberID}); err != nil {
		return err
	}
	return s.cancelQueued(ctx, runID, by)
}
func (s *Service) cancelQueued(ctx context.Context, runID domain.RunID, by domain.MemberID) error {
	var msgs []*store.RoomMessage
	commit := func() error {
		var err error
		msgs, err = s.cfg.Store.CancelQueuedSteerRequests(ctx, runID, by, s.now())
		return err
	}
	if s.cfg.Control != nil {
		if _, err := s.cfg.Control.AdmitRevoke(string(runID), commit); err != nil {
			return err
		}
	} else if err := commit(); err != nil {
		return err
	}
	var publicationErrs []error
	for _, msg := range msgs {
		if err := s.publishMessage(ctx, msg); err != nil {
			publicationErrs = append(publicationErrs, err)
		}
	}
	if len(publicationErrs) != 0 {
		return errors.Join(publicationErrs...)
	}
	return nil
}
