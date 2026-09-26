package evidence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	MaxCaptures                     = 64
	MaxCaptureBytes           int64 = 8 << 20
	MaxRunCaptureBytes        int64 = 128 << 20
	MaxVerificationNotesBytes       = 4096
)

func validateCaptureSelection(ids []string, notes string) error {
	if len(ids) > MaxCaptures || len(notes) > MaxVerificationNotesBytes || !utf8.ValidString(notes) {
		return fmt.Errorf("%w: capture selection or verification notes exceed bounds", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validArtifactID(id) {
			return fmt.Errorf("%w: invalid capture id", ErrInvalidRequest)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate capture id", ErrInvalidRequest)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validArtifactID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// RetainsDevelopmentArtifacts is used by live capability discovery. A staging
// journal is required even when a compatibility store permits ordinary packets.
func (s *Service) RetainsDevelopmentArtifacts() bool {
	return s.artifacts != nil && s.stagingStore() != nil
}

func (s *Service) captureDirectory(key string) (string, error) {
	path, err := s.artifactPath(key)
	if err != nil {
		return "", err
	}
	return path + ".captures", nil
}

// removeCaptures is part of the existing staging/packet cleanup transaction.
// It includes interrupted temporary copies; quota is released only as bytes
// are actually removed, never merely because a timestamp passed.
func (s *Service) removeCaptures(key string) error {
	dir, err := s.captureDirectory(key)
	if err != nil {
		return err
	}
	entries, err := s.fs.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("evidence: list capture cleanup: %w", err)
	}
	for _, entry := range entries {
		if err := s.fs.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("evidence: remove retained capture: %w", err)
		}
	}
	if err := s.fs.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.fs.SyncDir(s.root)
}

// captureUsage counts files rather than just packet declarations. The run lock
// serializes this scan with copies and cleanup; journal rows account for partial
// writes after a crash, including temporary files which were never published.
func (s *Service) captureUsage(ctx context.Context, run *domain.Run) (int, int64, error) {
	seen := make(map[string]struct{})
	count := 0
	var bytes int64
	add := func(key string) error {
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		dir, err := s.captureDirectory(key)
		if err != nil {
			return err
		}
		entries, err := s.fs.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("evidence: non-regular retained capture")
			}
			count++
			bytes += info.Size()
		}
		return nil
	}
	cursor := ""
	for {
		page, err := s.store.ListEvidencePackets(ctx, run.WorkspaceID, run.ID, cursor, MaxPageSize)
		if err != nil {
			return 0, 0, err
		}
		if page == nil {
			break
		}
		for _, packet := range page.Items {
			if packet == nil || packet.RunID != run.ID || packet.WorkspaceID != run.WorkspaceID {
				return 0, 0, errors.New("evidence: capture quota scope mismatch")
			}
			if err := add(packetCaptureKey(packet)); err != nil {
				return 0, 0, err
			}
		}
		if page.NextBefore == "" {
			break
		}
		if page.NextBefore == cursor {
			return 0, 0, errors.New("evidence: capture quota cursor did not advance")
		}
		cursor = page.NextBefore
	}
	rows, err := s.stagingStore().ListRunEvidenceStaging(ctx, run.ID, MaxPageSize)
	if err != nil {
		return 0, 0, err
	}
	// Conservatively refuse capture if the bounded journal read cannot account
	// for every row. Existing restart/expiry cleanup drains these rows.
	if len(rows) >= MaxPageSize {
		return 0, 0, errors.New("evidence: staging cleanup required before retention")
	}
	for _, row := range rows {
		if row == nil || row.RunID != run.ID || row.WorkspaceID != run.WorkspaceID {
			return 0, 0, errors.New("evidence: capture staging scope mismatch")
		}
		if err := add(row.ID); err != nil {
			return 0, 0, err
		}
	}
	return count, bytes, nil
}

func (s *Service) retainCaptures(ctx context.Context, run *domain.Run, key string, ids []string, authorize func() error) ([]store.DevelopmentArtifact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// An uncommitted retry may have left files. Remove them through the same
	// cleanup path before replacing anything or reclaiming their quota.
	if err := s.removeCaptures(key); err != nil {
		return nil, err
	}
	count, bytes, err := s.captureUsage(ctx, run)
	if err != nil {
		return nil, err
	}
	if count+len(ids) > MaxCaptures {
		return nil, errors.New("evidence: run retained capture count limit reached")
	}
	dir, err := s.captureDirectory(key)
	if err != nil {
		return nil, err
	}
	if err := s.fs.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := s.fs.SyncDir(s.root); err != nil {
		return nil, err
	}
	out := make([]store.DevelopmentArtifact, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if authorize != nil {
			if err := authorize(); err != nil {
				return nil, err
			}
		}
		artifact, reader, err := s.artifacts.OpenEvidenceArtifact(ctx, run.ID, id)
		if err != nil {
			return nil, fmt.Errorf("evidence: open selected capture: %w", err)
		}
		if reader == nil {
			return nil, errors.New("evidence: selected capture has no bytes")
		}
		copyErr := s.copyCapture(ctx, dir, run.ID, id, artifact, reader, MaxRunCaptureBytes-bytes)
		closeErr := reader.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return nil, err
		}
		bytes += artifact.Bytes
		out = append(out, artifact)
	}
	if err := s.fs.SyncDir(dir); err != nil {
		return nil, err
	}
	return out, nil
}

