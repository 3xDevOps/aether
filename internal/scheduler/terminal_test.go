package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestBuildTerminalPlanWithoutWorkspace(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	plan, err := e.sched.BuildEnvironmentPlan(context.Background(), nil, nil, e.member, profileForTerminalTest(), EnvironmentPurposeTerminal)
	if err != nil {
		t.Fatalf("BuildEnvironmentPlan: %v", err)
	}
	if plan.Image != "standard:latest" || plan.SetupScript != "" {
		t.Fatalf("plan = %+v", plan)
	}
	if _, ok := plan.Env["WS"]; ok {
		t.Fatalf("terminal plan leaked workspace variables: %+v", plan.Env)
	}
	if plan.Env["HOME"] != "/root" || plan.Env["TERM"] != "xterm-256color" {
		t.Fatalf("terminal environment = %+v", plan.Env)
	}
	if _, err := e.sched.BuildEnvironmentPlan(context.Background(), nil, nil, e.member, profileForTerminalTest(), EnvironmentPurposeRun); err == nil {
		t.Fatal("run plan without workspace unexpectedly succeeded")
	}
}

func TestEnsureTerminalCreatesPersistentContainer(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	terminal, err := e.sched.EnsureTerminal(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if terminal.ContainerID == "" || terminal.Image != "standard:latest" {
		t.Fatalf("terminal = %+v", terminal)
	}
	status, err := e.sched.TerminalStatus(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if !status.Running || len(status.Tabs) != 1 || status.Tabs[0] != "main" {
		t.Fatalf("status = %+v", status)
	}
	if err := e.sched.StopTerminal(context.Background(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if _, err := e.db.GetTerminal(context.Background(), e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal row after stop: %v", err)
	}
}

func TestRecoveredTerminalUsesCapturedUserAndHomeForImages(t *testing.T) {
	e := newTestEnv(t, nil)
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	user := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	wantUser := user
	if os.Geteuid() == 0 {
		wantUser = ""
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.mu.Lock()
	container.spec.User = user
	container.spec.Env["HOME"] = "/home/actual"
	container.mu.Unlock()
	if updateErr := e.db.UpdateMemberImage(t.Context(), e.member.ID, "mutable:latest"); updateErr != nil {
		t.Fatalf("UpdateMemberImage: %v", updateErr)
	}

	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close before recovery: %v", closeErr)
	}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, recovered)
	waitFor(t, "terminal recovery", func() bool {
		return recovered.lookupTerminal(e.member.ID) != nil
	})

	adoptedTerminal, err := recovered.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal adoption: %v", err)
	}
	adopted := recovered.lookupTerminal(e.member.ID)
	if adoptedTerminal.ContainerID != first.ContainerID || adopted == nil || adopted.runUser != wantUser || adopted.home != "/home/actual" {
		t.Fatalf("adopted terminal = %+v, metadata = %+v, want user %s and HOME /home/actual", adoptedTerminal, adopted, wantUser)
	}
	image := []byte("image")
	path, err := recovered.SaveTerminalImage(t.Context(), e.member.ID, "", ".png", image)
	if err != nil {
		t.Fatalf("SaveTerminalImage: %v", err)
	}
	if got := readSavedTerminalImage(t, e, e.member.ID, path); string(got) != string(image) {
		t.Fatalf("saved image = %q, want %q", got, image)
	}
	if !strings.HasPrefix(path, "/home/actual/.aether/terminal-images/") {
		t.Fatalf("image path = %q, want captured terminal HOME", path)
	}
}

type failingPutTerminalStore struct {
	store.Store
	fail      bool
	deleteErr error
}

func (s *failingPutTerminalStore) PutTerminal(ctx context.Context, terminal *domain.Terminal) error {
	if s.fail {
		return errors.New("test: PutTerminal unavailable")
	}
	return s.Store.PutTerminal(ctx, terminal)
}

func (s *failingPutTerminalStore) DeleteTerminal(ctx context.Context, member domain.MemberID) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Store.DeleteTerminal(ctx, member)
}

type failingTerminalFindRuntime struct {
	runtime.Runtime
	findErr error
}

func (r *failingTerminalFindRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	return "", r.findErr
}

type failingDestroyRuntime struct {
	*scriptedWaitRuntime
	destroyErr error
}

func (r *failingDestroyRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	if r.destroyErr != nil {
		return r.destroyErr
	}
	return r.fakeRuntime.Destroy(ctx, id)
}

func assertCleanupPendingTerminalIsNotLive(t *testing.T, e *testEnv) {
	t.Helper()
	waitFor(t, "pending terminal status to stop reporting live", func() bool {
		status, err := e.sched.TerminalStatus(t.Context(), e.member.ID)
		return err == nil && !status.Running && len(status.Tabs) == 0
	})
	if _, err := e.sched.TerminalContainerAddr(t.Context(), e.member.ID); err == nil || err.Error() != "environment terminal is not running" {
		t.Fatalf("TerminalContainerAddr for pending terminal = %v, want not running", err)
	}
	if _, err := e.sched.SaveEnvironment(t.Context(), e.member.ID); !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("SaveEnvironment for pending terminal = %v, want %v", err, ErrTerminalNotRunning)
	}
	if _, err := e.sched.SaveTerminalImage(t.Context(), e.member.ID, "", ".png", []byte("pending terminal image")); err == nil || !strings.Contains(err.Error(), "environment terminal is not running") {
		t.Fatalf("SaveTerminalImage for pending terminal = %v, want not running", err)
	}
	if _, err := e.sched.ConnectGitHub(t.Context(), e.member.ID); !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("ConnectGitHub for pending terminal = %v, want %v", err, ErrTerminalNotRunning)
	}
	if _, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID); !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("ProbeGitHubCLI for pending terminal = %v, want %v", err, ErrTerminalNotRunning)
	}
}

