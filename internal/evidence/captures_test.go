package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

type captureFileSource struct {
	dir       string
	artifacts map[string]protocol.DevArtifact
}

func (s *captureFileSource) OpenEvidenceArtifact(_ context.Context, _ domain.RunID, id string) (protocol.DevArtifact, io.ReadCloser, error) {
	reader, err := os.Open(filepath.Join(s.dir, id+".png"))
	return s.artifacts[id], reader, err
}
func (s *captureFileSource) add(t *testing.T, run domain.RunID, n int) (protocol.DevArtifact, []byte) {
	t.Helper()
	id := fmt.Sprintf("%032x", n)
	im := image.NewRGBA(image.Rect(0, 0, 3, 2))
	im.Set(1, 1, color.RGBA{R: 190, G: 80, B: 20, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, id+".png"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := protocol.DevArtifact{ID: id, RunID: string(run), Path: "/run/aether/captures/" + id + ".png", Source: "browser", Incarnation: "browser-session", PageID: "page-1", PageRevision: 9, ViewportID: "phone", URL: "http://127.0.0.1:3000/account", CapturedAt: "2026-09-20T10:11:12.123456789Z", ContentType: "image/png", Bytes: int64(data.Len()), Width: 3, Height: 2, Truncated: true}
	s.artifacts[id] = artifact
	return artifact, data.Bytes()
}

type workspaceCaptureGit struct {
	evidenceTestGit
	workspace domain.WorkspaceID
}

func (g *workspaceCaptureGit) CaptureEvidence(ctx context.Context, run domain.RunID, key string) (gitengine.EvidenceRevision, error) {
	revision, err := g.evidenceTestGit.CaptureEvidence(ctx, run, key)
	revision.WorkspaceID = g.workspace
	return revision, err
}

type durableCaptureFixture struct {
	cfg    Config
	db     *store.DB
	dbPath string
	run    *domain.Run
	source *captureFileSource
	now    time.Time
}

func newDurableCaptureFixture(t *testing.T) *durableCaptureFixture {
	t.Helper()
	f := &durableCaptureFixture{dbPath: filepath.Join(t.TempDir(), "evidence.db"), now: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)}
	var err error
	f.db, err = storetest.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Close(); err != nil {
			t.Error(err)
		}
	})
	workspace := &domain.Workspace{Name: "retention"}
	if err := f.db.CreateWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	member := &domain.Member{DisplayName: "Reviewer", TailnetLogin: "reviewer@example.test", Role: domain.RoleCollaborator}
	if err := f.db.CreateMember(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	f.run = &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Status: domain.RunRunning, Mode: domain.LaunchTUI, Harness: "claude", Task: "verify login"}
	if err := f.db.CreateRun(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	f.source = &captureFileSource{dir: filepath.Join(t.TempDir(), "transient"), artifacts: map[string]protocol.DevArtifact{}}
	f.cfg = Config{Store: f.db, Runs: f.db, Git: &workspaceCaptureGit{workspace: workspace.ID}, Artifacts: f.source, EvidenceDir: t.TempDir(), Now: func() time.Time { return f.now }}
	return f
}
func (f *durableCaptureFixture) service(t *testing.T) *Service {
	t.Helper()
	s, err := New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *durableCaptureFixture) restart(t *testing.T) *Service {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.db, err = store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Store, f.cfg.Runs = f.db, f.db
	return f.service(t)
}
func (f *durableCaptureFixture) request(key string, ids ...string) Request {
	return Request{RunID: f.run.ID, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(f.run.ID)}, IdempotencyKey: key, ArtifactIDs: ids, VerificationNotes: "Invalid login rejected; valid test login and logout verified."}
}

