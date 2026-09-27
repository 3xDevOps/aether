package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestBrowserSessionOutlivedByItsDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	m := mustCreateMember(t, db)
	dev := &domain.Device{Member: m.ID, Kind: domain.DeviceBrowser, Credential: "device-hash", Label: "phone"}
	if err := db.RegisterDevice(ctx, dev, true); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	s := &domain.BrowserSession{Device: dev.ID, Credential: "session-hash"}
	if err := db.CreateBrowserSession(ctx, s); err != nil {
		t.Fatalf("CreateBrowserSession: %v", err)
	}
	if err := db.CreateBrowserSession(ctx, &domain.BrowserSession{Device: "missing", Credential: "other"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session of a missing device = %v, want ErrNotFound", err)
	}
	now := time.Now().UTC()
	if err := db.TouchBrowserSession(ctx, s.ID, now); err != nil {
		t.Fatalf("TouchBrowserSession: %v", err)
	}
	got, err := db.GetBrowserSessionByCredential(ctx, "session-hash")
	if err != nil || got.ID != s.ID || got.Device != dev.ID || got.LastSeenAt == nil || !got.LastSeenAt.Equal(now) {
		t.Fatalf("GetBrowserSessionByCredential = %+v, %v", got, err)
	}
	if d, err := db.GetDevice(ctx, dev.ID); err != nil || d.LastSeenAt == nil || !d.LastSeenAt.Equal(now) {
		t.Fatalf("device after a session use = %+v, %v; want it seen", d, err)
	}

	if err := db.DeleteBrowserSession(ctx, s.ID); err != nil {
		t.Fatalf("DeleteBrowserSession: %v", err)
	}
	if _, err := db.GetBrowserSession(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after delete: %v", err)
	}
	if d, err := db.GetDevice(ctx, dev.ID); err != nil || d.Status != domain.DeviceApproved {
		t.Fatalf("device after its session ended = %+v, %v; want it approved", d, err)
	}

	s = &domain.BrowserSession{Device: dev.ID, Credential: "session-hash-2"}
	if err := db.CreateBrowserSession(ctx, s); err != nil {
		t.Fatalf("CreateBrowserSession: %v", err)
	}
	if err := db.DeleteMember(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	if _, err := db.GetBrowserSession(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived member removal: %v", err)
	}
}
