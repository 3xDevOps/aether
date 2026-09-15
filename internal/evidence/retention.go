package evidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func copyBounded(src io.Reader, dst io.Writer, limit int64) (truncated bool, sourceErr, writeErr error) {
	buf := make([]byte, 32*1024)
	var total int64
	for total < limit {
		want := int64(len(buf))
		if remaining := limit - total; remaining < want {
			want = remaining
		}
		n, err := src.Read(buf[:want])
		if n > 0 {
			written, werr := dst.Write(buf[:n])
			if werr != nil || written != n {
				return false, nil, io.ErrShortWrite
			}
			total += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil, nil
			}
			return false, err, nil
		}
		if n == 0 {
			return false, io.ErrNoProgress, nil
		}
	}
	var one [1]byte
	n, err := src.Read(one[:])
	if n > 0 {
		truncated = true
	}
	if err != nil && !errors.Is(err, io.EOF) {
		sourceErr = err
	}
	return truncated, sourceErr, nil
}

// List returns a bounded newest-first page scoped to one authorized workspace.
func (s *Service) List(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, before string, limit int) (protocol.EvidencePacketListResult, error) {
	if workspace == "" {
		return protocol.EvidencePacketListResult{}, fmt.Errorf("%w: workspace is required", ErrInvalidRequest)
	}
	limit = boundedPageSize(limit)
	cursor := before
	out := protocol.EvidencePacketListResult{Packets: make([]protocol.EvidencePacket, 0, limit)}
	for len(out.Packets) < limit {
		page, err := s.store.ListEvidencePackets(ctx, workspace, run, cursor, limit)
		if err != nil {
			return protocol.EvidencePacketListResult{}, fmt.Errorf("evidence: list packets: %w", err)
		}
		if page == nil || len(page.Items) == 0 {
			return out, nil
		}
		for _, raw := range page.Items {
			if raw == nil || raw.WorkspaceID != workspace {
				continue
			}
			p := safePacket(protocol.EvidencePacketFromStore(raw))
			out.Packets = append(out.Packets, p)
			if p.ID != "" && p.RunID != "" && p.IdempotencyKey != "" {
				s.mu.Lock()
				s.packets[captureOriginKey(domain.RunID(p.RunID), store.EvidenceOrigin{Kind: store.EvidenceOriginKind(p.Origin.Kind), ID: p.Origin.ID}, p.IdempotencyKey)] = clonePacket(p)
				s.mu.Unlock()
			}
			if len(out.Packets) == limit {
				out.NextBefore = store.EncodeEvidenceCursor(raw.CapturedAt, raw.ID)
				return out, nil
			}
		}
		if page.NextBefore == "" || page.NextBefore == cursor {
			return out, nil
		}
		cursor = page.NextBefore
	}
	return out, nil
}

// Get returns bounded packet metadata only; no host storage path or source
// body is included. The workspace scope is part of the authorization
// boundary; a packet in another workspace is indistinguishable from missing.
func (s *Service) Get(ctx context.Context, workspace domain.WorkspaceID, id string) (protocol.EvidencePacket, error) {
	if workspace == "" || id == "" {
		return protocol.EvidencePacket{}, fmt.Errorf("%w: workspace and packet id are required", ErrInvalidRequest)
	}
	p, err := s.store.GetEvidencePacket(ctx, id)
	if err != nil {
		return protocol.EvidencePacket{}, err
	}
	if p == nil || p.WorkspaceID != workspace {
		return protocol.EvidencePacket{}, store.ErrNotFound
	}
	return safePacket(protocol.EvidencePacketFromStore(p)), nil
}

// RenderPatch renders a retained patch from the private Git revision. The
// checkout is not consulted.
func (s *Service) RenderPatch(ctx context.Context, workspace domain.WorkspaceID, id string, maxBytes int) (gitengine.Patch, error) {
	p, err := s.Get(ctx, workspace, id)
	if err != nil {
		return gitengine.Patch{}, err
	}
	if p.Availability == protocol.EvidenceExpired || expired(p.ExpiresAt, s.now()) {
		return gitengine.Patch{}, ErrExpired
	}
	if p.RetainedRevision == "" {
		return gitengine.Patch{}, errors.New("evidence: retained patch unavailable")
	}
	return s.git.RenderEvidence(ctx, workspace, p.RetainedRevision, boundedPatchSize(maxBytes))
}

