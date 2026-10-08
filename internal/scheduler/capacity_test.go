package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type admissionRuntime struct {
	*fakeRuntime
	capacityMu  sync.Mutex
	host        runtime.HostCapacity
	capacityErr error
	probe       func(context.Context) error
	create      func(context.Context) error
}

func (r *admissionRuntime) Capacity(ctx context.Context) (runtime.HostCapacity, error) {
	if r.probe != nil {
		if err := r.probe(ctx); err != nil {
			return runtime.HostCapacity{}, err
		}
	}
	r.capacityMu.Lock()
	defer r.capacityMu.Unlock()
	return r.host, r.capacityErr
}

func (r *admissionRuntime) Create(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	if r.create != nil {
		if err := r.create(ctx); err != nil {
			return "", err
		}
	}
	return r.fakeRuntime.Create(ctx, spec)
}

func (r *admissionRuntime) setHost(host runtime.HostCapacity) {
	r.capacityMu.Lock()
	defer r.capacityMu.Unlock()
	r.host = host
}

func healthyAdmissionHost() runtime.HostCapacity {
	return runtime.HostCapacity{
		Filesystems:          []runtime.FilesystemCapacity{{Name: "Docker data-root", TotalBytes: 100 << 30, FreeBytes: 80 << 30}},
		MemoryTotalBytes:     32 << 30,
		MemoryAvailableBytes: 24 << 30,
	}
}

func healthyAdmissionData(string) (disk.Usage, error) {
	return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 80 << 30}, nil
}

func newAdmissionEnv(t *testing.T, mutate func(*Config)) (*testEnv, *admissionRuntime) {
	t.Helper()
	var rt *admissionRuntime
	e := newTestEnv(t, func(cfg *Config) {
		rt = &admissionRuntime{fakeRuntime: cfg.Runtime.(*fakeRuntime), host: healthyAdmissionHost()}
		cfg.Runtime = rt
		cfg.filesystemCapacity = healthyAdmissionData
		if mutate != nil {
			mutate(cfg)
		}
	})
	return e, rt
}

func TestCapacityDiskAdmissionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		total, floor uint64
		override     int64
	}{
		{name: "small minimum", total: 40 << 30, floor: 5 << 30},
		{name: "five percent", total: 200 << 30, floor: 10 << 30},
		{name: "large maximum", total: 800 << 30, floor: 20 << 30},
		{name: "explicit override", total: 200 << 30, floor: 7 << 30, override: 7 << 30},
	} {
		for _, resource := range []string{"Aether data filesystem", "containerd snapshotter"} {
			t.Run(tc.name+"/"+resource, func(t *testing.T) {
				for _, delta := range []uint64{1, 0} {
					host := healthyAdmissionHost()
					free := tc.floor + startupDiskBytes - delta
					measure := healthyAdmissionData
					if resource == "Aether data filesystem" {
						measure = func(string) (disk.Usage, error) {
							return disk.Usage{TotalBytes: tc.total, FreeBytes: free}, nil
						}
					} else {
						host.Filesystems = append(host.Filesystems, runtime.FilesystemCapacity{Name: resource, TotalBytes: tc.total, FreeBytes: free})
					}
					s := &Scheduler{cfg: Config{Runtime: &admissionRuntime{host: host}, MinFreeBytes: tc.override, filesystemCapacity: measure}, capacityGate: make(chan struct{}, 1)}
					release, err := s.reserveCapacity(t.Context())
					if delta != 0 {
						if !errors.Is(err, ErrDiskFull) || !strings.Contains(err.Error(), resource) {
							t.Fatalf("one byte below headroom: %v", err)
						}
					} else {
						if err != nil {
							t.Fatalf("exact headroom refused: %v", err)
						}
						release()
						release() // multiple cleanup paths must not underflow accounting
					}
					if s.capacityReservations != 0 {
						t.Fatalf("reservation leaked: %d", s.capacityReservations)
					}
				}
			})
		}
	}
}

