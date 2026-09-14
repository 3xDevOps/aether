package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	schedulerEvidenceRoomLimit       = 100
	schedulerEvidenceRoomMaxPages    = 4
	schedulerEvidenceRoomMaxMessages = schedulerEvidenceRoomLimit * schedulerEvidenceRoomMaxPages
)

// captureFinishEvidence retains the final run state after its final publish.
// The idempotency identity is derived from the published tip (or the durable
// exit boundary when publication failed), never from an event sequence.
func (s *Scheduler) captureFinishEvidence(ctx context.Context, run domain.RunID, outcome domain.RunStatus, identity string) error {
	return s.captureEvidence(ctx, run, store.EvidenceFinish, "finish:"+string(run)+":"+identity+":"+string(outcome), nil)
}

// captureBeforeCleanupEvidence retains the final state before automatic
// checkout/transcript cleanup. The evidence service owns a run lock spanning
// retention and cleanup, so a concurrent finish cannot remove either source.
func (s *Scheduler) captureBeforeCleanupEvidence(ctx context.Context, run domain.RunID, identity string, cleanup func(context.Context) error) error {
	return s.captureEvidence(ctx, run, store.EvidenceFinish, "cleanup:"+string(run)+":"+identity, cleanup)
}

func (s *Scheduler) captureEvidence(ctx context.Context, run domain.RunID, trigger store.EvidenceTrigger, idempotency string, cleanup func(context.Context) error) error {
	s.mu.Lock()
	service := s.evidence
	s.mu.Unlock()
	if service == nil {
		if cleanup != nil {
			return cleanup(ctx)
		}
		return nil
	}

	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("scheduler: evidence run lookup: %w", err)
	}
	if r == nil || r.ID != run {
		return fmt.Errorf("scheduler: evidence run lookup returned no matching run")
	}
	related, unresolved, roomSource := s.roomEvidenceWithFact(ctx, r.WorkspaceID, run)
	req := evidence.Request{
		RunID:                 run,
		Origin:                store.EvidenceOrigin{Kind: store.EvidenceOriginServer},
		OwnerID:               r.MemberID,
		Trigger:               trigger,
		IdempotencyKey:        idempotency,
		RelatedRoomMessageIDs: related,
		UnresolvedFacts:       unresolved,
		SourceFacts:           []store.EvidenceSourceFact{roomSource},
		Provenance:            "scheduler-finish",
	}
	var packet protocol.EvidencePacket
	if cleanup == nil {
		packet, err = service.Capture(ctx, req)
	} else {
		packet, err = service.CaptureBeforeCleanup(ctx, req, cleanup)
	}
	if err != nil {
		return fmt.Errorf("scheduler: capture evidence: %w", err)
	}
	if packet.ID != "" && s.publishEvidence(ctx, packet) {
		if outbox, ok := s.cfg.Store.(store.EvidencePublicationStore); ok {
			if err := outbox.MarkEvidencePublicationPublished(ctx, packet.ID, time.Now().UTC()); err != nil {
				slog.Warn("scheduler: mark evidence publication", "packet", packet.ID, "error", err)
			}
		}
	}
	return nil
}

type roomQuestionFact struct {
	id   string
	body string
}