func TestRecoveredTerminalAttachFailurePreservesAndRetries(t *testing.T) {
	e := newTestEnv(t, nil)
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.mu.Lock()
	container.spec.User = "1000:1000"
	container.mu.Unlock()
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	failures := 1
	attachHook := func(context.Context, runtime.ID) (runtime.Attachment, error) {
		if failures > 0 {
			failures--
			return nil, errors.New("test: attach unavailable")
		}
		return nil, errors.New("test: attach hook exhausted")
	}
	e.rt.mu.Lock()
	e.rt.attachHook = attachHook
	e.rt.mu.Unlock()
	row, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetTerminal: %v", err)
	}
	lock := s2.terminalLock(e.member.ID)
	lock.Lock()
	recoverErr := s2.recoverTerminalLocked(t.Context(), e.member, row)
	lock.Unlock()
	if recoverErr == nil {
		t.Fatal("recovery unexpectedly succeeded while attach was unavailable")
	}
	sup := s2.lookupTerminal(e.member.ID)
	if sup == nil || sup.containerID != runtime.ID(first.ContainerID) {
		t.Fatalf("surviving terminal supervision = %+v", sup)
	}
	if sup.userReservation == nil || sup.runUser != "1000:1000" {
		t.Fatalf("surviving terminal reservation = %+v", sup)
	}
	if container.currentState() != "running" {
		t.Fatalf("container state = %q, want running", container.currentState())
	}
	e.rt.mu.Lock()
	e.rt.attachHook = nil
	e.rt.mu.Unlock()
	retried, err := s2.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal retry: %v", err)
	}
	conflicting := &supervised{runID: "conflicting-run", memberID: e.member.ID}
	if err := s2.reserveRunUser(conflicting, "2000:2000", true); err == nil {
		t.Fatal("incompatible run ownership was accepted while survivor remained reserved")
	}
	if retried.ContainerID != first.ContainerID {
		t.Fatalf("retried container = %q, want surviving %q", retried.ContainerID, first.ContainerID)
	}
}

