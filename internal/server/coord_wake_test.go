package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

type wakeNoPeers struct{}

func (wakeNoPeers) Overlaps(context.Context) ([]overlap.Entry, error) { return nil, nil }

type wakeServerFixture struct {
	db        *store.DB
	dir       string
	bus       *events.InProc
	control   *control.Service
	admission coordWakeAdmission
	run       *domain.Run
	member    *domain.Member
	coord     *coord.Service
}

func newWakeServerFixture(t *testing.T, child bool) *wakeServerFixture {
	t.Helper()
	ctx := context.Background()
	dir, tempErr := os.MkdirTemp("", "wake-server-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, openErr := storetest.Open(filepath.Join(dir, "state.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, busErr := events.NewInProc(ctx, nil)
	if busErr != nil {
		t.Fatal(busErr)
	}
	t.Cleanup(func() { _ = bus.Close() })
	workspace := &domain.Workspace{Name: "wake", BaseBranch: domain.DefaultBaseBranch}
	if createErr := db.CreateWorkspace(ctx, workspace); createErr != nil {
		t.Fatal(createErr)
	}
	member := &domain.Member{DisplayName: "Wake owner", TailnetLogin: "wake@example.com", Role: domain.RoleCollaborator}
	if createErr := db.CreateMember(ctx, member); createErr != nil {
		t.Fatal(createErr)
	}
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Task: "wake test", Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	f := &wakeServerFixture{db: db, dir: dir, bus: bus, run: run, member: member, control: control.New(control.Config{})}
	if child {
		parent := &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Task: "parent", Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunRunning}
		if err := db.CreateRun(ctx, parent); err != nil {
			t.Fatal(err)
		}
		reserved, _, err := db.ReserveChild(ctx, &domain.ChildReservation{
			ParentRunID: parent.ID, RootRunID: parent.ID, TreeKey: string(parent.ID), WorkspaceID: workspace.ID,
			OwnerID: member.ID, AccountID: member.ID, Harness: "omp", Mode: domain.LaunchTUI, Task: "wake child",
			IdempotencyKey: "wake-child", RequestHash: "wake-child",
		})
		if err != nil {
			t.Fatal(err)
		}
		run.ID = reserved.RunID
		if err = db.CreateRunWithID(ctx, run); err != nil {
			t.Fatal(err)
		}
	} else if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	f.admission = coordWakeAdmission{store: db, control: f.control}
	msg := &store.RunMessage{WorkspaceID: workspace.ID, FromRun: run.ID, ToRun: run.ID, Body: "durable mail", IdempotencyKey: "wake-mail"}
	if err := db.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *wakeServerFixture) client(t *testing.T, admit coord.WakeAdmission) *protocol.Client {
	t.Helper()
	svc, err := coord.New(coord.Config{Dir: filepath.Join(f.dir, "coord"), Store: f.db, Mail: f.db, Bus: f.bus, Peers: wakeNoPeers{}, WakeAdmission: admit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	f.coord = svc
	dir, err := svc.Provision(context.Background(), f.run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, coordtransport.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return protocol.NewClient(conn)
}

func (f *wakeServerFixture) hold() error {
	return f.control.Admit(string(f.run.ID), func() error {
		return f.db.SetRunProtected(context.Background(), f.run.ID, true)
	})
}

func wakeSocketCall(client *protocol.Client) <-chan struct {
	status protocol.CoordStatusResult
	err    error
} {
	done := make(chan struct {
		status protocol.CoordStatusResult
		err    error
	}, 1)
	go func() {
		var status protocol.CoordStatusResult
		err := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status)
		done <- struct {
			status protocol.CoordStatusResult
			err    error
		}{status, err}
	}()
	return done
}

func awaitWakeBarrier(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("admission barrier not reached")
	}
}

func TestCoordWakeHoldBeforeDispatchSuppressesFrame(t *testing.T) {
	f := newWakeServerFixture(t, false)
	ready, resume := make(chan struct{}), make(chan struct{})
	client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
		close(ready)
		<-resume
		return f.admission.Admit(ctx, run, dispatch)
	})
	done := wakeSocketCall(client)
	awaitWakeBarrier(t, ready)
	if err := f.hold(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	select {
	case reply := <-done:
		if reply.err != nil || !reply.status.WaitSupported || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
			t.Fatalf("hold-first response = %+v, %v", reply.status, reply.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("suppressed response missing")
	}
}

func TestCoordWakeDispatchBeforeHoldOrdersAcceptedFrame(t *testing.T) {
	f := newWakeServerFixture(t, false)
	ready, resume, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
		return f.admission.Admit(ctx, run, func() error {
			close(ready)
			<-resume
			err := dispatch()
			close(written)
			return err
		})
	})
	done := wakeSocketCall(client)
	awaitWakeBarrier(t, ready)
	holding, held := make(chan struct{}), make(chan error, 1)
	go func() { close(holding); held <- f.hold() }()
	awaitWakeBarrier(t, holding)
	select {
	case err := <-held:
		t.Fatalf("hold passed an in-flight admitted frame: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	select {
	case reply := <-done:
		if reply.err != nil || !reply.status.WakeAdmitted {
			t.Fatalf("dispatch-first response = %+v, %v", reply.status, reply.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admitted frame missing")
	}
	awaitWakeBarrier(t, written)
	select {
	case err := <-held:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hold did not follow accepted frame")
	}
}

func TestCoordWakeAdmissionFailsClosedForUnavailableAndFinishedRuns(t *testing.T) {
	for _, mode := range []string{"missing-control", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := newWakeServerFixture(t, false)
			switch mode {
			case "missing-control":
				f.admission.control = nil
			case "terminal":
				if _, err := f.db.UpdateRunStatus(context.Background(), f.run.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			client := f.client(t, f.admission.Admit)
			var status protocol.CoordStatusResult
			if err := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status); err != nil {
				t.Fatal(err)
			}
			if !status.WaitSupported || status.WakeAdmitted || len(status.UnreadMessageIDs) != 1 {
				t.Fatalf("unavailable authority admitted %+v", status)
			}
		})
	}
}

func TestCoordWakeRefusesAChildAMemberControls(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "ordinary run is woken"
		if child {
			name = "child run keeps its mail"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, child)
			if _, _, err := f.control.Acquire(string(f.run.ID), string(f.member.ID), "tab", false); err != nil {
				t.Fatal(err)
			}
			var status protocol.CoordStatusResult
			if err := f.client(t, f.admission.Admit).Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status); err != nil {
				t.Fatal(err)
			}
			if status.WakeAdmitted == child || len(status.UnreadMessageIDs) != 1 {
				t.Fatalf("wake of a controlled run (child %v) = %+v", child, status)
			}
		})
	}
}

func TestCoordWakeDefersToControlBeforeSynchronousCleanup(t *testing.T) {
	f := newWakeServerFixture(t, false)
	observed, releaseWake := make(chan struct{}), make(chan struct{})
	resumeWake := sync.OnceFunc(func() { close(releaseWake) })
	defer resumeWake()
	client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
		close(observed)
		<-releaseWake
		return f.admission.Admit(ctx, run, dispatch)
	})
	wake := wakeSocketCall(client)
	awaitWakeBarrier(t, observed)
	entered, cleanup, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(abort)
	released := make(chan error, 1)
	go func() {
		released <- f.control.Admit(string(f.run.ID), func() error {
			close(entered)
			select {
			case <-abort:
				return context.Canceled
			case <-cleanup:
				return f.coord.Release(f.run.ID)
			}
		})
	}()
	awaitWakeBarrier(t, entered)
	resumeWake()
	select {
	case reply := <-wake:
		if reply.err != nil || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
			t.Fatalf("wake during control cleanup = %+v, %v", reply.status, reply.err)
		}
	case <-time.After(time.Second):
		t.Fatal("wake waited on Control while retaining cleanup's run reference")
	}
	close(cleanup)
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Control-owned cleanup did not finish")
	}
}