type captureContextReader struct {
	ctx context.Context
	io.Reader
}

func (r captureContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (s *Service) copyCapture(ctx context.Context, dir string, run domain.RunID, id string, artifact protocol.DevArtifact, reader io.Reader, remaining int64) error {
	if artifact.ID != id || artifact.RunID != string(run) || artifact.ContentType != "image/png" || artifact.Bytes <= 0 || artifact.Bytes > MaxCaptureBytes {
		return fmt.Errorf("%w: selected capture identity, content type or size mismatch", ErrInvalidRequest)
	}
	metadata, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	if len(metadata) >= 16<<10 {
		return fmt.Errorf("%w: capture metadata exceeds limit", ErrInvalidRequest)
	}
	if artifact.Bytes > remaining {
		return errors.New("evidence: run retained capture byte limit reached")
	}
	tmp, err := s.fs.CreateTemp(dir, ".capture-*")
	if err != nil {
		return err
	}
	named, ok := tmp.(namedFile)
	if !ok {
		return errors.Join(errors.New("evidence: capture file has no name"), tmp.Close())
	}
	reader = captureContextReader{ctx: ctx, Reader: reader}
	n, copyErr := io.Copy(tmp, io.LimitReader(reader, artifact.Bytes))
	if copyErr == nil && n != artifact.Bytes {
		copyErr = errors.New("evidence: selected capture bytes changed")
	}
	if copyErr == nil {
		var extra [1]byte
		n, readErr := io.ReadFull(reader, extra[:])
		if n != 0 || !errors.Is(readErr, io.EOF) {
			copyErr = errors.New("evidence: selected capture exceeds declared size")
		}
	}
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if writeErr := errors.Join(copyErr, syncErr, closeErr); writeErr != nil {
		return writeErr
	}
	image, err := s.fs.Open(named.Name())
	if err != nil {
		return err
	}
	config, decodeErr := png.DecodeConfig(image)
	closeErr = image.Close()
	if imageErr := errors.Join(decodeErr, closeErr); imageErr != nil {
		return fmt.Errorf("evidence: invalid PNG capture: %w", imageErr)
	}
	if config.Width != artifact.Width || config.Height != artifact.Height {
		return fmt.Errorf("%w: PNG dimensions mismatch", ErrInvalidRequest)
	}
	return s.fs.Rename(named.Name(), filepath.Join(dir, id+".png"))
}

// OpenArtifact opens the original capture under retained evidence authority.
// Callers authorize workspace/packet access, not live-run Steer permission.
// The original mount Path is metadata only, never a host filesystem input.
func (s *Service) OpenArtifact(ctx context.Context, workspace domain.WorkspaceID, packetID, artifactID string) (protocol.DevArtifact, io.ReadCloser, error) {
	var empty protocol.DevArtifact
	if !validArtifactID(artifactID) {
		return empty, nil, store.ErrNotFound
	}
	packet, err := s.store.GetEvidencePacket(ctx, packetID)
	if err != nil {
		return empty, nil, err
	}
	if packet == nil || packet.WorkspaceID != workspace {
		return empty, nil, store.ErrNotFound
	}
	run := packet.RunID
	lock := s.runLock(packet.RunID)
	lock.Lock()
	defer lock.Unlock()
	packet, err = s.store.GetEvidencePacket(ctx, packetID)
	if err != nil {
		return empty, nil, err
	}
	if packet == nil || packet.WorkspaceID != workspace || packet.RunID != run {
		return empty, nil, store.ErrNotFound
	}
	if packet.Availability == store.EvidenceExpired || (packet.ExpiresAt != nil && !s.now().Before(*packet.ExpiresAt)) {
		return empty, nil, ErrExpired
	}
	for _, artifact := range packet.Captures {
		if artifact.ID != artifactID {
			continue
		}
		if artifact.RunID != string(packet.RunID) {
			return empty, nil, errors.New("evidence: retained capture scope mismatch")
		}
		dir, err := s.captureDirectory(packetCaptureKey(packet))
		if err != nil {
			return empty, nil, err
		}
		path := filepath.Join(dir, artifactID+".png")
		info, err := s.fs.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil, store.ErrNotFound
		}
		if err != nil {
			return empty, nil, err
		}
		if !info.Mode().IsRegular() || info.Size() != artifact.Bytes {
			return empty, nil, errors.New("evidence: retained capture missing or changed")
		}
		reader, err := s.fs.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil, store.ErrNotFound
		}
		return artifact, reader, err
	}
	return empty, nil, store.ErrNotFound
}