func TestRetainedCaptureSurvivesTransientCleanupAndDatabaseRestart(t *testing.T) {
	f := newDurableCaptureFixture(t)
	s := f.service(t)
	artifact, original := f.source.add(t, f.run.ID, 1)
	packet, err := s.CaptureBeforeCleanup(t.Context(), f.request("review", artifact.ID), func(ctx context.Context) error {
		if err := os.RemoveAll(f.source.dir); err != nil {
			return err
		}
		return f.db.UpdateRunStatus(ctx, f.run.ID, domain.RunCompleted, "", nil, &f.now)
	})
	if err != nil {
		t.Fatal(err)
	}
	s = f.restart(t)
	got, reader, err := s.OpenArtifact(t.Context(), f.run.WorkspaceID, packet.ID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if streamErr := errors.Join(readErr, closeErr); streamErr != nil {
		t.Fatal(streamErr)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("retained bytes changed after transient cleanup/restart")
	}
	if !reflect.DeepEqual(got, artifact) {
		t.Fatalf("original observation lost: got %#v, want %#v", got, artifact)
	}
	stored, err := s.Get(t.Context(), f.run.WorkspaceID, packet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RetainedRevision == "" || stored.Captures[0].GitHead != "" || stored.Captures[0].Dirty != nil || stored.VerificationNotes != f.request("review").VerificationNotes {
		t.Fatalf("capture boundary confused with retention: %#v", stored)
	}
	if _, _, openErr := s.OpenArtifact(t.Context(), "another-workspace", packet.ID, artifact.ID); !errors.Is(openErr, store.ErrNotFound) {
		t.Fatalf("cross-workspace read: %v", openErr)
	}
	retried, err := s.Capture(t.Context(), f.request("review", artifact.ID))
	if err != nil || retried.ID != packet.ID {
		t.Fatalf("idempotent retry after source cleanup = %q, %v", retried.ID, err)
	}
	f.now = f.now.Add(DefaultRetention)
	if _, retryErr := s.Capture(t.Context(), f.request("review", artifact.ID)); !errors.Is(retryErr, ErrExpired) {
		t.Fatalf("retry at expiry = %v, want ErrExpired", retryErr)
	}
	if _, _, openErr := s.OpenArtifact(t.Context(), f.run.WorkspaceID, packet.ID, artifact.ID); !errors.Is(openErr, ErrExpired) {
		t.Fatalf("expired retained capture was readable: %v", openErr)
	}
	if _, cleanupErr := s.CleanupExpired(t.Context()); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	retainedDir, err := s.captureDirectory(captureOriginKey(f.run.ID, f.request("review").Origin, "review"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(retainedDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expiry left capture bytes: %v", statErr)
	}
	s = f.restart(t)
	f.now = f.now.Add(-DefaultRetention)
	if _, retryErr := s.Capture(t.Context(), f.request("review", artifact.ID)); !errors.Is(retryErr, ErrExpired) {
		t.Fatalf("retry after expiry cleanup and clock rollback = %v, want ErrExpired", retryErr)
	}
}

func TestRetainedCaptureRetryBindsSelectionAndNotes(t *testing.T) {
	f := newDurableCaptureFixture(t)
	s := f.service(t)
	first, original := f.source.add(t, f.run.ID, 1)
	second, _ := f.source.add(t, f.run.ID, 2)
	third, _ := f.source.add(t, f.run.ID, 3)
	req := f.request("review", first.ID, second.ID)
	packet, err := s.Capture(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if removeErr := os.RemoveAll(f.source.dir); removeErr != nil {
		t.Fatal(removeErr)
	}
	s = f.restart(t)
	for _, tc := range []struct {
		name string
		ids  []string
		note string
	}{
		{"replacement", []string{first.ID, third.ID}, req.VerificationNotes},
		{"removed", []string{first.ID}, req.VerificationNotes},
		{"reordered", []string{second.ID, first.ID}, req.VerificationNotes},
		{"notes", req.ArtifactIDs, "A different observation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := f.request(req.IdempotencyKey, tc.ids...)
			changed.VerificationNotes = tc.note
			if _, retryErr := s.Capture(t.Context(), changed); !errors.Is(retryErr, ErrInvalidRequest) {
				t.Fatalf("changed retry = %v, want ErrInvalidRequest", retryErr)
			}
		})
	}
	retried, err := s.Capture(t.Context(), req)
	if err != nil || !reflect.DeepEqual(retried, packet) {
		t.Fatalf("unchanged retry after rejected changes = %+v, %v; want %+v", retried, err, packet)
	}
	_, reader, err := s.OpenArtifact(t.Context(), f.run.WorkspaceID, packet.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if streamErr := errors.Join(readErr, closeErr); streamErr != nil {
		t.Fatal(streamErr)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("rejected retry changed retained bytes")
	}
}

func TestRetainedCaptureCountCannotBeResetByDeletingTransientCaptures(t *testing.T) {
	f := newDurableCaptureFixture(t)
	s := f.service(t)
	for batch := range 2 {
		ids := make([]string, 0, MaxCaptures/2)
		for i := 1; i <= MaxCaptures/2; i++ {
			artifact, _ := f.source.add(t, f.run.ID, batch*MaxCaptures+i)
			ids = append(ids, artifact.ID)
		}
		if _, err := s.Capture(t.Context(), f.request(fmt.Sprintf("batch-%d", batch), ids...)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(f.source.dir); err != nil {
			t.Fatal(err)
		}
	}
	s = f.restart(t)
	artifact, _ := f.source.add(t, f.run.ID, 1000)
	if _, err := s.Capture(t.Context(), f.request("overflow", artifact.ID)); err == nil {
		t.Fatal("retention bypassed count quota after deleting transient sources and restarting")
	}
	f.now = f.now.Add(DefaultRetention + time.Second)
	if _, err := s.Capture(t.Context(), f.request("expired-not-cleaned", artifact.ID)); err == nil {
		t.Fatal("timestamp alone reclaimed durable quota")
	}
	if _, err := s.CleanupExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(t.Context(), f.request("after-cleanup", artifact.ID)); err != nil {
		t.Fatalf("actual expiry cleanup did not release quota: %v", err)
	}
}

func TestCaptureQuotaIncludesInterruptedStagingBytesAndRestartCleansThem(t *testing.T) {
	f := newDurableCaptureFixture(t)
	s := f.service(t)
	origin := f.request("crashed").Origin
	key := captureOriginKey(f.run.ID, origin, "crashed")
	if err := s.beginStaging(t.Context(), f.run, origin, "", key, "crashed", f.now); err != nil {
		t.Fatal(err)
	}
	dir, err := s.captureDirectory(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range int(MaxRunCaptureBytes / MaxCaptureBytes) {
		file, err := os.Create(filepath.Join(dir, fmt.Sprintf(".capture-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		truncateErr := file.Truncate(MaxCaptureBytes)
		closeErr := file.Close()
		if err := errors.Join(truncateErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	artifact, _ := f.source.add(t, f.run.ID, 1)
	if _, err := s.Capture(t.Context(), f.request("blocked", artifact.ID)); err == nil {
		t.Fatal("staged bytes were not included in quota")
	}
	s = f.restart(t)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart left staged capture copies: %v", err)
	}
	if _, err := s.Capture(t.Context(), f.request("recovered", artifact.ID)); err != nil {
		t.Fatalf("restart cleanup did not release staged quota: %v", err)
	}
}

func TestCaptureSelectionRejectsForeignRunAndMalformedBytes(t *testing.T) {
	for _, kind := range []string{"foreign-run", "invalid-png", "size-changed", "notes-too-large"} {
		t.Run(kind, func(t *testing.T) {
			f := newDurableCaptureFixture(t)
			s := f.service(t)
			artifact, original := f.source.add(t, f.run.ID, 1)
			req := f.request(kind, artifact.ID)
			switch kind {
			case "foreign-run":
				artifact.RunID = "foreign"
			case "invalid-png":
				if err := os.WriteFile(filepath.Join(f.source.dir, artifact.ID+".png"), bytes.Repeat([]byte("x"), len(original)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "size-changed":
				artifact.Bytes--
			case "notes-too-large":
				req.VerificationNotes = strings.Repeat("x", MaxVerificationNotesBytes+1)
			}
			f.source.artifacts[artifact.ID] = artifact
			if _, err := s.Capture(t.Context(), req); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			page, err := f.db.ListEvidencePackets(t.Context(), f.run.WorkspaceID, f.run.ID, "", MaxPageSize)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 0 {
				t.Fatal("failed capture committed evidence")
			}
			artifact.RunID, artifact.Bytes = string(f.run.ID), int64(len(original))
			f.source.artifacts[artifact.ID] = artifact
			if err := os.WriteFile(filepath.Join(f.source.dir, artifact.ID+".png"), original, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Capture(t.Context(), f.request("valid", artifact.ID)); err != nil {
				t.Fatalf("failed capture leaked quota or staging: %v", err)
			}
		})
	}
}

type admissionCaptureSource struct {
	ArtifactSource
	opens      int
	afterClose func()
}

func (s *admissionCaptureSource) OpenEvidenceArtifact(ctx context.Context, run domain.RunID, id string) (protocol.DevArtifact, io.ReadCloser, error) {
	s.opens++
	artifact, reader, err := s.ArtifactSource.OpenEvidenceArtifact(ctx, run, id)
	if err != nil {
		return artifact, reader, err
	}
	return artifact, &admissionCaptureReader{ReadCloser: reader, afterClose: s.afterClose}, nil
}

type admissionCaptureReader struct {
	io.ReadCloser
	afterClose func()
}

func (r *admissionCaptureReader) Close() error {
	err := r.ReadCloser.Close()
	if r.afterClose != nil {
		r.afterClose()
	}
	return err
}

func waitCaptureAdmission(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not reach admission barrier")
	}
}

func assertDeniedCaptureRemoved(t *testing.T, f *durableCaptureFixture, s *Service, req Request) {
	t.Helper()
	packets, err := s.List(t.Context(), f.run.WorkspaceID, req.RunID, "", MaxPageSize)
	if err != nil || len(packets.Packets) != 0 {
		t.Fatalf("denied capture has View-accessible metadata: %v, %v", packets, err)
	}
	staged, err := f.db.ListRunEvidenceStaging(t.Context(), f.run.ID, MaxPageSize)
	if err != nil || len(staged) != 0 {
		t.Fatalf("denied capture left staging journal: %v, %v", staged, err)
	}
	files, err := os.ReadDir(f.cfg.EvidenceDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("denied capture left retained bytes: %v, %v", files, err)
	}
}

func TestExplicitCaptureRechecksQueuedAndStagedAuthority(t *testing.T) {
	for _, phase := range []string{"queued", "git-captured", "selected-copied"} {
		t.Run(phase, func(t *testing.T) {
			f := newDurableCaptureFixture(t)
			gate := &sync.Mutex{}
			f.cfg.AuthorizationMu = gate
			f.cfg.Transcript = &evidenceTestTranscript{data: []byte("private transcript")}
			artifact, _ := f.source.add(t, f.run.ID, 1)
			source := &admissionCaptureSource{ArtifactSource: f.source}
			f.cfg.Artifacts = source
			git := f.cfg.Git.(*workspaceCaptureGit)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			switch phase {
			case "git-captured":
				git.captureStarted, git.captureRelease = entered, release
			case "selected-copied":
				source.afterClose = func() { close(entered); <-release }
			}
			s := f.service(t)
			req := f.request("revoked", artifact.ID)
			var revoked atomic.Bool
			req.Authorize = func() error {
				if revoked.Load() {
					return permissions.ErrDenied
				}
				return nil
			}
			if err := req.Authorize(); err != nil {
				t.Fatal(err)
			}
			if phase == "queued" {
				lock := s.runLock(f.run.ID)
				lock.Lock()
				go func() { <-release; lock.Unlock() }()
			}
			done := make(chan error, 1)
			go func() {
				if phase == "queued" {
					close(entered)
				}
				_, err := s.Capture(t.Context(), req)
				done <- err
			}()
			waitCaptureAdmission(t, entered)
			gate.Lock()
			revoked.Store(true)
			gate.Unlock()
			unblock()
			select {
			case err := <-done:
				if !errors.Is(err, permissions.ErrDenied) {
					t.Fatalf("withdrawn authority admitted capture: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("withdrawn capture did not finish")
			}
			assertDeniedCaptureRemoved(t, f, s, req)
			if phase != "selected-copied" && source.opens != 0 {
				t.Fatal("opened selected private source after completed revocation")
			}
			if phase != "queued" && git.removes != 1 {
				t.Fatal("denied capture did not roll back its Git ref")
			}
		})
	}
}

type admissionPacketStore struct {
	*store.DB
	entered chan struct{}
	release chan struct{}
}

func (s *admissionPacketStore) CreateEvidencePacket(ctx context.Context, packet *store.EvidencePacket) error {
	close(s.entered)
	select {
	case <-s.release:
		return s.DB.CreateEvidencePacket(ctx, packet)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestExplicitCapturePublicationPrecedesRevocation(t *testing.T) {
	f := newDurableCaptureFixture(t)
	gate := &sync.Mutex{}
	f.cfg.AuthorizationMu = gate
	st := &admissionPacketStore{DB: f.db, entered: make(chan struct{}), release: make(chan struct{})}
	f.cfg.Store = st
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(st.release) }) }
	defer unblock()
	artifact, original := f.source.add(t, f.run.ID, 1)
	s := f.service(t)
	req := f.request("admitted", artifact.ID)
	var revoked atomic.Bool
	req.Authorize = func() error {
		if revoked.Load() {
			return permissions.ErrDenied
		}
		return nil
	}
	done := make(chan error, 1)
	var packet protocol.EvidencePacket
	go func() {
		var err error
		packet, err = s.Capture(t.Context(), req)
		done <- err
	}()
	waitCaptureAdmission(t, st.entered)
	if gate.TryLock() {
		gate.Unlock()
		t.Fatal("publication was not fenced against revocation")
	}
	revokeDone := make(chan struct{})
	go func() {
		gate.Lock()
		revoked.Store(true)
		gate.Unlock()
		close(revokeDone)
	}()
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("authorized publication failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publication did not finish")
	}
	waitCaptureAdmission(t, revokeDone)
	_, reader, err := s.OpenArtifact(t.Context(), f.run.WorkspaceID, packet.ID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if streamErr := errors.Join(readErr, closeErr); streamErr != nil || !bytes.Equal(data, original) {
		t.Fatalf("admitted evidence lost retained View access after revocation: %v", streamErr)
	}
	if _, captureErr := s.Capture(t.Context(), req); !errors.Is(captureErr, permissions.ErrDenied) {
		t.Fatalf("revoked caller retried an admitted capture: %v", captureErr)
	}
}

func TestExplicitCaptureDoesNotDeadlockDeletionAdmission(t *testing.T) {
	f := newDurableCaptureFixture(t)
	gate := &sync.Mutex{}
	f.cfg.AuthorizationMu = gate
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	f.cfg.Artifacts = &admissionCaptureSource{ArtifactSource: f.source, afterClose: func() { close(entered); <-release }}
	artifact, original := f.source.add(t, f.run.ID, 1)
	s := f.service(t)
	req := f.request("delete-race", artifact.ID)
	req.Authorize = func() error { return nil }
	done := make(chan error, 1)
	go func() {
		_, err := s.Capture(t.Context(), req)
		done <- err
	}()
	waitCaptureAdmission(t, entered)
	deleteEntered := make(chan struct{})
	deleted := make(chan error, 1)
	go func() {
		gate.Lock()
		close(deleteEntered)
		err := s.PurgeRun(t.Context(), f.run.WorkspaceID, f.run.ID, nil)
		gate.Unlock()
		deleted <- err
	}()
	waitCaptureAdmission(t, deleteEntered)
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("capture should yield to deletion admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture waited for global admission while holding the run lock")
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deletion did not acquire the released run lock")
	}
	assertDeniedCaptureRemoved(t, f, s, req)
	// A busy admission does not consume the explicit idempotency key. The
	// caller may retry once the competing cleanup admission has completed.
	f.cfg.Artifacts.(*admissionCaptureSource).afterClose = nil
	packet, err := s.Capture(t.Context(), req)
	if err != nil {
		t.Fatalf("explicit retry after busy admission: %v", err)
	}
	_, reader, err := s.OpenArtifact(t.Context(), f.run.WorkspaceID, packet.ID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if streamErr := errors.Join(readErr, closeErr); streamErr != nil || !bytes.Equal(data, original) {
		t.Fatalf("explicit retry did not retain selected capture: %v", streamErr)
	}
}

func TestExplicitCaptureIdempotencyRequiresCurrentAuthorityAndGate(t *testing.T) {
	f := newDurableCaptureFixture(t)
	artifact, _ := f.source.add(t, f.run.ID, 1)
	req := f.request("retry", artifact.ID)
	req.Authorize = func() error { return nil }
	s := f.service(t)
	if _, err := s.Capture(t.Context(), req); err == nil {
		t.Fatal("explicit capture admitted without a shared authorization gate")
	}
	assertDeniedCaptureRemoved(t, f, s, req)
	f.cfg.AuthorizationMu = &sync.Mutex{}
	s = f.service(t)
	packet, err := s.Capture(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if removeErr := os.RemoveAll(f.source.dir); removeErr != nil {
		t.Fatal(removeErr)
	}
	retry, err := s.Capture(t.Context(), req)
	if err != nil || retry.ID != packet.ID {
		t.Fatalf("authorized idempotent retry reopened missing source: %q, %v", retry.ID, err)
	}
}