// OpenTranscript opens the immutable bounded transcript copy retained for a
// packet. Its name is private and is never part of packet metadata.
func (s *Service) OpenTranscript(ctx context.Context, workspace domain.WorkspaceID, id string) (io.ReadCloser, error) {
	p, err := s.Get(ctx, workspace, id)
	if err != nil {
		return nil, err
	}
	if p.Availability == protocol.EvidenceExpired || expired(p.ExpiresAt, s.now()) {
		return nil, ErrExpired
	}
	available := false
	for _, source := range p.Sources {
		if source.Name == "transcript" {
			available = source.Available
			break
		}
	}
	if !available {
		return nil, ErrTranscriptUnavailable
	}
	key := captureOriginKey(domain.RunID(p.RunID), store.EvidenceOrigin{Kind: store.EvidenceOriginKind(p.Origin.Kind), ID: p.Origin.ID}, p.IdempotencyKey)
	path, err := s.artifactPath(key)
	if err != nil {
		return nil, err
	}
	f, err := s.fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrTranscriptUnavailable
		}
		return nil, errors.New("evidence: open transcript")
	}
	return f, nil
}

// ReadTranscript returns at most maxBytes from a retained transcript and
// reports whether the retained source was truncated or the caller-specific
// bound clipped the result. ErrTranscriptUnavailable reports unavailable
// transcript data.
func (s *Service) ReadTranscript(ctx context.Context, workspace domain.WorkspaceID, id string, maxBytes int) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxBytes > MaxTranscriptBytes {
		maxBytes = MaxTranscriptBytes
	}
	p, err := s.Get(ctx, workspace, id)
	if err != nil {
		return nil, false, err
	}
	var sourceTruncated bool
	for _, source := range p.Sources {
		if source.Name == "transcript" {
			sourceTruncated = source.Truncated
			break
		}
	}
	reader, err := s.OpenTranscript(ctx, workspace, id)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = reader.Close() }()
	out := make([]byte, 0, minInt(maxBytes, 64*1024))
	buf := make([]byte, 32*1024)
	for len(out) < maxBytes {
		want := minInt(len(buf), maxBytes-len(out))
		n, readErr := reader.Read(buf[:want])
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, sourceTruncated, nil
			}
			return nil, false, errors.New("evidence: read transcript")
		}
		if n == 0 {
			return out, sourceTruncated, nil
		}
	}
	var one [1]byte
	n, readErr := reader.Read(one[:])
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, errors.New("evidence: read transcript")
	}
	return out, sourceTruncated || n > 0, nil
}

// CleanupExpired removes expired private Git refs, transcript copies, and
// metadata in bounded batches. It is safe to call repeatedly and does not
// depend on an in-memory workspace index.
func (s *Service) CleanupExpired(ctx context.Context) (int, error) {
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	now := s.now().UTC()
	if err := s.reapStaging(cleanupCtx, now); err != nil {
		return 0, err
	}
	packets, err := s.store.ListExpiredEvidencePackets(cleanupCtx, now, MaxPageSize)
	if err != nil {
		return 0, fmt.Errorf("evidence: list expired packets: %w", err)
	}
	if len(packets) > MaxPageSize {
		packets = packets[:MaxPageSize]
	}
	sort.SliceStable(packets, func(i, j int) bool {
		if packets[i] == nil {
			return false
		}
		if packets[j] == nil {
			return true
		}
		if packets[i].ExpiresAt == nil {
			return false
		}
		if packets[j].ExpiresAt == nil {
			return true
		}
		if packets[i].ExpiresAt.Equal(*packets[j].ExpiresAt) {
			return packets[i].ID < packets[j].ID
		}
		return packets[i].ExpiresAt.Before(*packets[j].ExpiresAt)
	})
	count := 0
	for _, p := range packets {
		if p == nil {
			continue
		}
		lock := s.runLock(p.RunID)
		lock.Lock()
		var err error
		if expiry, ok := s.store.(store.EvidenceExpiryStore); ok {
			err = s.expirePacket(cleanupCtx, p, expiry, now)
		} else {
			err = s.cleanupPacket(cleanupCtx, p)
		}
		lock.Unlock()
		if err != nil {
			return count, err
		}
		count++
	}
	if expiry, ok := s.store.(store.EvidenceExpiryStore); ok {
		purged, err := expiry.PurgeExpiredEvidenceTombstones(cleanupCtx, now.Add(-tombstoneRetention), MaxPageSize)
		if err != nil {
			return count, fmt.Errorf("evidence: purge tombstones: %w", err)
		}
		count += purged
	}
	return count, nil
}

func (s *Service) expirePacket(ctx context.Context, p *store.EvidencePacket, expiry store.EvidenceExpiryStore, now time.Time) error {
	key := packetCaptureKey(p)
	if err := s.removeStagedArtifacts(ctx, p.WorkspaceID, key); err != nil {
		return err
	}
	if pruner, ok := s.git.(GitEvidencePruner); ok {
		if err := pruner.PruneEvidence(ctx, p.WorkspaceID); err != nil {
			return fmt.Errorf("evidence: prune expired git evidence: %w", err)
		}
	}
	sources := make([]store.EvidenceSourceFact, len(p.Sources))
	for i, source := range p.Sources {
		source.Available = false
		source.Truncated = false
		source.Reason = "retention expired"
		sources[i] = source
	}
	if err := expiry.MarkEvidenceExpired(ctx, p.ID, now, sources); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("evidence: mark packet expired: %w", err)
	}
	s.mu.Lock()
	s.cleaned[key] = struct{}{}
	delete(s.packets, key)
	s.mu.Unlock()
	return nil
}