func TestRecoveredTerminalPutFailurePreservesAndRetries(t *testing.T) {
	e := newTestEnv(t, nil)
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.mu.Lock()
	container.spec.User = "1000:1000"
	container.mu.Unlock()
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	failing := &failingPutTerminalStore{Store: e.db, fail: true}
	s2.cfg.Store = failing
	row, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetTerminal: %v", err)
	}
	row.ContainerID = "stale-container"
	lock := s2.terminalLock(e.member.ID)
	lock.Lock()
	recoverErr := s2.recoverTerminalLocked(t.Context(), e.member, row)
	lock.Unlock()
	if recoverErr == nil {
		t.Fatal("recovery unexpectedly succeeded while PutTerminal was unavailable")
	}
	sup := s2.lookupTerminal(e.member.ID)
	if sup == nil || sup.containerID != runtime.ID(first.ContainerID) || sup.userReservation == nil {
		t.Fatalf("surviving terminal state = %+v", sup)
	}
	if !sup.persistPending {
		t.Fatal("PutTerminal failure did not retain persistence pending state")
	}
	failing.fail = false
	retried, err := s2.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal retry: %v", err)
	}
	if retried.ContainerID != first.ContainerID {
		t.Fatalf("retried container = %q, want surviving %q", retried.ContainerID, first.ContainerID)
	}
	stored, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetTerminal after retry: %v", err)
	}
	if stored.ContainerID != first.ContainerID {
		t.Fatalf("stored container = %q, want %q", stored.ContainerID, first.ContainerID)
	}
}

func TestRecoverTerminalWithoutRowAdoptsCreationKeySurvivor(t *testing.T) {
	e := newTestEnv(t, nil)
	failing := &failingPutTerminalStore{Store: e.db, fail: true}
	e.sched.cfg.Store = failing
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err == nil {
		t.Fatal("EnsureTerminal unexpectedly succeeded while PutTerminal was unavailable")
	}
	firstID, err := e.rt.FindByCreationKey(t.Context(), terminalCreationKey(e.member.ID))
	if err != nil {
		t.Fatalf("FindByCreationKey: %v", err)
	}
	container, err := e.rt.get(firstID)
	if err != nil {
		t.Fatal(err)
	}
	container.mu.Lock()
	container.spec.User = "1000:1000"
	container.mu.Unlock()
	if _, getErr := e.db.GetTerminal(t.Context(), e.member.ID); !errors.Is(getErr, store.ErrNotFound) {
		t.Fatalf("terminal row after failed initial persist = %v, want ErrNotFound", getErr)
	}
	e.rt.mu.Lock()
	createdBefore := e.rt.seq
	containersBefore := len(e.rt.containers)
	e.rt.mu.Unlock()
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close before recovery: %v", closeErr)
	}

	failing.fail = false
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Store = failing
	startScheduler(t, s2)
	waitFor(t, "row-less terminal recovery", func() bool {
		stored, storeErr := e.db.GetTerminal(t.Context(), e.member.ID)
		sup := s2.lookupTerminal(e.member.ID)
		return storeErr == nil && stored.ContainerID == string(firstID) &&
			sup != nil && sup.containerID == firstID &&
			len(s2.cfg.PTY.ActiveSessions(terminalPrefix(e.member.ID))) == 1
	})

	stored, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetTerminal after recovery: %v", err)
	}
	if stored.ContainerID != string(firstID) {
		t.Fatalf("recovered terminal row = %+v, want container %q", stored, firstID)
	}
	sup := s2.lookupTerminal(e.member.ID)
	if sup == nil || sup.userReservation == nil || sup.runUser != "1000:1000" {
		t.Fatalf("recovered terminal ownership = %+v, want one reserved 1000:1000 owner", sup)
	}
	e.rt.mu.Lock()
	createdAfter := e.rt.seq
	containersAfter := len(e.rt.containers)
	e.rt.mu.Unlock()
	if createdAfter != createdBefore || containersAfter != containersBefore {
		t.Fatalf("row-less recovery changed runtime population: creates %d -> %d, containers %d -> %d",
			createdBefore, createdAfter, containersBefore, containersAfter)
	}
	s2.mu.Lock()
	reservationCount := len(s2.credentialUsers)
	s2.mu.Unlock()
	if reservationCount != 1 {
		t.Fatalf("terminal credential reservations = %d, want 1", reservationCount)
	}
}