func (s *Scheduler) roomEvidenceWithFact(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) ([]string, []string, store.EvidenceSourceFact) {
	source := store.EvidenceSourceFact{Name: "room", Available: true}
	ids := make([]string, 0, evidence.MaxRelatedMessages)
	questions := make([]roomQuestionFact, 0, schedulerEvidenceRoomMaxMessages)
	answered := make(map[string]struct{}, schedulerEvidenceRoomMaxMessages)
	seenIDs := make(map[string]struct{}, schedulerEvidenceRoomMaxMessages)
	seenCursors := make(map[string]struct{}, schedulerEvidenceRoomMaxPages)
	before := ""
	messageCount := 0

	for pageNumber := 0; ; pageNumber++ {
		if pageNumber >= schedulerEvidenceRoomMaxPages {
			source.Truncated = true
			if source.Reason == "" {
				source.Reason = "room history truncated"
			}
			break
		}
		page, err := s.cfg.Store.ListRoomMessages(ctx, workspace, run, before, schedulerEvidenceRoomLimit)
		if err != nil {
			if pageNumber == 0 {
				source.Available = false
				source.Reason = "room messages unavailable"
				return nil, []string{"room messages unavailable"}, source
			}
			source.Truncated = true
			source.Reason = "older room messages unavailable"
			break
		}
		if page == nil {
			if pageNumber == 0 {
				source.Available = false
				source.Reason = "room messages unavailable"
				return nil, []string{"room messages unavailable"}, source
			}
			source.Truncated = true
			source.Reason = "older room messages unavailable"
			break
		}

		for _, message := range page.Items {
			if message == nil {
				continue
			}
			if messageCount >= schedulerEvidenceRoomMaxMessages {
				source.Truncated = true
				if source.Reason == "" {
					source.Reason = "room history truncated"
				}
				break
			}
			messageCount++
			if message.Anchor != nil && message.ID != "" {
				if _, ok := seenIDs[message.ID]; !ok {
					seenIDs[message.ID] = struct{}{}
					if len(ids) < evidence.MaxRelatedMessages {
						ids = append(ids, message.ID)
					} else {
						source.Truncated = true
						if source.Reason == "" {
							source.Reason = "room facts truncated"
						}
					}
				}
			}
			if message.Kind == store.RoomMessageReply && message.CorrelationID != "" {
				answered[message.CorrelationID] = struct{}{}
				continue
			}
			if message.Kind != store.RoomMessageQuestion ||
				message.State == store.RoomMessageDenied || message.State == store.RoomMessageCancelled {
				continue
			}
			body := strings.Join(strings.Fields(message.Body), " ")
			if body == "" {
				body = "unspecified"
			}
			if len(body) > 512 {
				body = body[:512]
			}
			questions = append(questions, roomQuestionFact{id: message.ID, body: body})
		}
		if messageCount >= schedulerEvidenceRoomMaxMessages {
			// A non-empty cursor at the cap proves that at least one row
			// remains outside the bounded evidence packet. Do not infer
			// truncation from page count alone: exactly 400 messages with
			// no successor cursor are complete.
			if page.NextBefore != "" {
				source.Truncated = true
				if source.Reason == "" {
					source.Reason = "room history truncated"
				}
			}
			break
		}
		next := page.NextBefore
		if next == "" {
			break
		}
		if _, ok := seenCursors[next]; ok {
			source.Truncated = true
			if source.Reason == "" {
				source.Reason = "room history cursor did not advance"
			}
			break
		}
		seenCursors[next] = struct{}{}
		before = next
	}

	facts := make([]string, 0, evidence.MaxUnresolvedFacts)
	for _, question := range questions {
		if _, ok := answered[question.id]; ok {
			continue
		}
		if len(facts) >= evidence.MaxUnresolvedFacts {
			source.Truncated = true
			if source.Reason == "" {
				source.Reason = "room facts truncated"
			}
			break
		}
		facts = append(facts, fmt.Sprintf("open question %s: %s", question.id, question.body))
	}
	if source.Truncated && source.Reason == "" {
		source.Reason = "room context truncated"
	}
	return ids, facts, source
}