// PurgeRun removes every retained packet for a run while holding the same
// per-run lock used by Capture. The callback runs under that lock only after
// every packet's transcript, Git ref, and metadata have been removed.
func (s *Service) PurgeRun(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, cleanup func(context.Context) error) error {
	if workspace == "" || run == "" {
		return fmt.Errorf("%w: workspace and run are required", ErrInvalidRequest)
	}
	lock := s.runLock(run)
	lock.Lock()
	defer lock.Unlock()
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()

	// Deletion always drains the first bounded page. Advancing a cursor after
	// deleting that cursor row makes the store's tuple lookup return NULL and
	// strands every packet after the first page.
	for {
		page, err := s.store.ListEvidencePackets(cleanupCtx, workspace, run, "", MaxPageSize)
		if err != nil {
			return fmt.Errorf("evidence: list run packets: %w", err)
		}
		if page == nil || len(page.Items) == 0 {
			break
		}
		for _, p := range page.Items {
			if p == nil {
				continue
			}
			if p.WorkspaceID != workspace || p.RunID != run {
				return errors.New("evidence: run packet scope mismatch")
			}
			if err := s.purgePacket(cleanupCtx, p); err != nil {
				return err
			}
		}
	}
	if cleanup != nil {
		if err := cleanup(cleanupCtx); err != nil {
			return fmt.Errorf("evidence: run cleanup: %w", err)
		}
	}
	return nil
}

func (s *Service) purgePacket(ctx context.Context, p *store.EvidencePacket) error {
	if p == nil {
		return nil
	}
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	key := packetCaptureKey(p)
	path, err := s.artifactPath(key)
	if err != nil {
		return err
	}
	if err := s.fs.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("evidence: remove transcript")
	}
	if marker, err := s.truncationMarkerPath(key); err != nil {
		return err
	} else if err := s.fs.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("evidence: remove transcript marker")
	}
	if err := s.git.RemoveEvidence(cleanupCtx, p.WorkspaceID, key); err != nil {
		return fmt.Errorf("evidence: remove git evidence: %w", err)
	}
	if pruner, ok := s.git.(GitEvidencePruner); ok {
		if err := pruner.PruneEvidence(cleanupCtx, p.WorkspaceID); err != nil {
			return fmt.Errorf("evidence: prune git evidence: %w", err)
		}
	}
	if err := s.store.DeleteEvidencePacket(cleanupCtx, p.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("evidence: remove packet: %w", err)
	}
	s.mu.Lock()
	delete(s.packets, key)
	s.mu.Unlock()
	return nil
}

func (s *Service) cleanupPacket(ctx context.Context, p *store.EvidencePacket) error {
	if p == nil {
		return nil
	}
	key := packetCaptureKey(p)
	s.mu.Lock()
	_, already := s.cleaned[key]
	s.mu.Unlock()
	if already {
		return nil
	}
	return s.purgePacket(ctx, p)
}
func boundedPageSize(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}
func boundedPatchSize(limit int) int {
	if limit <= 0 || limit > DefaultPatchBytes {
		return DefaultPatchBytes
	}
	return limit
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func expired(raw *string, now time.Time) bool {
	if raw == nil || *raw == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, *raw)
	return err == nil && !now.Before(t)
}

