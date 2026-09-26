package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// WithWorkspaceDeletion refuses active candidate work before deleting anything.
// Candidate locks remain held until the workspace row and repository are gone.
func (s *Service) WithWorkspaceDeletion(ctx context.Context, workspace domain.WorkspaceID, cleanup func() error) error {
	if !s.workspaceDeleteMu.TryLock() {
		return fmt.Errorf("%w: an integration candidate is being prepared", store.ErrInUse)
	}
	defer s.workspaceDeleteMu.Unlock()
	ids, err := s.store.ListIntegrationCandidateIDs(ctx, workspace)
	if err != nil {
		return err
	}
	locked := 0
	defer func() {
		for _, id := range ids[:locked] {
			s.lock(id).Unlock()
		}
	}()
	for _, id := range ids {
		if !s.lock(id).TryLock() {
			return fmt.Errorf("%w: integration candidate %s has an operation in progress", store.ErrInUse, id)
		}
		locked++
		_, c, err := s.load(ctx, string(workspace), id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		for _, v := range c.Verifications {
			if v.Status == protocol.VerificationRunning || v.ContainerID != "" || s.verificationActive(v.VerificationID) {
				return fmt.Errorf("%w: candidate %s verification %s is active or cleaning up", store.ErrInUse, id, v.VerificationID)
			}
		}
		if c.DeliveryRequest != nil && c.DeliveryRequest.State == protocol.DeliveryDelivering {
			return fmt.Errorf("%w: candidate %s has an unfinished delivery", store.ErrInUse, id)
		}
	}
	for _, id := range ids {
		record, err := s.store.GetIntegrationCandidate(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := s.git.RemoveCandidate(ctx, workspace, id); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.root, "candidates", id)); err != nil {
			return fmt.Errorf("integration: remove candidate artifacts: %w", err)
		}
		if err := s.store.DeleteIntegrationCandidate(ctx, id, record.Version); err != nil {
			return err
		}
	}
	return cleanup()
}
