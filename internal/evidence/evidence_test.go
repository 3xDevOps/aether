package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/store"
)

type evidenceTestStore struct {
	mu             sync.Mutex
	packets        map[string]*store.EvidencePacket
	keys           map[string]string
	originKeys     map[string]string
	staging        map[string]*store.EvidenceStaging
	creates        int
	deletes        int
	createErr      error
	stagingListed  chan struct{}
	cancelOnCreate context.CancelFunc
}

func newEvidenceTestStore() *evidenceTestStore {
	return &evidenceTestStore{packets: make(map[string]*store.EvidencePacket), keys: make(map[string]string), originKeys: make(map[string]string), staging: make(map[string]*store.EvidenceStaging)}
}
func evidenceOriginKey(origin store.EvidenceOrigin, run domain.RunID, key string) string {
	return string(origin.Kind) + "\x00" + origin.ID + "\x00" + string(run) + "\x00" + key
}
func (s *evidenceTestStore) CreateEvidencePacket(_ context.Context, p *store.EvidencePacket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++
	if s.cancelOnCreate != nil {
		cancel := s.cancelOnCreate
		s.cancelOnCreate = nil
		cancel()
		return context.Canceled
	}
	if s.createErr != nil {
		return s.createErr
	}
	key := string(p.CreatorID) + "\x00" + string(p.RunID) + "\x00" + p.IdempotencyKey
	if id := s.keys[key]; id != "" {
		*p = *cloneStorePacket(s.packets[id])
		for stagingID, staged := range s.staging {
			if staged.CreatorID == p.CreatorID && staged.RunID == p.RunID && staged.IdempotencyKey == p.IdempotencyKey {
				delete(s.staging, stagingID)
			}
		}
		return nil
	}
	p.ID = fmt.Sprintf("packet-%d", len(s.packets)+1)
	s.packets[p.ID] = cloneStorePacket(p)
	s.keys[key] = p.ID
	if p.Origin.Valid() {
		s.originKeys[evidenceOriginKey(p.Origin, p.RunID, p.IdempotencyKey)] = p.ID
	}
	for stagingID, staged := range s.staging {
		if staged.CreatorID == p.CreatorID && staged.RunID == p.RunID && staged.IdempotencyKey == p.IdempotencyKey {
			delete(s.staging, stagingID)
		}
	}
	return nil
}
func (s *evidenceTestStore) GetEvidencePacket(_ context.Context, id string) (*store.EvidencePacket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.packets[id]
	if p == nil {
		return nil, store.ErrNotFound
	}
	return cloneStorePacket(p), nil
}
func (s *evidenceTestStore) ListEvidencePackets(_ context.Context, workspace domain.WorkspaceID, run domain.RunID, _ string, limit int) (*store.EvidencePacketPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &store.EvidencePacketPage{}
	for _, p := range s.packets {
		if p.WorkspaceID != workspace || (run != "" && p.RunID != run) {
			continue
		}
		out.Items = append(out.Items, cloneStorePacket(p))
		if len(out.Items) == limit {
			break
		}
	}
	return out, nil
}
func (s *evidenceTestStore) ListExpiredEvidencePackets(_ context.Context, before time.Time, limit int) ([]*store.EvidencePacket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*store.EvidencePacket, 0, limit)
	for _, p := range s.packets {
		if p.Availability == store.EvidenceExpired {
			continue
		}
		if p.ExpiresAt != nil && !p.ExpiresAt.After(before) {
			out = append(out, cloneStorePacket(p))
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (s *evidenceTestStore) DeleteEvidencePacket(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.packets[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.packets, id)
	s.deletes++
	return nil
}
func (s *evidenceTestStore) CreateEvidenceStaging(_ context.Context, staged *store.EvidenceStaging) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.staging[staged.ID]; !ok {
		copy := *staged
		s.staging[staged.ID] = &copy
	}
	return nil
}

func (s *evidenceTestStore) MarkEvidenceExpired(_ context.Context, id string, at time.Time, sources []store.EvidenceSourceFact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.packets[id]
	if p == nil {
		return store.ErrNotFound
	}
	if p.Availability == store.EvidenceExpired {
		return nil
	}
	p.Availability, p.ExpiredAt, p.Sources = store.EvidenceExpired, &at, append([]store.EvidenceSourceFact(nil), sources...)
	p.RetainedRevision, p.BaseRevision = "", ""
	return nil
}

func (s *evidenceTestStore) PurgeExpiredEvidenceTombstones(_ context.Context, before time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, p := range s.packets {
		if count == limit {
			break
		}
		if p.Availability == store.EvidenceExpired && p.ExpiredAt != nil && !p.ExpiredAt.After(before) {
			delete(s.packets, id)
			count++
		}
	}
	return count, nil
}

func (s *evidenceTestStore) ListEvidenceStaging(_ context.Context, before time.Time, limit int) ([]*store.EvidenceStaging, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*store.EvidenceStaging, 0, limit)
	for _, staged := range s.staging {
		if staged.CreatedAt.After(before) {
			continue
		}
		copy := *staged
		out = append(out, &copy)
		if len(out) == limit {
			break
		}
	}
	if s.stagingListed != nil {
		close(s.stagingListed)
		s.stagingListed = nil
	}
	return out, nil
}