func cleanObjectID(id string) string {
	if len(id) > 128 {
		return id[:128]
	}
	return cleanText(id, 128)
}
func cleanRepoPath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if path == "" || strings.HasPrefix(path, "/") || filepath.IsAbs(path) || strings.HasPrefix(path, "../") || path == ".." || strings.Contains(path, "/../") {
		return "<redacted-path>"
	}
	return cleanText(path, MaxFactBytes)
}
func cleanText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\x00' || r == '\r' || r == '\n' || r < 0x20 {
			return ' '
		}
		return r
	}, value)
	value = secretPattern.ReplaceAllString(value, "$1=<redacted>")
	fields := strings.Fields(value)
	for i, field := range fields {
		trimmed := strings.Trim(field, "\"'()[]{}<>,;:!")
		if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "../") || trimmed == ".." {
			fields[i] = "<redacted-path>"
		}
	}
	value = strings.Join(fields, " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

var secretPattern = regexp.MustCompile(`(?i)\b(token|secret|password|api[_-]?key|authorization)\s*[:=]\s*[^\s,;]+`)

func safeReason(err error) string {
	if err == nil {
		return "unavailable"
	}
	return cleanText(err.Error(), MaxFactBytes)
}
func boundedFacts(in []string) []string {
	out := make([]string, 0, minInt(len(in), MaxUnresolvedFacts))
	for _, fact := range in {
		fact = cleanText(fact, MaxFactBytes)
		if fact == "" {
			continue
		}
		out = append(out, fact)
		if len(out) == MaxUnresolvedFacts {
			break
		}
	}
	return out
}
func appendFacts(dst []string, facts ...string) []string {
	for _, fact := range facts {
		if len(dst) >= MaxUnresolvedFacts {
			break
		}
		fact = cleanText(fact, MaxFactBytes)
		if fact != "" {
			dst = append(dst, fact)
		}
	}
	return dst
}
func boundedIDs(in []string) []string {
	out := make([]string, 0, minInt(len(in), MaxRelatedMessages))
	for _, id := range in {
		id = cleanText(id, 128)
		if id == "" {
			continue
		}
		out = append(out, id)
		if len(out) == MaxRelatedMessages {
			break
		}
	}
	return out
}
func appendSourceFacts(dst []store.EvidenceSourceFact, facts ...store.EvidenceSourceFact) []store.EvidenceSourceFact {
	for _, fact := range facts {
		if len(dst) >= MaxSourceFacts {
			break
		}
		fact.Name = cleanText(fact.Name, 128)
		fact.Reason = cleanText(fact.Reason, MaxFactBytes)
		if fact.Name == "" {
			continue
		}
		dst = append(dst, fact)
	}
	return dst
}
func clonePacket(in protocol.EvidencePacket) protocol.EvidencePacket {
	in.ChangedFiles = append([]protocol.ChangedFileFact(nil), in.ChangedFiles...)
	in.Sources = append([]protocol.EvidenceSourceFact(nil), in.Sources...)
	in.RelatedRoomMessageIDs = append([]string(nil), in.RelatedRoomMessageIDs...)
	in.UnresolvedFacts = append([]string(nil), in.UnresolvedFacts...)
	return in
}
func safePacket(p protocol.EvidencePacket) protocol.EvidencePacket {
	p.Objective = cleanText(p.Objective, MaxObjectiveBytes)
	p.NextAction = cleanText(p.NextAction, MaxNextActionBytes)
	p.Provenance = cleanText(p.Provenance, MaxProvenanceBytes)
	if p.Origin.Kind != protocol.EvidenceOriginHuman && p.Origin.Kind != protocol.EvidenceOriginRun && p.Origin.Kind != protocol.EvidenceOriginServer {
		p.Origin = protocol.EvidenceOrigin{}
	}
	p.Origin.ID = cleanText(p.Origin.ID, 128)
	p.BaseRevision = cleanObjectID(p.BaseRevision)
	p.RetainedRevision = cleanObjectID(p.RetainedRevision)
	if len(p.ChangedFiles) > MaxChangedFiles {
		p.ChangedFiles = p.ChangedFiles[:MaxChangedFiles]
	}
	for i := range p.ChangedFiles {
		p.ChangedFiles[i].Path = cleanRepoPath(p.ChangedFiles[i].Path)
		p.ChangedFiles[i].Additions = nonNegative(p.ChangedFiles[i].Additions)
		p.ChangedFiles[i].Deletions = nonNegative(p.ChangedFiles[i].Deletions)
	}
	if len(p.Sources) > MaxSourceFacts {
		p.Sources = p.Sources[:MaxSourceFacts]
	}
	for i := range p.Sources {
		p.Sources[i].Name = cleanText(p.Sources[i].Name, 128)
		p.Sources[i].Reason = cleanText(p.Sources[i].Reason, MaxFactBytes)
	}
	p.UnresolvedFacts = boundedFacts(p.UnresolvedFacts)
	p.RelatedRoomMessageIDs = boundedIDs(p.RelatedRoomMessageIDs)
	return p
}

// Summary deterministically renders the packet's structured factual fields for
// handoff or review UI. It intentionally performs no model call and omits
// storage paths and source bodies.
func Summary(p protocol.EvidencePacket) string {
	transcript := "unavailable"
	for _, source := range p.Sources {
		if source.Name != "transcript" {
			continue
		}
		if source.Available {
			transcript = "available"
			if source.Truncated {
				transcript = "truncated"
			}
		}
		break
	}
	return fmt.Sprintf("objective=%s; revision=%s; changed_files=%d; transcript=%s; unresolved=%d; next_action=%s; provenance=%s",
		cleanText(p.Objective, MaxObjectiveBytes), p.RetainedRevision, len(p.ChangedFiles), transcript,
		len(p.UnresolvedFacts), cleanText(p.NextAction, MaxNextActionBytes), cleanText(p.Provenance, MaxProvenanceBytes))
}