func TestCapacityMemoryAdmissionBoundaries(t *testing.T) {
	for _, total := range []uint64{16 << 30, 64 << 30} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			floor := max(uint64(2<<30), total/10)
			rt := &admissionRuntime{host: healthyAdmissionHost()}
			s := &Scheduler{cfg: Config{Runtime: rt, MinFreeBytes: -1}, capacityGate: make(chan struct{}, 1)}
			host := rt.host
			host.MemoryTotalBytes = total
			host.MemoryAvailableBytes = floor + startupMemoryBytes - 1
			rt.setHost(host)
			if _, err := s.reserveCapacity(t.Context()); !errors.Is(err, ErrMemoryPressure) || errors.Is(err, ErrDiskFull) {
				t.Fatalf("low memory with disk disabled: %v", err)
			}
			host.MemoryAvailableBytes++
			rt.setHost(host)
			release, err := s.reserveCapacity(t.Context())
			if err != nil {
				t.Fatalf("exact memory headroom refused: %v", err)
			}
			release()
		})
	}
}

func TestCapacityUnknownRefusesWithoutReservations(t *testing.T) {
	for _, resource := range []string{"data", "runtime", "memory", "missing storage"} {
		t.Run(resource, func(t *testing.T) {
			rt := &admissionRuntime{host: healthyAdmissionHost()}
			s := &Scheduler{cfg: Config{Runtime: rt, filesystemCapacity: healthyAdmissionData}, capacityGate: make(chan struct{}, 1)}
			switch resource {
			case "data":
				s.cfg.filesystemCapacity = func(string) (disk.Usage, error) { return disk.Usage{}, errors.New("statfs failed") }
			case "runtime":
				rt.capacityErr = errors.New("containerd snapshotter root is unknown")
			case "memory":
				rt.host.MemoryTotalBytes = 0
			case "missing storage":
				rt.host.Filesystems = nil
			}
			if _, err := s.reserveCapacity(t.Context()); !errors.Is(err, ErrCapacityUnknown) || errors.Is(err, ErrDiskFull) {
				t.Fatalf("unknown capacity: %v", err)
			}
			if s.capacityReservations != 0 {
				t.Fatal("failed probe leaked reservation")
			}
		})
	}
}

func TestCapacityRuntimeWithoutProbeStillChecksData(t *testing.T) {
	s := &Scheduler{cfg: Config{Runtime: newFakeRuntime(), DataDir: "data-volume", StateDir: "scheduler-volume", filesystemCapacity: func(path string) (disk.Usage, error) {
		if path == "data-volume" {
			return disk.Usage{TotalBytes: 50 << 30, FreeBytes: 1 << 30}, nil
		}
		return healthyAdmissionData(path)
	}}, capacityGate: make(chan struct{}, 1)}
	if _, err := s.reserveCapacity(t.Context()); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("data guard bypassed by custom runtime: %v", err)
	}
	s.cfg.MinFreeBytes = -1
	release, err := s.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("explicitly disabled disk guard: %v", err)
	}
	release()
}