func (s *evidenceTestStore) DeleteEvidenceStaging(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.staging, id)
	return nil
}

func (s *evidenceTestStore) GetEvidencePacketByIdempotency(_ context.Context, creator domain.MemberID, run domain.RunID, key string) (*store.EvidencePacket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.keys[string(creator)+"\x00"+string(run)+"\x00"+key]
	if id == "" {
		return nil, store.ErrNotFound
	}
	return cloneStorePacket(s.packets[id]), nil
}
func (s *evidenceTestStore) GetEvidencePacketByOrigin(_ context.Context, origin store.EvidenceOrigin, run domain.RunID, key string) (*store.EvidencePacket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.originKeys[evidenceOriginKey(origin, run, key)]
	if id == "" {
		return nil, store.ErrNotFound
	}
	return cloneStorePacket(s.packets[id]), nil
}

func cloneStorePacket(p *store.EvidencePacket) *store.EvidencePacket {
	if p == nil {
		return nil
	}
	q := *p
	q.ChangedFiles = append([]store.ChangedFileFact(nil), p.ChangedFiles...)
	q.Sources = append([]store.EvidenceSourceFact(nil), p.Sources...)
	q.RelatedRoomMessageIDs = append([]string(nil), p.RelatedRoomMessageIDs...)
	q.UnresolvedFacts = append([]string(nil), p.UnresolvedFacts...)
	return &q
}

type evidenceTestGit struct {
	mu               sync.Mutex
	captures         int
	removes          int
	captured         bool
	changedTruncated bool
	captureStarted   chan struct{}
	captureRelease   chan struct{}
}

func (g *evidenceTestGit) CaptureEvidence(_ context.Context, _ domain.RunID, _ string) (gitengine.EvidenceRevision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.captures++
	g.captured = true
	if g.captureStarted != nil {
		close(g.captureStarted)
		g.captureStarted = nil
		<-g.captureRelease
	}
	return gitengine.EvidenceRevision{
		WorkspaceID: "workspace-1", BaseCommit: strings.Repeat("a", 40),
		Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40),
		RetainedRefCreated:    true,
		ChangedFiles:          []events.FileDiffStat{{Path: "z.txt", Additions: 2}, {Path: "a.txt", Deletions: 1}},
		ChangedFilesTruncated: g.changedTruncated,
	}, nil
}
func (g *evidenceTestGit) RenderEvidence(_ context.Context, _ domain.WorkspaceID, _ string, _ int) (gitengine.Patch, error) {
	return gitengine.Patch{Base: strings.Repeat("a", 40), Text: "diff --git a/a.txt b/a.txt\n"}, nil
}
func (g *evidenceTestGit) RemoveEvidence(_ context.Context, _ domain.WorkspaceID, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removes++
	return nil
}

type evidenceTestRuns struct{ run *domain.Run }

func (r evidenceTestRuns) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	if r.run == nil || r.run.ID != id {
		return nil, store.ErrNotFound
	}
	q := *r.run
	return &q, nil
}

type evidenceTestEvents struct {
	seq uint64
	err error
}

func (e evidenceTestEvents) LastSeq(context.Context) (uint64, error) { return e.seq, e.err }

