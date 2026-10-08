package evidence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestExpiredPrefixCaptureSurvivesRestartAndHistoryRemoval(t *testing.T) {
	ctx := context.Background()
	transcript := &evidenceTestTranscript{data: []byte("retained tail\n"), prefixExpired: true}
	svc, st, git := newEvidenceTestService(t, transcript, time.Now)
	req := Request{RunID: "run-1", IdempotencyKey: "expired-prefix"}
	key := captureKey(req.RunID, "member-1", req.IdempotencyKey)
	// Simulate a restart after the transcript and marker reached disk but
	// before packet insertion. The new service must not consult live history.
	fact, _, created, err := svc.retainTranscript(ctx, req.RunID, key)
	if err != nil || !created || !fact.Truncated || fact.Reason != "earlier transcript history expired" {
		t.Fatalf("retained prefix = %+v, created=%v, err=%v", fact, created, err)
	}
	transcript.data = nil
	transcript.err = errors.New("recording removed by history GC")
	restarted, err := New(Config{Store: st, Git: git, Transcript: transcript, Runs: svc.runs,
		Events: evidenceTestEvents{seq: 42}, EvidenceDir: svc.root})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := restarted.Capture(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	data, truncated, err := restarted.ReadTranscript(ctx, "workspace-1", packet.ID, 0)
	if err != nil || !truncated || string(data) != "retained tail\n" {
		t.Fatalf("retained bytes = %q, truncated=%v, err=%v", data, truncated, err)
	}
	retry, err := restarted.Capture(ctx, req)
	if err != nil || retry.ID != packet.ID {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	assertSubmissionTranscript(t, restarted, packet.ID, true, true)
	if _, _, readErr := restarted.ReadTranscript(ctx, "other-workspace", packet.ID, 0); readErr == nil {
		t.Fatal("retained history escaped workspace authorization")
	}
	stored, err := st.GetEvidencePacket(ctx, packet.ID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := restarted.artifactPath(StorageKey(stored))
	if err != nil {
		t.Fatal(err)
	}
	if truncateErr := os.Truncate(path, 2); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	assertSubmissionTranscript(t, restarted, packet.ID, false, true)
	if _, _, _, retainErr := restarted.retainTranscript(ctx, req.RunID, key); retainErr == nil {
		t.Fatal("retry accepted a damaged artifact as a shorter prefix capture")
	}
}

func assertSubmissionTranscript(t *testing.T, svc *Service, id string, available, truncated bool) {
	t.Helper()
	err := svc.WithSubmissionSources(context.Background(), "workspace-1", []string{id}, func(packets []protocol.EvidencePacket) error {
		for _, source := range packets[0].Sources {
			if source.Name == "transcript" {
				if source.Available != available || source.Truncated != truncated {
					t.Fatalf("submission transcript = %+v, want available=%v truncated=%v", source, available, truncated)
				}
				return nil
			}
		}
		return errors.New("missing transcript fact")
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptCaptureDistinguishesPrefixAndCopyCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int
		prefix bool
		cap    bool
	}{
		{name: "empty-prefix", prefix: true},
		{name: "exact-complete", size: MaxTranscriptBytes},
		{name: "copy-cap", size: MaxTranscriptBytes + 1, cap: true},
		{name: "both", size: MaxTranscriptBytes + 1, prefix: true, cap: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: bytes.Repeat([]byte("x"), tc.size), prefixExpired: tc.prefix}, time.Now)
			packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range packet.Sources {
				if source.Name == "transcript" && (source.Truncated != (tc.prefix || tc.cap) || strings.Contains(source.Reason, "expired") != tc.prefix || strings.Contains(source.Reason, "capped") != tc.cap) {
					t.Fatalf("capture causes = %+v", source)
				}
			}
			assertSubmissionTranscript(t, svc, packet.ID, true, tc.prefix || tc.cap)
		})
	}
}

func TestLegacyTranscriptMarkersMigrateWithoutRelaxingCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		size  int
		valid bool
	}{
		{name: "complete", state: "complete\n", size: 4, valid: true},
		{name: "exact-complete", state: "complete\n", size: MaxTranscriptBytes, valid: true},
		{name: "capped", state: "truncated\n", size: MaxTranscriptBytes, valid: true},
		{name: "damaged-cap", state: "truncated\n", size: 4},
		{name: "unknown", state: "partial\n", size: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{err: errors.New("source gone")}, time.Now)
			key := captureKey("run-1", "member-1", tc.name)
			path, err := svc.artifactPath(key)
			if err != nil {
				t.Fatal(err)
			}
			markerPath, err := svc.truncationMarkerPath(key)
			if err != nil {
				t.Fatal(err)
			}
			if writeErr := os.WriteFile(path, bytes.Repeat([]byte("x"), tc.size), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if writeErr := os.WriteFile(markerPath, []byte(tc.state), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
			fact, _, created, err := svc.retainTranscript(context.Background(), "run-1", key)
			if (err == nil) != tc.valid || created {
				t.Fatalf("legacy reuse = %+v, created=%v, err=%v", fact, created, err)
			}
			if tc.valid {
				marker, known := svc.readTruncationMarker(markerPath, int64(tc.size))
				if !known || marker.legacy || marker.PrefixExpired || marker.CopyCapped != (tc.state == "truncated\n") {
					t.Fatalf("migrated marker = %+v, known=%v", marker, known)
				}
			}
		})
	}
}

type failedPrefixTranscript struct{}

func (failedPrefixTranscript) Replay(domain.RunID) (io.ReadCloser, bool, error) {
	return io.NopCloser(io.MultiReader(strings.NewReader("partial"), transcriptReadFailure{})), true, nil
}

type transcriptReadFailure struct{}

func (transcriptReadFailure) Read([]byte) (int, error) {
	return 0, errors.New("private /host/transcript failed")
}

func TestPrefixCaptureReadFailureNeverPublishesPartialArtifact(t *testing.T) {
	svc, _, _ := newEvidenceTestService(t, nil, time.Now)
	svc.transcript = failedPrefixTranscript{}
	packet, err := svc.Capture(context.Background(), Request{RunID: "run-1", IdempotencyKey: "read-failure"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range packet.Sources {
		if source.Name == "transcript" && (source.Available || strings.Contains(source.Reason, "/host")) {
			t.Fatalf("failed read published transcript: %+v", source)
		}
	}
	if _, _, err := svc.ReadTranscript(context.Background(), "workspace-1", packet.ID, 0); !errors.Is(err, ErrTranscriptUnavailable) {
		t.Fatalf("partial read = %v", err)
	}
}