func TestConcurrentProvisioningCannotReuseHeadroom(t *testing.T) {
	for _, resource := range []string{"memory", "disk"} {
		for _, outcome := range []string{"success", "error", "cancel"} {
			t.Run(resource+"/"+outcome, func(t *testing.T) {
				e, rt := newAdmissionEnv(t, nil)
				host := healthyAdmissionHost()
				want := ErrMemoryPressure
				if resource == "memory" {
					host.MemoryAvailableBytes = host.MemoryTotalBytes/10 + startupMemoryBytes
				} else {
					host.Filesystems[0].FreeBytes = 5<<30 + startupDiskBytes
					want = ErrDiskFull
				}
				rt.setHost(host)
				entered, proceed := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				rt.create = func(ctx context.Context) error {
					if calls.Add(1) != 1 {
						return nil
					}
					close(entered)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-proceed:
					}
					if outcome == "error" {
						return errors.New("create failed")
					}
					return nil
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				first := make(chan error, 1)
				go func() {
					_, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "first", "fake", domain.LaunchTUI)
					first <- err
				}()
				select {
				case <-entered:
				case <-time.After(waitTimeout):
					t.Fatal("first launch never reached Create")
				}
				if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "second", "fake", domain.LaunchTUI); !errors.Is(err, want) {
					t.Fatalf("concurrent launch reused headroom: %v", err)
				}
				runs, err := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID)
				if err != nil || len(runs) != 1 {
					t.Fatalf("refused launch mutated rows: %d, %v", len(runs), err)
				}
				if outcome == "cancel" {
					cancel()
				} else {
					close(proceed)
				}
				select {
				case err := <-first:
					if (err == nil) != (outcome == "success") {
						t.Fatalf("first launch outcome %s: %v", outcome, err)
					}
				case <-time.After(waitTimeout):
					t.Fatal("first launch did not finish")
				}
				if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "after release", "fake", domain.LaunchTUI); err != nil {
					t.Fatalf("reservation survived %s: %v", outcome, err)
				}
			})
		}
	}
}

func TestCapacityProbeDoesNotHoldSchedulerLockAndHonorsCancellation(t *testing.T) {
	e, rt := newAdmissionEnv(t, nil)
	entered := make(chan struct{})
	var calls atomic.Int32
	rt.probe = func(ctx context.Context) error {
		if calls.Add(1) != 1 {
			return nil
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.sched.reserveCapacity(ctx)
		done <- err
	}()
	<-entered
	snapshot := make(chan *terminalSupervision, 1)
	go func() {
		snapshot <- e.sched.lookupTerminal(e.member.ID)
	}()
	select {
	case terminal := <-snapshot:
		if terminal != nil {
			t.Fatal("capacity probe created an unrelated member terminal")
		}
	case <-time.After(time.Second):
		t.Fatal("capacity IO blocked the member terminal lookup")
	}
	canceled, cancelWait := context.WithCancel(t.Context())
	cancelWait()
	if _, err := e.sched.reserveCapacity(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting admission ignored cancellation: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("probe ignored cancellation: %v", err)
	}
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("canceled probe blocked later admission: %v", err)
	}
	release()
}

func TestCapacityRefusalPrecedesRunMutationAndDoesNotEvict(t *testing.T) {
	e, rt := newAdmissionEnv(t, nil)
	run, container := e.launchFake(t, "existing")
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "refused", "fake", domain.LaunchTUI); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("new launch under memory pressure: %v", err)
	}
	if container.currentState() != "running" {
		t.Fatal("refusal evicted existing work")
	}
	e.base.mu.Lock()
	captures := len(e.base.calls)
	e.base.mu.Unlock()
	if captures != 1 {
		t.Fatal("refused launch captured another base")
	}
	replayed, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, run.Task, run.Harness, run.Mode, domain.LaunchOptions{AssignedRunID: run.ID})
	if err != nil || replayed.ID != run.ID {
		t.Fatalf("idempotent assigned-run replay charged capacity: %+v, %v", replayed, err)
	}
	runs, err := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("refused/replayed launch created rows: %d, %v", len(runs), err)
	}
}