type evidenceTestTranscript struct {
	data []byte
	err  error
}

func (t *evidenceTestTranscript) Replay(domain.RunID) (io.ReadCloser, error) {
	if t.err != nil {
		return nil, t.err
	}
	return io.NopCloser(bytes.NewReader(t.data)), nil
}

type recordingEvidenceFS struct {
	osFileSystem
	mu         sync.Mutex
	operations []string
	syncErr    error
}

func (f *recordingEvidenceFS) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	f.operations = append(f.operations, "rename")
	f.mu.Unlock()
	return f.osFileSystem.Rename(oldPath, newPath)
}

func (f *recordingEvidenceFS) SyncDir(path string) error {
	f.mu.Lock()
	f.operations = append(f.operations, "sync-dir")
	f.mu.Unlock()
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.osFileSystem.SyncDir(path)
}

func (f *recordingEvidenceFS) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.operations...)
}

func newEvidenceTestService(t *testing.T, transcript *evidenceTestTranscript, now func() time.Time) (*Service, *evidenceTestStore, *evidenceTestGit) {
	t.Helper()
	st := newEvidenceTestStore()
	git := &evidenceTestGit{}
	runs := evidenceTestRuns{run: &domain.Run{ID: "run-1", WorkspaceID: "workspace-1", MemberID: "member-1", Task: "record task"}}
	svc, err := New(Config{
		Store: st, Git: git, Transcript: transcript, Runs: runs,
		Events: evidenceTestEvents{seq: 42}, EvidenceDir: t.TempDir(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, git
}

func TestCaptureRetainsTranscriptBeforeCleanup(t *testing.T) {
	transcript := &evidenceTestTranscript{data: []byte("cast header\noutput\n")}
	svc, _, git := newEvidenceTestService(t, transcript, func() time.Time { return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC) })
	var packetID string
	cleaned := false
	packet, err := svc.CaptureBeforeCleanup(context.Background(), Request{RunID: "run-1", Trigger: store.EvidenceFinish, IdempotencyKey: "idem-1"}, func(context.Context) error {
		cleaned = true
		if !git.captured {
			return errors.New("cleanup raced git capture")
		}
		return nil
	})
	if err != nil || !cleaned {
		t.Fatalf("capture before cleanup = %#v, %v", packet, err)
	}
	packetID = packet.ID
	transcript.err = errors.New("source checkout removed")
	got, _, err := svc.ReadTranscript(context.Background(), "workspace-1", packetID, 0)

	if err != nil || string(got) != "cast header\noutput\n" {
		t.Fatalf("retained transcript = %q, %v", got, err)
	}
}
func TestTranscriptRetentionSyncsDirectoryAfterRenames(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("durable")}, time.Now)
	fs := &recordingEvidenceFS{}
	svc.fs = fs
	if _, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "sync-order"}); err != nil {
		t.Fatal(err)
	}
	ops := fs.ops()
	if len(ops) != 3 || ops[0] != "rename" || ops[1] != "rename" || ops[2] != "sync-dir" {
		t.Fatalf("transcript filesystem operations = %v, want two renames followed by directory sync", ops)
	}
}

func TestTranscriptDirectorySyncFailureRollsBackCapture(t *testing.T) {
	svc, st, git := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("durable")}, time.Now)
	syncErr := errors.New("directory unavailable")
	fs := &recordingEvidenceFS{syncErr: syncErr}
	svc.fs = fs
	req := Request{RunID: "run-1", IdempotencyKey: "sync-failure"}
	if _, err := svc.Capture(context.Background(), req); err == nil ||
		!strings.Contains(err.Error(), "sync transcript directory") || !errors.Is(err, syncErr) {
		t.Fatalf("directory sync failure = %v, want contextual wrapped error", err)
	}
	if st.creates != 0 {
		t.Fatalf("packet creates after directory sync failure = %d, want 0", st.creates)
	}
	key := captureKey(req.RunID, "member-1", req.IdempotencyKey)
	path, err := svc.artifactPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := svc.fs.Stat(path); statErr == nil {
		t.Fatal("transcript survived directory sync failure")
	}
	marker, err := svc.truncationMarkerPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := svc.fs.Stat(marker); statErr == nil {
		t.Fatal("transcript marker survived directory sync failure")
	}
	if git.removes != 1 {
		t.Fatalf("git rollback calls = %d, want 1", git.removes)
	}
}

