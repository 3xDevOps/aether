package evidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// WithCandidateSource gives a candidate consumer a stable, authoritative
// packet and its retained transcript while the packet's run lock is held.
//
// The initial packet lookup determines the run whose capture and purge
// operations must be serialized. The packet is read again after taking that
// lock so a source cannot be opened from stale metadata. The callback owns
// preservation of the packet's immutable inputs (including copying transcript
// bytes and retaining the packet's Git revision); source cleanup cannot begin
// until it returns.
func (s *Service) WithCandidateSource(ctx context.Context, workspace domain.WorkspaceID, id string, preserve func(*store.EvidencePacket, string, io.ReadCloser) error) error {
	if workspace == "" || id == "" {
		return fmt.Errorf("%w: workspace and packet id are required", ErrInvalidRequest)
	}
	if preserve == nil {
		return fmt.Errorf("%w: candidate source callback is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Resolve the authoritative packet before locking only to obtain the run
	// identity. No source is opened from this snapshot.
	packet, err := s.store.GetEvidencePacket(ctx, id)
	if err != nil {
		return fmt.Errorf("evidence: resolve candidate source packet: %w", err)
	}
	if packet == nil {
		return fmt.Errorf("evidence: resolve candidate source packet: %w", store.ErrNotFound)
	}
	if packet.ID != id || packet.WorkspaceID != workspace || packet.RunID == "" {
		if packet.WorkspaceID != workspace || packet.ID != id {
			return fmt.Errorf("evidence: resolve candidate source packet: %w", store.ErrNotFound)
		}
		return fmt.Errorf("%w: packet run id is required", ErrInvalidRequest)
	}

	runID := packet.RunID
	origin := packet.Origin
	idempotencyKey := packet.IdempotencyKey
	lock := s.runLock(runID)
	lock.Lock()
	defer lock.Unlock()

	// Purge, expiry cleanup, and capture all use this lock. Re-read metadata
	// under it and reject any identity change before opening private artifacts.
	packet, err = s.store.GetEvidencePacket(ctx, id)
	if err != nil {
		return fmt.Errorf("evidence: reread candidate source packet: %w", err)
	}
	if packet == nil {
		return fmt.Errorf("evidence: reread candidate source packet: %w", store.ErrNotFound)
	}
	if packet.ID != id || packet.WorkspaceID != workspace || packet.RunID != runID ||
		packet.Origin != origin || packet.IdempotencyKey != idempotencyKey {
		if packet.WorkspaceID != workspace || packet.ID != id || packet.RunID != runID {
			return fmt.Errorf("evidence: candidate source packet identity changed: %w", store.ErrNotFound)
		}
		return fmt.Errorf("evidence: candidate source packet identity changed")
	}
	now := s.now().UTC()
	if packet.Availability == store.EvidenceExpired ||
		(packet.ExpiresAt != nil && !now.Before(*packet.ExpiresAt)) {
		return fmt.Errorf("evidence: candidate source packet: %w", ErrExpired)
	}
	if packet.Availability != store.EvidenceAvailable {
		return fmt.Errorf("evidence: candidate source packet unavailable")
	}

	var transcript io.ReadCloser
	if transcriptAvailable(packet) {
		transcript, err = s.openCandidateTranscript(packet)
		if err != nil {
			return fmt.Errorf("evidence: open candidate transcript: %w", err)
		}
	}

	callbackErr, closeErr := invokeCandidateSource(preserve, packet, packetCaptureKey(packet), transcript)
	if callbackErr != nil {
		return fmt.Errorf("evidence: preserve candidate source: %w", callbackErr)
	}
	if closeErr != nil {
		return fmt.Errorf("evidence: close candidate transcript: %w", closeErr)
	}
	return nil
}

func transcriptAvailable(packet *store.EvidencePacket) bool {
	if packet == nil {
		return false
	}
	for _, source := range packet.Sources {
		if source.Name == "transcript" {
			return source.Available
		}
	}
	return false
}

func invokeCandidateSource(preserve func(*store.EvidencePacket, string, io.ReadCloser) error, packet *store.EvidencePacket, captureKey string, transcript io.ReadCloser) (callbackErr, closeErr error) {
	if transcript == nil {
		return preserve(packet, captureKey, nil), nil
	}
	defer func() {
		closeErr = transcript.Close()
	}()
	callbackErr = preserve(packet, captureKey, transcript)
	return callbackErr, closeErr
}

func (s *Service) openCandidateTranscript(packet *store.EvidencePacket) (io.ReadCloser, error) {
	path, err := s.artifactPath(packetCaptureKey(packet))
	if err != nil {
		return nil, err
	}
	reader, err := s.fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrTranscriptUnavailable
		}
		return nil, err
	}
	return reader, nil
}