func TestCapacityRelaunchReusesRetainedCompute(t *testing.T) {
	for _, paused := range []bool{true, false} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			e, rt := newAdmissionEnv(t, nil)
			run, container := e.launchFake(t, "retained")
			if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
				t.Fatal(err)
			}
			e.waitStoreStatus(t, run.ID, domain.RunMerged)
			if !paused {
				if err := rt.Resume(t.Context(), container.id); err != nil {
					t.Fatal(err)
				}
				e.sched.mu.Lock()
				e.sched.runs[run.ID].paused = false
				e.sched.mu.Unlock()
			}
			host := healthyAdmissionHost()
			host.MemoryAvailableBytes = 1
			host.Filesystems[0].FreeBytes = 1
			rt.setHost(host)
			e.sched.cfg.MinFreeBytes = math.MaxInt64
			var probes atomic.Int32
			rt.probe = func(context.Context) error {
				probes.Add(1)
				return errors.New("capacity unavailable")
			}
			reopened, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID)
			if err != nil || reopened.ID != run.ID || reopened.Status != domain.RunRunning {
				t.Fatalf("reopen existing compute under pressure: %+v, %v", reopened, err)
			}
			if container.currentState() != "running" || probes.Load() != 0 {
				t.Fatalf("resume state=%s, capacity probes=%d", container.currentState(), probes.Load())
			}
			e.sched.capacityGate <- struct{}{}
			reservations := e.sched.capacityReservations
			<-e.sched.capacityGate
			if reservations != 0 {
				t.Fatalf("resume reserved startup capacity: %d", reservations)
			}
		})
	}
}

func TestCapacityRelaunchDoesNotRestartStoppedCompute(t *testing.T) {
	e, rt := newAdmissionEnv(t, nil)
	run, container := e.launchFake(t, "retained")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	closed := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	// Model an external stop that the retained owner has not observed yet.
	// Leave Wait pending so this exercises Relaunch, not exit finalization.
	container.mu.Lock()
	container.state = "stopped"
	container.mu.Unlock()
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	var creates atomic.Int32
	rt.create = func(context.Context) error {
		creates.Add(1)
		return errors.New("unexpected replacement")
	}
	_, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID)
	if !errors.Is(err, ErrInvalidTransition) || !strings.Contains(err.Error(), `resume from state "stopped"`) {
		t.Fatalf("stale retained runtime state must report unpause failure: %v", err)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Status != closed.Status || row.Reason != closed.Reason || container.currentState() != "stopped" {
		t.Fatalf("failed resume changed retained state: %+v, %v, %s", row, err, container.currentState())
	}
	if creates.Load() != 0 {
		t.Fatal("failed resume created a replacement container")
	}
	rt.setHost(healthyAdmissionHost())
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("failed resume leaked startup capacity: %v", err)
	}
	release()
}

func TestCapacityMemberEnvironmentReusesExistingContainer(t *testing.T) {
	e, rt := newAdmissionEnv(t, func(cfg *Config) {
		cfg.RunCPULimit, cfg.RunMemoryBytes, cfg.RunPidsLimit = 2.5, 12<<30, 6000
	})
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("new member environment under pressure: %v", err)
	}
	if _, err := e.db.GetTerminal(t.Context(), e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused terminal created a row: %v", err)
	}
	rt.setHost(healthyAdmissionHost())
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	container, err := rt.get(runtime.ID(terminal.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	if container.spec.CPULimit != 2.5 || container.spec.MemoryLimitBytes != 12<<30 || container.spec.PidsLimit != 6000 {
		t.Fatalf("member environment missing explicit budgets: %+v", container.spec)
	}
	_, ordinary := e.launchFake(t, "explicit budgets")
	if ordinary.spec.CPULimit != 2.5 || ordinary.spec.MemoryLimitBytes != 12<<30 || ordinary.spec.PidsLimit != 6000 {
		t.Fatalf("ordinary run missing explicit budgets: %+v", ordinary.spec)
	}
	rt.setHost(host)
	again, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil || again.ContainerID != terminal.ContainerID {
		t.Fatalf("existing terminal charged another admission: %+v, %v", again, err)
	}
}

func TestThinkingWorkersAreNotChargedTheirMemoryCeilings(t *testing.T) {
	e, rt := newAdmissionEnv(t, nil)
	host := healthyAdmissionHost()
	// Enough actual headroom for one startup at a time, despite a 32 GiB host
	// running eight workers whose individual 8 GiB ceilings total 64 GiB.
	host.MemoryAvailableBytes = host.MemoryTotalBytes/10 + startupMemoryBytes
	rt.setHost(host)
	for i := range 8 {
		run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, fmt.Sprintf("worker %d", i), "fake", domain.LaunchHeadless,
			domain.LaunchOptions{AssignedRunID: domain.RunID(fmt.Sprintf("capacity-worker-%d", i))})
		if err != nil {
			t.Fatalf("thinking worker %d charged prior maxima: %v", i, err)
		}
		container := rt.byName(string(run.ID))
		if container == nil || container.spec.MemoryLimitBytes != 8<<30 || container.spec.CPULimit != float64(min(8, goruntime.NumCPU())) || container.spec.PidsLimit != 4096 {
			t.Fatalf("worker %d has no managed budget: %+v", i, container)
		}
	}
}