func TestReapStagingWaitsForCaptureBeforeDeletingArtifacts(t *testing.T) {
	now := time.Now()
	transcript := &evidenceTestTranscript{data: []byte("capture")}
	svc, st, git := newEvidenceTestService(t, transcript, func() time.Time { return now })
	key := captureKey("run-1", "member-1", "overlap")
	if err := st.CreateEvidenceStaging(context.Background(), &store.EvidenceStaging{
		ID: key, WorkspaceID: "workspace-1", RunID: "run-1",
		CreatorID: "member-1", IdempotencyKey: "overlap",
		CreatedAt: now.Add(-time.Hour), ExpiresAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	listed := make(chan struct{})
	git.captureStarted = started
	git.captureRelease = release
	st.stagingListed = listed
	captureDone := make(chan error, 1)
	go func() {
		_, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "overlap"})
		captureDone <- err
	}()
	<-started

	reapDone := make(chan error, 1)
	go func() { reapDone <- svc.reapStaging(context.Background(), now) }()
	<-listed
	select {
	case err := <-reapDone:
		t.Fatalf("staging reap completed while capture held run lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-captureDone; err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := <-reapDone; err != nil {
		t.Fatalf("reap staging: %v", err)
	}
	git.mu.Lock()
	removes := git.removes
	git.mu.Unlock()
	if removes != 0 {
		t.Fatalf("reaper removed committed capture artifacts %d times", removes)
	}
}

func TestCaptureTruncatesAndMarksUnavailableSources(t *testing.T) {
	transcript := &evidenceTestTranscript{data: bytes.Repeat([]byte{'x'}, MaxTranscriptBytes+1)}
	svc, _, _ := newEvidenceTestService(t, transcript, time.Now)
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "large"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, source := range packet.Sources {
		if source.Name == "transcript" {
			found = true
			if !source.Available || !source.Truncated {
				t.Fatalf("transcript source = %+v", source)
			}
		}
	}
	if !found {
		t.Fatal("transcript source fact missing")
	}
	got, truncated, err := svc.ReadTranscript(context.Background(), "workspace-1", packet.ID, MaxTranscriptBytes)
	if err != nil || !truncated || len(got) != MaxTranscriptBytes {
		t.Fatalf("bounded transcript = %d bytes, truncated=%v, err=%v", len(got), truncated, err)
	}

	unavailable := &evidenceTestTranscript{err: errors.New("no session")}
	svc, _, _ = newEvidenceTestService(t, unavailable, time.Now)
	packet, err = svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range packet.Sources {
		if source.Name == "transcript" && source.Available {
			t.Fatalf("unavailable transcript marked available: %+v", source)
		}
	}
}
func TestCaptureMarksChangedFilesTruncatedSource(t *testing.T) {
	svc, _, git := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("one")}, time.Now)
	git.changedTruncated = true
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "changed-truncated"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range packet.Sources {
		if source.Name == "changed_files" {
			if !source.Available || !source.Truncated {
				t.Fatalf("changed-files source = %+v", source)
			}
			return
		}
	}
	t.Fatal("changed-files source fact missing")
}

func TestExpiredPacketsRemainVisibleAsMetadata(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	svc, st, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("old")}, func() time.Time { return now })
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.packets[packet.ID].ExpiresAt = new(now.Add(-time.Second))
	st.mu.Unlock()
	got, err := svc.Get(context.Background(), "workspace-1", packet.ID)
	if err != nil || got.ID != packet.ID {
		t.Fatalf("expired get = %+v, %v, want metadata", got, err)
	}
	page, err := svc.List(context.Background(), "workspace-1", "run-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Packets) != 1 || page.Packets[0].ID != packet.ID {
		t.Fatalf("expired list = %+v, want metadata packet", page)
	}
	if _, _, err := svc.ReadTranscript(context.Background(), "workspace-1", packet.ID, 10); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired transcript error = %v, want ErrExpired", err)
	}
}

