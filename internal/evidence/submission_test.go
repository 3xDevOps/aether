package evidence

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestSubmissionSourcesHoldRetentionLocksThroughAcceptance(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("retained acceptance bytes")}, time.Now)
	packet, err := svc.Capture(ctx, Request{RunID: "run-1", IdempotencyKey: "submission-lock"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	accepted := make(chan error, 1)
	go func() {
		// The primary packet can also appear as an input: its lock must only
		// be acquired once, while both entries remain available to the caller.
		accepted <- svc.WithSubmissionSources(ctx, "workspace-1", []string{packet.ID, packet.ID}, func(packets []protocol.EvidencePacket) error {
			if len(packets) != 2 || packets[0].ID != packet.ID || packets[1].ID != packet.ID {
				return errors.New("retained source identities missing")
			}
			close(entered)
			<-release
			_, _, err := svc.ReadTranscript(ctx, "workspace-1", packet.ID, 64)
			return err
		})
	}()
	select {
	case <-entered:
	case err := <-accepted:
		t.Fatalf("source callback not reached: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("source lookup deadlocked")
	}
	purged := make(chan error, 1)
	go func() { purged <- svc.PurgeRun(ctx, "workspace-1", "run-1", nil) }()
	select {
	case err := <-purged:
		t.Fatalf("purge completed before acceptance callback: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	if err := <-accepted; err != nil {
		t.Fatalf("source disappeared before acceptance finished: %v", err)
	}
	if err := <-purged; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "workspace-1", packet.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purge did not remove source after acceptance: %v", err)
	}
}

func TestSubmissionSourcesValidateRepeatedPrimaryTranscript(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("retained primary")}, time.Now)
	packet, captureErr := svc.Capture(ctx, Request{RunID: "run-1", IdempotencyKey: "submission-primary-duplicate"})
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	stored, lookupErr := st.GetEvidencePacket(ctx, packet.ID)
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	path, pathErr := svc.artifactPath(packetCaptureKey(stored))
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatal(removeErr)
	}
	sourceErr := svc.WithSubmissionSources(ctx, "workspace-1", []string{packet.ID, packet.ID}, func(packets []protocol.EvidencePacket) error {
		if len(packets) != 2 {
			return errors.New("duplicate primary lookup lost entries")
		}
		for _, current := range packets {
			found := false
			for _, fact := range current.Sources {
				if fact.Name == "transcript" {
					found = true
					if fact.Available {
						return errors.New("duplicate primary bypassed missing transcript validation")
					}
				}
			}
			if !found {
				return errors.New("missing transcript fact")
			}
		}
		return nil
	})
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
}

type submissionLookupProbe struct {
	Store
	first chan struct{}
	once  sync.Once
}

func (p *submissionLookupProbe) GetEvidencePacket(ctx context.Context, id string) (*store.EvidencePacket, error) {
	packet, err := p.Store.GetEvidencePacket(ctx, id)
	p.once.Do(func() { close(p.first) })
	return packet, err
}

func TestSubmissionSourcesRereadAfterConcurrentExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st, _ := newEvidenceTestService(t, &evidenceTestTranscript{data: []byte("expires while waiting")}, func() time.Time { return now })
	packet, err := svc.Capture(ctx, Request{RunID: "run-1", IdempotencyKey: "submission-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	primary, captureErr := svc.Capture(ctx, Request{RunID: "run-1", IdempotencyKey: "submission-primary"})
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	probe := &submissionLookupProbe{Store: st, first: make(chan struct{})}
	svc.store = probe
	lock := svc.runLock("run-1")
	lock.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(lock.Unlock) }
	defer unlock()
	done := make(chan error, 1)
	go func() {
		done <- svc.WithSubmissionSources(ctx, "workspace-1", []string{primary.ID, packet.ID}, func(packets []protocol.EvidencePacket) error {
			if len(packets) != 2 || packets[0].Availability != protocol.EvidenceAvailable || packets[1].Availability != protocol.EvidenceExpired || packets[1].RetainedRevision != "" {
				return errors.New("pre-expiry metadata escaped locked reread")
			}
			for _, fact := range packets[1].Sources {
				if fact.Available {
					return errors.New("expired source was reclassified available")
				}
			}
			return nil
		})
	}()
	<-probe.first
	stored, err := st.GetEvidencePacket(ctx, packet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.expirePacket(ctx, stored, st, now.Add(DefaultRetention)); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