func TestRunBudgetValidationPrecedesSchedulerState(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cpu          float64
		memory, pids int64
	}{
		{name: "negative CPU", cpu: -1},
		{name: "NaN CPU", cpu: math.NaN()},
		{name: "infinite CPU", cpu: math.Inf(1)},
		{name: "overflow CPU", cpu: math.MaxFloat64},
		{name: "underflow CPU", cpu: 1e-300},
		{name: "negative memory", memory: -1},
		{name: "negative processes", pids: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "must-not-exist")
			_, err := New(Config{StateDir: dir, RunCPULimit: tc.cpu, RunMemoryBytes: tc.memory, RunPidsLimit: tc.pids})
			if err == nil || !strings.Contains(err.Error(), "run-") {
				t.Fatalf("budget was not rejected first: %v", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid config created scheduler state: %v", err)
			}
		})
	}
}

func TestCapacityCanceledBeforeProbe(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			var probes atomic.Int32
			rt := &admissionRuntime{host: healthyAdmissionHost(), probe: func(context.Context) error {
				probes.Add(1)
				return nil
			}}
			s := &Scheduler{cfg: Config{Runtime: rt, MinFreeBytes: -1}, capacityGate: make(chan struct{}, 1)}
			if busy {
				s.capacityGate <- struct{}{}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := s.reserveCapacity(ctx)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCapacityUnknown) {
				t.Fatalf("canceled admission lost its capacity classification: %v", err)
			}
			if probes.Load() != 0 || s.capacityReservations != 0 {
				t.Fatal("canceled admission probed capacity or reserved headroom")
			}
			if busy {
				<-s.capacityGate
			}
			release, err := s.reserveCapacity(t.Context())
			if err != nil {
				t.Fatalf("canceled admission kept the gate: %v", err)
			}
			release()
		})
	}
}

func TestUpdaterCapacityRefusalAndCreateFailureRelease(t *testing.T) {
	e, rt := newAdmissionEnv(t, nil)
	spec := runtime.Spec{
		Name: "harness-update-capacity", CreationKey: "harness-update-capacity",
		Mounts: []runtime.Mount{{HostPath: t.TempDir(), ContainerPath: "/home/agent"}},
	}
	var creates atomic.Int32
	createFailure := errors.New("updater create failed")
	rt.create = func(context.Context) error {
		creates.Add(1)
		return createFailure
	}
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	update := &harnessUpdateRun{}
	if _, _, err := e.sched.updateInContainer(t.Context(), update, spec, "fake", "update"); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("updater bypassed memory admission: %v", err)
	}
	if creates.Load() != 0 {
		t.Fatal("refused updater reached runtime Create")
	}
	host.MemoryAvailableBytes = host.MemoryTotalBytes/10 + startupMemoryBytes
	rt.setHost(host)
	if _, _, err := e.sched.updateInContainer(t.Context(), update, spec, "fake", "update"); !errors.Is(err, createFailure) {
		t.Fatalf("updater did not reach Create with sufficient capacity: %v", err)
	}
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("failed updater leaked its startup allowance: %v", err)
	}
	release()
}