func (s *Scheduler) publishEvidence(ctx context.Context, packet protocol.EvidencePacket) bool {
	unavailable, truncated := 0, 0
	for _, source := range packet.Sources {
		if !source.Available {
			unavailable++
		}
		if source.Truncated {
			truncated++
		}
	}
	eventTime := time.Unix(0, 0).UTC()
	if packet.CapturedAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, packet.CapturedAt); err == nil {
			eventTime = parsed.UTC()
		}
	}
	origin := events.EvidenceOriginPayload{Kind: string(packet.Origin.Kind), ID: packet.Origin.ID}
	actor := domain.MemberID("")
	if packet.Origin.Kind == protocol.EvidenceOriginHuman {
		actor = domain.MemberID(packet.Origin.ID)
	}
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		ID: "evidence:" + packet.ID, Time: eventTime,
		WorkspaceID: domain.WorkspaceID(packet.WorkspaceID),
		RunID:       domain.RunID(packet.RunID), ActorID: actor,
		Payload: events.EvidencePacketPayload{
			PacketID: packet.ID, WorkspaceID: domain.WorkspaceID(packet.WorkspaceID),
			RunID: domain.RunID(packet.RunID), Origin: origin, CreatorID: domain.MemberID(packet.CreatorID),
			Availability: string(packet.Availability), ExpiredAt: packet.ExpiredAt,
			Trigger: string(packet.Trigger), EventBoundary: packet.EventBoundary,
			ExpiresAt: packet.ExpiresAt, ChangedFileCount: len(packet.ChangedFiles),
			SourceCount: len(packet.Sources), UnavailableSourceCount: unavailable,
			TruncatedSourceCount: truncated, UnresolvedFactCount: len(packet.UnresolvedFacts),
		},
	})
	if err != nil {
		slog.Warn("scheduler: publish evidence failed", "packet", packet.ID, "error", err)
		return false
	}
	return true
}
func (s *Scheduler) drainEvidencePublications(ctx context.Context) {
	outbox, ok := s.cfg.Store.(store.EvidencePublicationStore)
	if !ok {
		return
	}
	before := ""
	for page := 0; page < 8; page++ {
		rows, next, err := outbox.ListPendingEvidencePublications(ctx, time.Now().UTC(), before, schedulerEvidenceRoomLimit)
		if err != nil {
			slog.Warn("scheduler: list evidence publications", "error", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, row := range rows {
			if row == nil {
				continue
			}
			packet, err := s.cfg.Store.GetEvidencePacket(ctx, row.PacketID)
			if err != nil {
				continue
			}
			if s.publishEvidence(ctx, protocol.EvidencePacketFromStore(packet)) {
				if markErr := outbox.MarkEvidencePublicationPublished(ctx, row.PacketID, time.Now().UTC()); markErr != nil {
					slog.Warn("scheduler: mark evidence publication published",
						"packet", row.PacketID, "error", markErr)
				}
			} else {
				nextAttempt := time.Now().UTC().Add(time.Duration(1+row.Attempts) * time.Second)
				if markErr := outbox.MarkEvidencePublicationFailure(ctx, row.PacketID, row.Attempts+1, nextAttempt, "bus publish failed"); markErr != nil {
					slog.Warn("scheduler: mark evidence publication failure",
						"packet", row.PacketID, "attempts", row.Attempts+1, "error", markErr)
				}
			}
		}
		if next == "" || next == before {
			return
		}
		before = next
	}
}

func evidenceCommitIdentity(published, committed string, code int) string {
	if published != "" {
		return published
	}
	if committed != "" {
		return committed
	}
	return fmt.Sprintf("exit-%d", code)
}

// finishCaptureIdentity binds a commit/exit boundary to the durable launch
// generation. A retained relaunch with no Git changes therefore gets a new
// packet, while a retry after a crash reuses the already-composed identity.
func finishCaptureIdentity(generation, published, committed string, code int) string {
	if strings.HasPrefix(generation, "launch:") && strings.Count(generation, ":") == 1 {
		return generation + ":" + evidenceCommitIdentity(published, committed, code)
	}
	if generation != "" {
		// A fully composed identity persisted after terminalization is the
		// retry key; never derive a second key from a repeated commit.
		return generation
	}
	return evidenceCommitIdentity(published, committed, code)
}

func logEvidenceFailure(run domain.RunID, err error) {
	if err != nil {
		slog.Warn("scheduler: retain run after evidence capture failure", "run", run, "error", err)
	}
}

func (s *Scheduler) retainAfterEvidenceFailure(entry *supervised) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[entry.runID] != entry {
		return
	}
	now := time.Now().UTC()
	entry.retained = true
	entry.retainedUntil = &now
	entry.evidencePending = true
	entry.finalizing = false
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist evidence-retained sidecar", "run", entry.runID, "error", err)
	}
}

// persistEvidenceIdentity records the exact commit/exit identity before a
// capture attempt. A crash after Git publication but before packet metadata
// commits therefore retries the same packet key.
func (s *Scheduler) persistEvidenceIdentity(run domain.RunID, identity string) error {
	sc, err := s.readSidecar(run)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("scheduler: read evidence identity sidecar: %w", err)
	}
	if sc.RunID == "" {
		sc.RunID = string(run)
	}
	sc.EvidenceIdentity = identity
	if err := s.writeSidecar(sc); err != nil {
		return fmt.Errorf("scheduler: persist evidence identity sidecar: %w", err)
	}
	return nil
}

// persistEvidencePending leaves a durable retry marker even when no in-memory
// owner remains (for example, a missing container discovered during recovery).
func (s *Scheduler) persistEvidencePending(run domain.RunID, identity string) {
	sc, err := s.readSidecar(run)
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("scheduler: read evidence-pending sidecar", "run", run, "error", err)
		return
	}
	if sc.RunID == "" {
		sc.RunID = string(run)
	}
	sc.EvidenceIdentity = identity
	sc.EvidencePending = true
	if err := s.writeSidecar(sc); err != nil {
		slog.Warn("scheduler: persist evidence-pending sidecar", "run", run, "error", err)
	}
}

func (s *Scheduler) resolveEvidencePending(ctx context.Context, run domain.RunID, outcome domain.RunStatus, identity string) error {
	if identity == "" {
		identity = "none"
	}
	if err := s.captureFinishEvidence(ctx, run, outcome, identity); err != nil {
		return err
	}
	s.mu.Lock()
	entry := s.runs[run]
	if entry != nil {
		entry.evidencePending = false
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			s.mu.Unlock()
			return err
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	sc, err := s.readSidecar(run)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	sc.EvidencePending = false
	if sc.ContainerID == "" && !sc.Retained && !sc.DestroyPending {
		s.removeSidecar(run)
		return nil
	}
	return s.writeSidecar(sc)
}
