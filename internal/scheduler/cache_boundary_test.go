package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestCachePartialRemovalRetriesBelowTargetsAfterRestart(t *testing.T) {
	for _, pool := range []string{"runs", "terminal"} {
		t.Run(pool, func(t *testing.T) {
			e := newTestEnv(t, nil)
			homes := e.sched.cfg.Homes
			failed := false
			remover := func(_ context.Context, path string) error {
				if !failed {
					failed = true
					if err := os.Truncate(filepath.Join(path, "packages"), 3<<30); err != nil {
						return err
					}
					return errors.New("partial deletion")
				}
				return os.RemoveAll(path)
			}
			manager, err := memberhome.New(homes.Root(), homes.CacheRoot(), remover)
			if err != nil {
				t.Fatal(err)
			}
			e.sched.cfg.Homes = manager
			path := writeCacheFixture(t, e, e.member.ID, pool, 5<<30)
			e.sched.sweepCaches(t.Context(), false)
			stat, err := os.Stat(filepath.Join(path, "packages"))
			if err != nil || stat.Size() != 3<<30 {
				t.Fatalf("partial deletion: %v, %v", stat, err)
			}
			// A successful unrelated operation cannot clear the data retry marker.
			e.sched.sweepMemberImages(t.Context(), e.member.ID, "")
			manager, err = memberhome.New(homes.Root(), homes.CacheRoot(), remover)
			if err != nil {
				t.Fatal(err)
			}
			e.sched.cfg.Homes = manager
			info, err := manager.ReadCache(e.member.ID, pool)
			if err != nil || !info.DataCleanupPending {
				t.Fatalf("lost durable data retry: %+v, %v", info, err)
			}
			e.sched.sweepCaches(t.Context(), false)
			requireCacheExists(t, path, false)
			info, err = manager.ReadCache(e.member.ID, pool)
			if err != nil || info.DataCleanupPending {
				t.Fatalf("retry not completed: %+v, %v", info, err)
			}
		})
	}
}

func TestEnsureTerminalReclaimsOwnRecentInactiveCache(t *testing.T) {
	e := newTestEnv(t, nil)
	path := writeCacheFixture(t, e, e.member.ID, "runs", 3<<30)
	probes := 0
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		probes++
		free := uint64(1)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			free = 80 << 30
		}
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free}, nil
	}
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.ContainerID == "" || probes != 2 {
		t.Fatalf("terminal=%+v probes=%d", terminal, probes)
	}
	requireCacheExists(t, path, false)
	// Reuse bypasses admission even when the disk becomes full again.
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) { t.Fatal("reuse attempted admission"); return disk.Usage{}, nil }
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTerminalDoesNotPinExitedUpdater(t *testing.T) {
	e := newTestEnv(t, nil)
	terminal, terminalErr := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if terminalErr != nil {
		t.Fatal(terminalErr)
	}
	path := writeCacheFixture(t, e, e.member.ID, "terminal", 5<<30)
	key := "harness-update-" + string(e.member.ID) + "-fake"
	if terminalErr = e.sched.cfg.Homes.AddCacheOwner(e.member.ID, "terminal", key); terminalErr != nil {
		t.Fatal(terminalErr)
	}
	cid, createErr := e.rt.Create(t.Context(), runtime.Spec{Image: "fake", CreationKey: key, Command: []string{"sh", "-c", "npm install -g @anthropic-ai/claude-code"}})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := e.rt.Start(t.Context(), cid); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), false)
	if got, findErr := e.rt.FindByCreationKey(t.Context(), key); findErr != nil || got != cid {
		t.Fatalf("live updater lost: %s, %v", got, findErr)
	}
	if err := e.rt.Stop(t.Context(), cid, 0); err != nil {
		t.Fatal(err)
	}
	failure := &destroyFailureRuntime{Runtime: e.rt, destroyErr: errors.New("destroy failed")}
	e.sched.cfg.Runtime = failure
	e.sched.sweepCaches(t.Context(), false)
	info, cacheErr := e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if cacheErr != nil || len(info.Owners) != 1 {
		t.Fatalf("failed updater retry key lost: %+v, %v", info, cacheErr)
	}
	failure.destroyErr = nil
	e.sched.sweepCaches(t.Context(), false)
	if _, err := e.rt.FindByCreationKey(t.Context(), key); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exited updater retained: %v", err)
	}
	info, cacheErr = e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if cacheErr != nil || len(info.Owners) != 0 {
		t.Fatalf("completed updater retained key: %+v, %v", info, cacheErr)
	}
	requireCacheExists(t, path, true)
	if got, err := e.rt.FindByCreationKey(t.Context(), terminalCreationKey(e.member.ID)); err != nil || string(got) != terminal.ContainerID {
		t.Fatalf("terminal changed: %s, %v", got, err)
	}
}

func TestOpenTerminalPreservesUpdaterPrecreateAndUnknownKeys(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	path := writeCacheFixture(t, e, e.member.ID, "terminal", 5<<30)
	key := "harness-update-" + string(e.member.ID) + "-fake"
	home, homeErr := e.sched.cfg.Homes.Path(e.member.ID)
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	e.sched.mu.Lock()
	e.sched.harnessUpdates = map[harnessUpdateKey]*harnessUpdateState{
		{home: home, harness: "fake"}: {running: &harnessUpdateRun{done: make(chan struct{})}},
	}
	e.sched.mu.Unlock()
	for _, owner := range []string{key, "unknown-owner"} {
		if err := e.sched.cfg.Homes.AddCacheOwner(e.member.ID, "terminal", owner); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	e.rt.createHook = func() { close(entered); <-release }
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	created := make(chan error, 1)
	go func() {
		_, err := e.rt.Create(t.Context(), runtime.Spec{Image: "fake", CreationKey: key, Command: []string{"sh", "-c", "npm install -g @anthropic-ai/claude-code"}})
		created <- err
	}()
	<-entered
	e.sched.sweepCaches(t.Context(), false)
	info, cacheErr := e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if cacheErr != nil || len(info.Owners) != 2 {
		t.Fatalf("unconfirmed keys removed: %+v, %v", info, cacheErr)
	}
	requireCacheExists(t, path, true)
	close(release)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
}
