package evidence

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

func TestWithCandidateSourceSerializesPurge(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("candidate transcript")}, func() time.Time {
		return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	})
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "candidate-lock"})
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	sourceDone := make(chan error, 1)
	go func() {
		sourceDone <- svc.WithCandidateSource(context.Background(), "workspace-1", packet.ID, func(got *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
			if got == nil || transcript == nil {
				return errors.New("candidate source was not opened")
			}
			close(entered)
			<-release
			_, err := io.Copy(io.Discard, transcript)
			return err
		})
	}()
	<-entered

	purgeDone := make(chan error, 1)
	go func() {
		purgeDone <- svc.PurgeRun(context.Background(), "workspace-1", "run-1", nil)
	}()
	select {
	case err := <-purgeDone:
		t.Fatalf("purge completed while candidate source callback held lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-sourceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-purgeDone; err != nil {
		t.Fatal(err)
	}
}

func TestWithCandidateSourcePreservesResourcesOnCallbackError(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("preserve me")}, func() time.Time {
		return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	})
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "candidate-error"})
	if err != nil {
		t.Fatal(err)
	}
	preserveErr := errors.New("candidate copy failed")
	err = svc.WithCandidateSource(context.Background(), "workspace-1", packet.ID, func(_ *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
		if _, readErr := io.ReadAll(transcript); readErr != nil {
			return readErr
		}
		return preserveErr
	})
	if !errors.Is(err, preserveErr) {
		t.Fatalf("callback error = %v, want wrapped preservation error", err)
	}
	got, _, err := svc.ReadTranscript(context.Background(), "workspace-1", packet.ID, 0)
	if err != nil || string(got) != "preserve me" {
		t.Fatalf("source after callback error = %q, %v", got, err)
	}
}

func TestWithCandidateSourceReportsUnavailableTranscript(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{err: errors.New("source unavailable")}, func() time.Time {
		return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	})
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "candidate-unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.WithCandidateSource(context.Background(), "workspace-1", packet.ID, func(got *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
		if got == nil || transcript != nil {
			t.Fatalf("unavailable source callback = packet %v, transcript %v; want packet and nil transcript", got != nil, transcript)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWithCandidateSourceRejectsAbsentAndExpiredPackets(t *testing.T) {
	svc, st, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("expired")}, func() time.Time {
		return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	})
	if err := svc.WithCandidateSource(context.Background(), "workspace-1", "missing", func(*store.EvidencePacket, string, io.ReadCloser) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing packet error = %v, want store.ErrNotFound", err)
	}
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "candidate-expired"})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	expiredAt := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	st.packets[packet.ID].ExpiresAt = &expiredAt
	st.mu.Unlock()
	if err := svc.WithCandidateSource(context.Background(), "workspace-1", packet.ID, func(*store.EvidencePacket, string, io.ReadCloser) error { return nil }); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired packet error = %v, want ErrExpired", err)
	}
}

func TestWithCandidateSourceClosesTranscriptAfterCallback(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("close after callback")}, time.Now)
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "candidate-close"})
	if err != nil {
		t.Fatal(err)
	}
	var provided io.ReadCloser
	if err := svc.WithCandidateSource(context.Background(), "workspace-1", packet.ID, func(_ *store.EvidencePacket, _ string, transcript io.ReadCloser) error {
		provided = transcript
		data, err := io.ReadAll(transcript)
		if err != nil {
			return err
		}
		if string(data) != "close after callback" {
			t.Fatalf("copied transcript = %q", data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if provided == nil {
		t.Fatal("candidate callback did not receive transcript")
	}
	var one [1]byte
	if _, err := provided.Read(one[:]); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("transcript read after callback = %v, want os.ErrClosed", err)
	}
}