func TestRecoverTerminalWithoutRowAndSurvivorDoesNotCreate(t *testing.T) {
	e := newTestEnv(t, nil)
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	if err := s2.recoverTerminals(t.Context()); err != nil {
		t.Fatalf("recoverTerminals: %v", err)
	}
	e.rt.mu.Lock()
	created := e.rt.seq
	containers := len(e.rt.containers)
	e.rt.mu.Unlock()
	if created != 0 || containers != 0 {
		t.Fatalf("recovery created terminal without survivor: creates=%d containers=%d", created, containers)
	}
	if sup := s2.lookupTerminal(e.member.ID); sup != nil {
		t.Fatalf("terminal supervision without survivor = %+v", sup)
	}
}

func TestRecoveredTerminalProbeFailurePreservesAndRetries(t *testing.T) {

	e := newTestEnv(t, nil)
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.mu.Lock()
	container.spec.User = "1000:1000"
	container.mu.Unlock()
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	probeErr := errors.New("test: daemon probe unavailable")
	e.rt.setWaitError(probeErr)
	row, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetTerminal: %v", err)
	}
	lock := s2.terminalLock(e.member.ID)
	lock.Lock()
	recoverErr := s2.recoverTerminalLocked(t.Context(), e.member, row)
	lock.Unlock()
	if recoverErr == nil || !errors.Is(recoverErr, probeErr) {
		t.Fatalf("recovery error = %v, want probe error", recoverErr)
	}
	sup := s2.lookupTerminal(e.member.ID)
	if sup == nil || sup.containerID != runtime.ID(first.ContainerID) ||
		sup.userReservation == nil || !sup.metadataPending {
		t.Fatalf("surviving terminal ownership after probe error = %+v", sup)
	}

	e.rt.setWaitError(nil)
	retried, err := s2.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal retry: %v", err)
	}
	if retried.ContainerID != first.ContainerID {
		t.Fatalf("retried container = %q, want surviving %q", retried.ContainerID, first.ContainerID)
	}
	if got := len(s2.cfg.PTY.ActiveSessions(terminalPrefix(e.member.ID))); got != 1 {
		t.Fatalf("terminal PTY sessions after retry = %d, want 1", got)
	}
	s2.mu.Lock()
	reservationCount := len(s2.credentialUsers)
	s2.mu.Unlock()
	if reservationCount != 1 {
		t.Fatalf("terminal credential reservations after retry = %d, want 1", reservationCount)
	}
}

func TestRecoverTerminalWithoutRowFindFailureFailsClosed(t *testing.T) {
	e := newTestEnv(t, nil)
	failing := &failingPutTerminalStore{Store: e.db, fail: true}
	e.sched.cfg.Store = failing
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err == nil {
		t.Fatal("EnsureTerminal unexpectedly succeeded while PutTerminal was unavailable")
	}
	if _, err := e.rt.FindByCreationKey(t.Context(), terminalCreationKey(e.member.ID)); err != nil {
		t.Fatalf("FindByCreationKey before restart: %v", err)
	}
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close before recovery: %v", closeErr)
	}

	findErr := errors.New("test: daemon creation-key lookup unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &failingTerminalFindRuntime{Runtime: e.rt, findErr: findErr}
	if err := s2.recoverTerminals(t.Context()); err == nil || !errors.Is(err, findErr) {
		t.Fatalf("recoverTerminals error = %v, want creation-key lookup failure", err)
	}
	if sup := s2.lookupTerminal(e.member.ID); sup != nil {
		t.Fatalf("terminal supervision after failed creation-key lookup = %+v", sup)
	}
	e.rt.mu.Lock()
	created := e.rt.seq
	containers := len(e.rt.containers)
	e.rt.mu.Unlock()
	if created != 1 || containers != 1 {
		t.Fatalf("failed recovery changed runtime population: creates=%d containers=%d, want 1 and 1", created, containers)
	}
}

