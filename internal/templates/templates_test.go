package templates

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

type recordingLauncher struct {
	mu    sync.Mutex
	tasks []string
}

func (l *recordingLauncher) Launch(_ context.Context, workspace domain.WorkspaceID, member, _ domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tasks = append(l.tasks, task)
	return &domain.Run{
		ID:            "run_1",
		WorkspaceID:   workspace,
		MemberID:      member,
		Task:          task,
		Harness:       harness,
		Mode:          mode,
		BaseCommit:    "0123456789abcdef0123456789abcdef01234567",
		BaseBranch:    "main",
		BaseSource:    "local",
		BaseCheckedAt: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC),
	}, nil
}

func (l *recordingLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tasks)
}

// fakeClock is the cron loop's clock, wound forward by the test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// A schedule whose slots all passed while the server was down fires once
// at its next real slot and never for the ones it missed: no catch-up
// storm at boot.
func TestScheduleMissedWhileDownIsSkippedNotCaughtUp(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
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
	ws := &domain.Workspace{
		Name:        "proj",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tpl := &store.Template{WorkspaceID: ws.ID, Name: "hourly", Task: "sweep", Harness: "claude", Mode: domain.LaunchHeadless}
	if err = db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	// Stored a week ago, hourly, never fired since: 168 slots are owed if
	// the loop ever tried to catch up.
	weekAgo := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
	sc := &store.Schedule{TemplateID: tpl.ID, Cron: "0 * * * *", MemberID: m.ID, CreatedAt: weekAgo}
	if err = db.SaveSchedule(ctx, sc); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if err = db.MarkScheduleFired(ctx, sc.ID, weekAgo); err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	now := time.Date(2026, 8, 13, 3, 30, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	runs := &recordingLauncher{}
	svc, err := New(Config{
		Store: db, Bus: bus, Runs: runs,
		Interval: 5 * time.Millisecond,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	// Well past several missed slots' worth of scans, still nothing owed.
	time.Sleep(50 * time.Millisecond)
	if n := runs.count(); n != 0 {
		t.Fatalf("launches after start = %d, want none: missed slots are skipped", n)
	}

	clock.set(time.Date(2026, 8, 13, 4, 0, 1, 0, time.UTC))
	deadline := time.Now().Add(5 * time.Second)
	for runs.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runs.count(); n != 1 {
		t.Fatalf("launches after the next real slot = %d, want exactly one", n)
	}
}

// "0 0 31 4 *" - April 31st - is a valid set of cron fields naming a
// date the calendar never reaches: it parses, and then it has no next
// slot at all. Saving one is refused, and a row that already carries one
// stays quiet instead of being due on every scan forever.
func TestImpossibleCronRuleIsRefusedAndNeverFires(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
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
	ws := &domain.Workspace{
		Name:        "proj",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tpl := &store.Template{WorkspaceID: ws.ID, Name: "nightly-deps", Task: "sweep", Harness: "claude", Mode: domain.LaunchHeadless}
	if err = db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}

	now := time.Date(2026, 8, 13, 3, 30, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	runs := &recordingLauncher{}
	svc, err := New(Config{
		Store: db, Bus: bus, Runs: runs,
		Interval: 5 * time.Millisecond,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, saveErr := svc.SaveSchedule(ctx, ws.ID, tpl.Name, "0 0 31 4 *", m.ID); !errors.Is(saveErr, ErrInvalidDefinition) {
		t.Fatalf("SaveSchedule error = %v, want ErrInvalidDefinition", saveErr)
	}

	// A row written straight to the store stands in for one saved before
	// the check existed, or by any other writer.
	if err = db.SaveSchedule(ctx, &store.Schedule{TemplateID: tpl.ID, Cron: "0 0 31 4 *", MemberID: m.ID}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	time.Sleep(50 * time.Millisecond)
	if n := runs.count(); n != 0 {
		t.Fatalf("launches from an impossible rule = %d, want none", n)
	}
}

// A cron fire supplies no parameters, so a template with a placeholder
// that has no default could only ever fail unattended. Scheduling one is
// refused at save time, naming the parameter; a template whose
// placeholders all have defaults schedules and fires; and a scheduled
// template cannot later be edited into a prompt it can never render.
func TestScheduleRequiresATemplateThatRendersUnattended(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
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
	ws := &domain.Workspace{
		Name:        "proj",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	now := time.Date(2026, 8, 13, 3, 30, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	runs := &recordingLauncher{}
	svc, err := New(Config{
		Store: db, Bus: bus, Runs: runs,
		Interval: 5 * time.Millisecond,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Saving the template itself is fine: a person launching it by hand
	// supplies the value.
	open := &store.Template{WorkspaceID: ws.ID, Name: "triage", Task: "triage {{ticket}}", Harness: "claude"}
	if err = svc.Save(ctx, open); err != nil {
		t.Fatalf("save open template: %v", err)
	}
	_, err = svc.SaveSchedule(ctx, ws.ID, "triage", "0 * * * *", m.ID)
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("SaveSchedule error = %v, want ErrInvalidDefinition", err)
	}
	if !strings.Contains(err.Error(), "ticket") {
		t.Fatalf("SaveSchedule error = %q, want it to name the missing parameter", err)
	}
	stored, err := db.ListSchedules(ctx, ws.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("schedules stored = %d, want none: the save was refused", len(stored))
	}

	ready := &store.Template{
		WorkspaceID: ws.ID, Name: "sweep", Task: "sweep {{ecosystem}}",
		Params: map[string]string{"ecosystem": "go"}, Harness: "claude",
	}
	if err = svc.Save(ctx, ready); err != nil {
		t.Fatalf("save defaulted template: %v", err)
	}
	if _, err = svc.SaveSchedule(ctx, ws.ID, "sweep", "0 * * * *", m.ID); err != nil {
		t.Fatalf("SaveSchedule for a fully defaulted template: %v", err)
	}
	if err = svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	clock.set(time.Date(2026, 8, 13, 4, 0, 1, 0, time.UTC))
	deadline := time.Now().Add(5 * time.Second)
	for runs.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	runs.mu.Lock()
	tasks := append([]string(nil), runs.tasks...)
	runs.mu.Unlock()
	if len(tasks) != 1 || tasks[0] != "sweep go" {
		t.Fatalf("fired tasks = %v, want exactly [\"sweep go\"]", tasks)
	}

	// Editing the scheduled template into an undefaulted placeholder would
	// break every future fire, so it is refused too.
	ready.Task = "sweep {{ecosystem}} for {{ticket}}"
	err = svc.Save(ctx, ready)
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("Save of a scheduled template with a new undefaulted parameter = %v, want ErrInvalidDefinition", err)
	}
	if !strings.Contains(err.Error(), "ticket") {
		t.Fatalf("Save error = %q, want it to name the missing parameter", err)
	}
}

// testPublicKey is a throwaway key: members need one identity.
const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF3jVX1WCbXCEjHVFVBExpFvhOsSJfLNJDDXCM4Q3xJd test"

// listGateStore holds the first ListSchedules open until the test releases
// it, which is the window a concurrent SaveSchedule lands in.
type listGateStore struct {
	store.Store
	once     sync.Once
	released sync.Once
	listed   chan struct{}
	release  chan struct{}
}

func (g *listGateStore) ListSchedules(ctx context.Context, workspace domain.WorkspaceID) ([]*store.Schedule, error) {
	out, err := g.Store.ListSchedules(ctx, workspace)
	g.once.Do(func() {
		close(g.listed)
		<-g.release
	})
	return out, err
}

func (g *listGateStore) open() { g.released.Do(func() { close(g.release) }) }

// gateEnv is the fixture the two save-during-a-scan tests share: a service
// over a store whose first schedule listing the test holds open, and one
// template ready to schedule.
type gateEnv struct {
	svc      *Service
	gate     *listGateStore
	ws       domain.WorkspaceID
	tpl      string
	owner    domain.MemberID
	scanned  chan error
	waitOnce sync.Once
	scanErr  error
}

func newGateEnv(t *testing.T, clock *fakeClock) *gateEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	m := &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey, Color: "#e6194b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, m); err != nil {
		t.Fatalf("create member: %v", err)
	}
	ws := &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tpl := &store.Template{WorkspaceID: ws.ID, Name: "nightly-deps", Task: "sweep", Harness: "claude", Mode: domain.LaunchHeadless}
	if err = db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
	gate := &listGateStore{Store: db, listed: make(chan struct{}), release: make(chan struct{})}
	svc, err := New(Config{Store: gate, Bus: bus, Runs: &recordingLauncher{}, Now: clock.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &gateEnv{svc: svc, gate: gate, ws: ws.ID, tpl: tpl.Name, owner: m.ID}
}

// startScan runs one scan and returns once its listing is held open. The
// cleanup waits too, so an assertion that fails before waitScan cannot leave
// the scan goroutine blocked in the gate or racing the store close.
func (e *gateEnv) startScan(t *testing.T) {
	t.Helper()
	e.scanned = make(chan error, 1)
	go func() { e.scanned <- e.svc.scan(context.Background(), false) }()
	t.Cleanup(func() { _ = e.waitScan() })
	<-e.gate.listed
}

func (e *gateEnv) waitScan() error {
	e.waitOnce.Do(func() {
		e.gate.open()
		e.scanErr = <-e.scanned
	})
	return e.scanErr
}

// A schedule saved while a scan's listing is in flight is missing from that
// listing and present in the timer map. Pruning the map against the stale
// listing would drop the slot SaveSchedule just seeded, and the next scan
// would re-seed it from a later now - silently moving the first fire a slot
// out, or losing it entirely against a clock that is not advancing.
func TestScheduleSavedDuringAScanKeepsItsSeededSlot(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}
	e := newGateEnv(t, clock)

	e.startScan(t)
	saved, err := e.svc.SaveSchedule(context.Background(), e.ws, e.tpl, "* * * * *", e.owner)
	if err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	if scanErr := e.waitScan(); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}

	if next := e.svc.nextFire(saved.Schedule.ID); !next.Equal(saved.Next) {
		t.Fatalf("next fire after the scan = %v, want the seeded %v", next, saved.Next)
	}
}

// Re-saving a schedule with a new rule lands in the same window from the
// other side: the stale listing carries the old rule and the timer carries
// the new one, so the schedule is present in both and the prune guard alone
// does not cover it. Rebuilding the timer from the stale row would throw
// away the slot SaveSchedule just returned, reporting the old rule's next
// fire everywhere it is shown and skipping the new rule's first slot when it
// falls before the next scan.
func TestScheduleRuleChangedDuringAScanKeepsItsSeededSlot(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)}
	e := newGateEnv(t, clock)

	first, err := e.svc.SaveSchedule(ctx, e.ws, e.tpl, "0 4 * * *", e.owner)
	if err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	e.startScan(t)
	resaved, err := e.svc.SaveSchedule(ctx, e.ws, e.tpl, "0 5 * * *", e.owner)
	if err != nil {
		t.Fatalf("re-save SaveSchedule: %v", err)
	}
	if scanErr := e.waitScan(); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}

	// A re-save upserts on the template, so this is the same schedule with a
	// new rule rather than a new row the prune guard would already cover.
	if resaved.Schedule.ID != first.Schedule.ID {
		t.Fatalf("re-saved schedule id = %q, want the upserted %q", resaved.Schedule.ID, first.Schedule.ID)
	}
	if next := e.svc.nextFire(resaved.Schedule.ID); !next.Equal(resaved.Next) {
		t.Fatalf("next fire after the scan = %v, want the seeded %v", next, resaved.Next)
	}
}