func TestInsertFailureRollsBackArtifactsAndRetainsTruncationOnRetry(t *testing.T) {
	transcript := &evidenceTestTranscript{data: bytes.Repeat([]byte{'x'}, MaxTranscriptBytes+1)}
	svc, st, git := newEvidenceTestService(t, transcript, time.Now)
	st.createErr = errors.New("insert unavailable")
	req := Request{RunID: "run-1", IdempotencyKey: "insert-retry"}
	if _, err := svc.Capture(context.Background(), req); err == nil {
		t.Fatal("capture succeeded while packet insert failed")
	}
	key := captureKey(req.RunID, "member-1", req.IdempotencyKey)
	path, err := svc.artifactPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := svc.fs.Stat(path); statErr == nil {
		t.Fatal("transcript artifact survived failed packet insert")
	}
	if git.removes != 1 {
		t.Fatalf("git rollback calls = %d, want 1", git.removes)
	}
	st.createErr = nil
	packet, err := svc.Capture(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range packet.Sources {
		if source.Name == "transcript" && (!source.Available || !source.Truncated) {
			t.Fatalf("retry transcript source = %+v", source)
		}
	}
}

func TestPurgeRunSerializesCaptureBeforeDeletion(t *testing.T) {
	svc, st, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("one")}, time.Now)
	if _, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "before-delete"}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	purgeDone := make(chan error, 1)
	go func() {
		purgeDone <- svc.PurgeRun(context.Background(), "workspace-1", "run-1", func(context.Context) error {
			close(entered)
			<-release
			svc.runs = evidenceTestRuns{}
			return nil
		})
	}()
	<-entered
	captureDone := make(chan error, 1)
	go func() {
		_, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "during-delete"})
		captureDone <- err
	}()
	select {
	case err := <-captureDone:
		t.Fatalf("capture completed during purge: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-purgeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-captureDone; !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("capture after deleted run = %v, want not found", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.packets) != 0 {
		t.Fatalf("packets after purge = %d, want none", len(st.packets))
	}
}