func TestTerminalContainerAddrReturnsLiveContainerIP(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.containerIP = "192.0.2.44"
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	addr, err := e.sched.TerminalContainerAddr(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("TerminalContainerAddr: %v", err)
	}
	if addr != "192.0.2.44" {
		t.Fatalf("TerminalContainerAddr = %q, want 192.0.2.44", addr)
	}
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if _, err := e.sched.TerminalContainerAddr(t.Context(), e.member.ID); err == nil || err.Error() != "environment terminal is not running" {
		t.Fatalf("TerminalContainerAddr after stop = %v", err)
	}
}

func TestEnsureTerminalTabRetriesShellFallback(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	if _, err := e.sched.EnsureTerminal(context.Background(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	calls := 0
	e.rt.execTTYHook = func(ctx context.Context, id runtime.ID, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
		calls++
		if calls == 1 {
			return nil, &runtime.ExecExitError{Code: 127}
		}
		return e.rt.attachForExec(ctx, id, argv, workDir, cols, rows)
	}
	if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, "logs", 120, 40); err != nil {
		t.Fatalf("EnsureTerminalTab: %v", err)
	}
	execCalls := e.rt.execTTYCalls()
	if len(execCalls) != 2 || execCalls[0].argv[0] != "/bin/bash" || execCalls[1].argv[0] != "/bin/sh" {
		t.Fatalf("exec calls = %+v", execCalls)
	}
	if execCalls[1].cols != 120 || execCalls[1].rows != 40 || execCalls[1].workDir != "/root" {
		t.Fatalf("fallback call = %+v", execCalls[1])
	}
}

// A member typing `exit` in the main shell must get a fresh environment on
// the next open: the exited container is destroyed, the row pruned, and
// EnsureTerminal creates a new container instead of adopting the corpse.
func TestEnsureTerminalRecreatesAfterMainShellExit(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	first, err := e.sched.EnsureTerminal(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.exitNow(0)
	var second *domain.Terminal
	waitFor(t, "terminal recreation after exit", func() bool {
		candidate, candidateErr := e.sched.EnsureTerminal(context.Background(), e.member.ID)
		if candidateErr != nil {
			return false
		}
		second = candidate
		return candidate.ContainerID != first.ContainerID
	})
	if second == nil || second.ContainerID == first.ContainerID {
		t.Fatalf("terminal after exit = %+v, want a fresh container", second)
	}
	stored, err := e.db.GetTerminal(context.Background(), e.member.ID)
	if err != nil || stored.ContainerID != second.ContainerID {
		t.Fatalf("terminal row after recreation = %+v, %v", stored, err)
	}
	if _, err := e.rt.get(runtime.ID(first.ContainerID)); err == nil {
		t.Fatal("exited terminal container was never destroyed")
	}
}

func profileForTerminalTest() harness.Profile {
	return harness.Profile{}
}

func TestEnsureTerminalTabLimit(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	for _, tab := range []string{"t1", "t2", "t3", "t4", "t5"} {
		if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, tab, 80, 24); err != nil {
			t.Fatalf("EnsureTerminalTab(%q): %v", tab, err)
		}
	}
	if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, "t6", 80, 24); !errors.Is(err, ErrTerminalTabLimit) {
		t.Fatalf("EnsureTerminalTab over limit error = %v, want %v", err, ErrTerminalTabLimit)
	}
}

