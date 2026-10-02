package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const integrationCleanupBatch = 100

// Cleanup advances durable candidate fences and removes only artifacts whose
// aggregate has already recorded the deleting/expired state. Rows remain as
// bounded tombstones until their retention deadline, allowing restart-safe
// retries after a Git or filesystem failure.
func (s *Service) Cleanup(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, ErrUnavailable
	}
	now := s.nowTime()
	cleaned := 0
	var firstErr error
	cursor := ""
	for {
		rows, err := s.store.ListIntegrationCleanupCandidatesAfter(ctx, now, cursor, integrationCleanupBatch)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row == nil || row.ID == "" {
				continue
			}
			lock := s.lock(row.ID)
			lock.Lock()
			fresh, loadErr := s.store.GetIntegrationCandidate(ctx, row.ID)
			if loadErr != nil {
				lock.Unlock()
				if !errors.Is(loadErr, store.ErrNotFound) && firstErr == nil {
					firstErr = loadErr
				}
				continue
			}
			n, cleanErr := s.cleanupCandidateLocked(ctx, fresh, now)
			lock.Unlock()
			cleaned += n
			if cleanErr != nil && firstErr == nil {
				firstErr = cleanErr
			}
		}
		last := rows[len(rows)-1]
		if last == nil || last.ID == "" || len(rows) < integrationCleanupBatch {
			break
		}
		if last.ID == cursor {
			break
		}
		cursor = last.ID
	}
	return cleaned, firstErr
}

func (s *Service) cleanupCandidateLocked(ctx context.Context, row *store.IntegrationCandidate, now time.Time) (int, error) {
	if row == nil {
		return 0, nil
	}
	var c protocol.Candidate
	if err := json.Unmarshal(row.Payload, &c); err != nil {
		return 0, fmt.Errorf("integration: decode cleanup candidate: %w", err)
	}
	c.Version = row.Version
	if c.CandidateID == "" {
		c.CandidateID = row.ID
	}
	// A tombstone has already relinquished every owned artifact. Never reset
	// its deadline: doing so would make deletion starvation permanent.
	if c.State == protocol.CandidateExpired {
		if !row.ExpiresAt.IsZero() && now.Before(row.ExpiresAt) {
			return 0, nil
		}
		if err := s.store.DeleteIntegrationCandidate(ctx, c.CandidateID, row.Version); err != nil && !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		return 1, nil
	}
	if candidateActive(&c, s.verificationActive) {
		if c.State == protocol.CandidateDeleting || (!c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)) {
			s.cancelCandidate(&c)
		}
		return 0, nil
	}
	// Recover interrupted runtime work even when the candidate itself has
	// not expired yet; startup must never leave a running container behind.
	if err := s.recoverVerifications(ctx, &c); err != nil {
		return 0, err
	}
	if c.State != protocol.CandidateDeleting {
		if !c.ExpiresAt.IsZero() && now.Before(c.ExpiresAt) {
			return 0, nil
		}
		c.State = protocol.CandidateDeleting
		if err := s.save(ctx, row, &c); err != nil {
			return 0, err
		} // durable fence first
	}
	if s.git == nil {
		return 0, ErrUnavailable
	}
	if err := s.git.RemoveCandidate(ctx, domain.WorkspaceID(c.WorkspaceID), c.CandidateID); err != nil {
		return 0, err
	}
	if s.root != "" {
		if err := os.RemoveAll(filepath.Join(s.root, "candidates", c.CandidateID)); err != nil {
			return 0, err
		}
	}
	c.State = protocol.CandidateExpired
	c.Inputs = nil
	c.Verifications = nil
	c.DeliveryRequest = nil
	c.DeliveryReceipt = nil
	c.Error = "candidate expired"
	c.ExpiresAt = now.Add(protocol.IntegrationTombstoneRetention)
	if err := s.save(ctx, row, &c); err != nil {
		return 0, err
	}
	return 1, nil
}
