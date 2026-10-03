package evidence

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// WithSubmissionSources revalidates the primary packet's retained transcript bytes
// without materializing them. ids[0] is primary; other IDs are input-only packet
// presence checks. Repeated primary IDs still receive transcript validation.
// Missing packets occupy empty entries; unavailable sources stay unavailable.
// It holds every involved run's capture/expiry/purge lock through consume, allowing
// acceptance to commit its refreshed observations before cleanup can remove them.
// This is a presence check, not candidate required_sources completeness validation.
func (s *Service) WithSubmissionSources(ctx context.Context, workspace domain.WorkspaceID, ids []string, consume func([]protocol.EvidencePacket) error) error {
	if workspace == "" || len(ids) == 0 || consume == nil {
		return fmt.Errorf("%w: workspace, packet ids and submission callback are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	packets := make([]*store.EvidencePacket, len(ids))
	runs := make([]domain.RunID, 0, len(ids))
	seen := make(map[domain.RunID]bool, len(ids))
	for i, id := range ids {
		p, err := s.store.GetEvidencePacket(ctx, id)
		if err != nil || p == nil || p.ID != id || p.WorkspaceID != workspace || p.RunID == "" {
			continue
		}
		packets[i] = p
		if !seen[p.RunID] {
			seen[p.RunID] = true
			runs = append(runs, p.RunID)
		}
	}
	// Input packets can belong to other runs. A stable order prevents two
	// acceptances with overlapping input sets from deadlocking.
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	for _, run := range runs {
		lock := s.runLock(run)
		lock.Lock()
		defer lock.Unlock()
	}
	out := make([]protocol.EvidencePacket, len(ids))
	for i, before := range packets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if before == nil {
			continue
		}
		p, err := s.store.GetEvidencePacket(ctx, ids[i])
		if err != nil || p == nil || p.ID != before.ID || p.WorkspaceID != workspace ||
			p.RunID != before.RunID || p.Origin != before.Origin || p.IdempotencyKey != before.IdempotencyKey {
			continue
		}
		packet := safePacket(protocol.EvidencePacketFromStore(p))
		if ids[i] == ids[0] && p.Availability == store.EvidenceAvailable && p.ExpiredAt == nil &&
			(p.ExpiresAt == nil || s.now().UTC().Before(*p.ExpiresAt)) {
			for j := range packet.Sources {
				fact := &packet.Sources[j]
				if fact.Name != "transcript" || !fact.Available {
					continue
				}
				if err := s.checkSubmissionTranscript(ctx, p, fact.Truncated); err != nil {
					fact.Available = false
					if fact.Reason != "" {
						fact.Reason += "; "
					}
					fact.Reason += "retained transcript is unreadable"
				}
			}
		}
		out[i] = packet
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return consume(out)
}

func (s *Service) checkSubmissionTranscript(ctx context.Context, packet *store.EvidencePacket, truncated bool) error {
	reader, err := s.openCandidateTranscript(packet)
	if err != nil {
		return err
	}
	// Count the bounded stream without retaining its payload. Capture records no
	// general byte length, but a capped source must retain exactly the full cap.
	limited := &io.LimitedReader{R: captureContextReader{ctx: ctx, Reader: reader}, N: MaxTranscriptBytes + 1}
	oversized, readErr, writeErr := copyBounded(limited, io.Discard, MaxTranscriptBytes)
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if oversized {
		return fmt.Errorf("evidence: retained transcript exceeds capture bound")
	}
	if truncated && MaxTranscriptBytes+1-limited.N < MaxTranscriptBytes {
		return fmt.Errorf("evidence: capped retained transcript is shorter than capture bound")
	}
	return nil
}
