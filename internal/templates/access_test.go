package templates

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// A schedule fires as its creator: once they lose access to the template's
// workspace, its next slot launches nothing.
func TestScheduleFireNeedsTheCreatorsAccessToTheWorkspace(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	m := &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey, Color: "#e6194b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, m); err != nil {
		t.Fatalf("create member: %v", err)
	}
	ws := &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err = db.GrantWorkspace(ctx, ws.ID, m.ID, ""); err != nil {
		t.Fatalf("grant workspace: %v", err)
	}
	tpl := &store.Template{WorkspaceID: ws.ID, Name: "hourly", Task: "sweep", Harness: "claude", Mode: domain.LaunchHeadless}
	if err = db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	if err = db.SaveSchedule(ctx, &store.Schedule{TemplateID: tpl.ID, Cron: "0 * * * *", MemberID: m.ID, CreatedAt: time.Now()}, true); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	clock := &fakeClock{t: time.Date(2026, 8, 13, 3, 30, 0, 0, time.UTC)}
	runs := &recordingLauncher{}
	svc, err := New(Config{Store: db, Bus: bus, Runs: runs, Now: clock.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fireAt := func(at time.Time) {
		t.Helper()
		clock.set(at)
		if err = svc.scan(ctx, true); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}

	fireAt(clock.now())
	fireAt(time.Date(2026, 8, 13, 4, 0, 1, 0, time.UTC))
	if n := runs.count(); n != 1 {
		t.Fatalf("launches while the creator reaches the workspace = %d, want one", n)
	}
	if err = db.RevokeWorkspaceGrant(ctx, ws.ID, m.ID, ""); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	fireAt(time.Date(2026, 8, 13, 5, 0, 1, 0, time.UTC))
	if n := runs.count(); n != 1 {
		t.Fatalf("launches after the creator lost the workspace = %d, want still one", n)
	}
}