func TestCaptureIdempotent(t *testing.T) {
	svc, st, git := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("one")}, time.Now)
	req := Request{RunID: "run-1", IdempotencyKey: "same"}
	first, err := svc.Capture(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Capture(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || st.creates != 1 || git.captures != 1 {
		t.Fatalf("idempotency first=%+v second=%+v creates=%d captures=%d", first, second, st.creates, git.captures)
	}
}
func TestCaptureRetryUsesDurablePacketBeforeLiveSources(t *testing.T) {
	root := t.TempDir()
	st := newEvidenceTestStore()
	runs := evidenceTestRuns{run: &domain.Run{ID: "run-1", WorkspaceID: "workspace-1", MemberID: "member-1", Task: "record task"}}
	transcript := &evidenceTestTranscript{data: []byte("durable")}
	makeService := func(git *evidenceTestGit) *Service {
		svc, err := New(Config{Store: st, Git: git, Runs: runs, Transcript: transcript, EvidenceDir: root, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	req := Request{RunID: "run-1", IdempotencyKey: "durable-retry"}
	first, err := makeService(&evidenceTestGit{}).Capture(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	transcript.err = errors.New("checkout removed")
	retryGit := &evidenceTestGit{}
	second, err := makeService(retryGit).Capture(context.Background(), req)
	if err != nil || second.ID != first.ID {
		t.Fatalf("durable retry = %+v, %v", second, err)
	}
	retryGit.mu.Lock()
	captures := retryGit.captures
	retryGit.mu.Unlock()
	if captures != 0 {
		t.Fatalf("durable retry touched Git %d times", captures)
	}
}

func TestCleanupExpiredIsBoundedIdempotentAndPathSafe(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	svc, st, git := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("old")}, func() time.Time { return now })
	packet, err := svc.Capture(context.Background(), Request{
		RunID: "run-1", IdempotencyKey: "cleanup", Objective: filepath.Join("/host", "secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(packet.Objective, "/host") || strings.Contains(Summary(packet), "/host") {
		t.Fatalf("host path escaped packet: %+v", packet)
	}
	st.mu.Lock()
	st.packets[packet.ID].ExpiresAt = new(now.Add(-time.Minute))
	st.mu.Unlock()
	if n, cleanupErr := svc.CleanupExpired(context.Background()); cleanupErr != nil || n != 1 {
		t.Fatalf("cleanup = %d, %v", n, cleanupErr)
	}
	got, err := svc.Get(context.Background(), "workspace-1", packet.ID)
	if err != nil || got.Availability != "expired" || got.ExpiredAt == nil {
		t.Fatalf("expired tombstone = %+v, %v", got, err)
	}
	if n, err := svc.CleanupExpired(context.Background()); err != nil || n != 0 {
		t.Fatalf("second cleanup = %d, %v", n, err)
	}
	if git.removes != 1 || st.deletes != 0 {
		t.Fatalf("cleanup calls removes=%d deletes=%d", git.removes, st.deletes)
	}
}

func TestExactLimitTranscriptIsCompleteAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	st := newEvidenceTestStore()
	runs := evidenceTestRuns{run: &domain.Run{ID: "run-1", WorkspaceID: "workspace-1", MemberID: "member-1", Task: "record task"}}
	transcript := &evidenceTestTranscript{data: bytes.Repeat([]byte{'x'}, MaxTranscriptBytes)}
	newService := func(git *evidenceTestGit) *Service {
		svc, err := New(Config{
			Store: st, Git: git, Runs: runs, Transcript: transcript,
			Events: evidenceTestEvents{seq: 42}, EvidenceDir: root,
			Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	first, err := newService(&evidenceTestGit{}).Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range first.Sources {
		if source.Name == "transcript" && source.Truncated {
			t.Fatalf("exact-limit transcript marked truncated: %+v", source)
		}
	}
	transcript.err = errors.New("recording removed after restart")
	second, err := newService(&evidenceTestGit{}).Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range second.Sources {
		if source.Name == "transcript" && source.Truncated {
			t.Fatalf("restart changed exact-limit transcript state: %+v", source)
		}
	}
}

func TestCanceledPacketInsertCleansArtifactsWithIndependentContext(t *testing.T) {
	svc, st, git := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("one")}, time.Now)
	captureCtx, cancel := context.WithCancel(context.Background())
	st.mu.Lock()
	st.cancelOnCreate = cancel
	st.mu.Unlock()
	_, err := svc.Capture(captureCtx, Request{RunID: "run-1", IdempotencyKey: "cancel-insert"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled packet insert = %v, want context canceled", err)
	}
	key := captureKey("run-1", "member-1", "cancel-insert")
	path, pathErr := svc.artifactPath(key)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := svc.fs.Stat(path); statErr == nil {
		t.Fatal("transcript artifact survived canceled packet insert")
	}
	st.mu.Lock()
	staging := len(st.staging)
	st.mu.Unlock()
	if staging != 0 {
		t.Fatalf("staging rows after canceled insert = %d, want 0", staging)
	}
	git.mu.Lock()
	removes := git.removes
	git.mu.Unlock()
	if removes != 1 {
		t.Fatalf("git rollback calls = %d, want 1", removes)
	}
}

func TestPurgeRunDrainsAllMutatingPages(t *testing.T) {
	svc, st, git := newEvidenceTestService(t, nil, time.Now)
	st.mu.Lock()
	for i := range MaxPageSize*2 + 5 {
		key := fmt.Sprintf("packet-%03d", i)
		packet := &store.EvidencePacket{
			ID: key, WorkspaceID: "workspace-1", RunID: "run-1",
			CreatorID: "member-1", IdempotencyKey: fmt.Sprintf("idem-%03d", i),
		}
		st.packets[key] = packet
		st.keys["member-1\x00run-1\x00"+packet.IdempotencyKey] = key
	}
	st.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.PurgeRun(ctx, "workspace-1", "run-1", nil); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	remaining := len(st.packets)
	st.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("packets after purge = %d, want 0", remaining)
	}
	git.mu.Lock()
	removed := git.removes
	git.mu.Unlock()
	if removed != MaxPageSize*2+5 {
		t.Fatalf("git refs removed = %d, want %d", removed, MaxPageSize*2+5)
	}
}

func TestScopedGetRejectsOtherWorkspace(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("one")}, time.Now)
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(context.Background(), "other-workspace", packet.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-workspace get = %v, want not found", err)
	}
}