func TestSuperviseTerminalRetriesTransportErrorUntilExit(t *testing.T) {
	e := newTestEnv(t, nil)
	rt := newScriptedWaitRuntime(e.rt)
	e.sched.cfg.Runtime = rt
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	c, err := e.rt.get(runtime.ID(terminal.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	if got := waitScriptedWaitCall(t, rt); got != runtime.ID(terminal.ContainerID) {
		t.Fatalf("first Wait container = %q, want %q", got, terminal.ContainerID)
	}
	sendScriptedWaitOutcome(t, rt, scriptedWaitOutcome{err: errors.New("test: daemon socket reset")})
	if got := waitScriptedWaitCall(t, rt); got != runtime.ID(terminal.ContainerID) {
		t.Fatalf("retry Wait container = %q, want %q", got, terminal.ContainerID)
	}
	sendScriptedWaitOutcome(t, rt, scriptedWaitOutcome{useUnderlying: true})
	if err := e.sched.EnsureTerminalTab(t.Context(), e.member.ID, "logs", 80, 24); err != nil {
		t.Fatalf("EnsureTerminalTab after transient Wait: %v", err)
	}
	c.exitNow(0)
	var replacement *domain.Terminal
	waitFor(t, "terminal cleanup after eventual exit", func() bool {
		next, nextErr := e.sched.EnsureTerminal(t.Context(), e.member.ID)
		if nextErr != nil {
			return false
		}
		replacement = next
		return next.ContainerID != terminal.ContainerID
	})
	if replacement == nil || replacement.ContainerID == terminal.ContainerID {
		t.Fatalf("terminal after eventual exit = %+v, want a fresh container", replacement)
	}
	if _, err := e.rt.get(runtime.ID(terminal.ContainerID)); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exited terminal container = %v, want ErrNotFound", err)
	}
}

func TestSuperviseTerminalMissingContainerPrunesState(t *testing.T) {
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if err := e.rt.Destroy(t.Context(), runtime.ID(terminal.ContainerID)); err != nil {
		t.Fatalf("Destroy terminal: %v", err)
	}
	var replacement *domain.Terminal
	waitFor(t, "missing terminal cleanup", func() bool {
		next, nextErr := e.sched.EnsureTerminal(t.Context(), e.member.ID)
		if nextErr != nil {
			return false
		}
		replacement = next
		return next.ContainerID != terminal.ContainerID
	})
	if replacement == nil || replacement.ContainerID == terminal.ContainerID {
		t.Fatalf("terminal after missing container = %+v, want a fresh container", replacement)
	}
}

func TestExitedTerminalCleanupRetainsStateForRetry(t *testing.T) {
	e := newTestEnv(t, nil)
	waitRuntime := newScriptedWaitRuntime(e.rt)
	runtimeWithFailure := &failingDestroyRuntime{
		scriptedWaitRuntime: waitRuntime,
		destroyErr:          errors.New("test: destroy unavailable"),
	}
	e.sched.cfg.Runtime = runtimeWithFailure
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if got := waitScriptedWaitCall(t, waitRuntime); got != runtime.ID(first.ContainerID) {
		t.Fatalf("Wait container = %q, want %q", got, first.ContainerID)
	}
	sendScriptedWaitOutcome(t, waitRuntime, scriptedWaitOutcome{useUnderlying: true})
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.exitNow(0)
	waitFor(t, "failed terminal cleanup retained", func() bool {
		_, ensureErr := e.sched.EnsureTerminal(t.Context(), e.member.ID)
		return ensureErr != nil
	})
	stored, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil || stored.ContainerID != first.ContainerID {
		t.Fatalf("terminal row after destroy failure = %+v, %v", stored, err)
	}
	assertCleanupPendingTerminalIsNotLive(t, e)

	if _, err = e.sched.EnsureTerminal(t.Context(), e.member.ID); err == nil {
		t.Fatal("EnsureTerminal succeeded while exited container destroy was unavailable")
	}

	runtimeWithFailure.destroyErr = nil
	failingStore := &failingPutTerminalStore{
		Store:     e.db,
		deleteErr: errors.New("test: terminal row delete unavailable"),
	}
	e.sched.cfg.Store = failingStore
	if _, err = e.sched.EnsureTerminal(t.Context(), e.member.ID); err == nil {
		t.Fatal("EnsureTerminal succeeded while terminal row delete was unavailable")
	}
	stored, err = e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil || stored.ContainerID != first.ContainerID {
		t.Fatalf("terminal row after delete failure = %+v, %v", stored, err)
	}
	assertCleanupPendingTerminalIsNotLive(t, e)

	failingStore.deleteErr = nil
	second, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal retry after cleanup failures: %v", err)
	}
	if second.ContainerID == first.ContainerID {
		t.Fatalf("EnsureTerminal reused exited container %q", first.ContainerID)
	}
	status, err := e.sched.TerminalStatus(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("TerminalStatus after replacement: %v", err)
	}
	if !status.Running || len(status.Tabs) != 1 || status.Tabs[0] != terminalTabMain {
		t.Fatalf("replacement terminal status = %+v, want running main tab", status)
	}
}
