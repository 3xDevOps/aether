package disk

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// gatedCache holds every walk in enrich until the test releases it.
type gatedCache struct {
	*Cache
	walks   atomic.Int32
	release chan struct{}
}

func newGatedCache(t *testing.T, dir string) *gatedCache {
	t.Helper()
	g := &gatedCache{release: make(chan struct{})}
	g.Cache = NewCache(dir, time.Minute, func(*Usage) {
		g.walks.Add(1)
		<-g.release
	})
	t.Cleanup(func() { close(g.release) })
	return g
}

// reading fails the test instead of hanging when a reading waits on a walk
// past the first-walk bound. Inside a synctest bubble the wait costs no real
// time.
func (g *gatedCache) reading(t *testing.T) (Usage, error) {
	t.Helper()
	type result struct {
		u   Usage
		err error
	}
	done := make(chan result, 1)
	go func() {
		u, err := g.Usage(t.Context())
		done <- result{u, err}
	}()
	select {
	case r := <-done:
		return r.u, r.err
	case <-time.After(time.Minute):
		t.Fatal("a reading waited on a walk")
		return Usage{}, nil
	}
}

func (g *gatedCache) read(t *testing.T) Usage {
	t.Helper()
	u, err := g.reading(t)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.TotalBytes == 0 {
		t.Fatal("reading has no filesystem total, want fresh headroom")
	}
	return u
}

func TestCacheAnswersFromTheLastWalkWhileTheNextRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := seedDataDir(t)
		g := newGatedCache(t, dir)
		go func() { g.release <- struct{}{} }()
		if u := g.read(t); u.WorktreeBytes != seededWorktreeBytes || u.RepoBytes != seededRepoBytes {
			t.Fatalf("first reading: worktree %d, repo %d, want the seeded tree", u.WorktreeBytes, u.RepoBytes)
		}

		if err := os.WriteFile(filepath.Join(dir, checkoutsDir, "run_1", "big"), make([]byte, 1000), 0o644); err != nil {
			t.Fatalf("grow the checkout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, reposDir, "ws_1.git", "objects", "pack", "pack-2.pack"), make([]byte, 250), 0o644); err != nil {
			t.Fatalf("grow the bare repo: %v", err)
		}
		if u := g.read(t); u.WorktreeBytes != seededWorktreeBytes || g.walks.Load() != 1 {
			t.Fatalf("reading within the TTL: worktree %d after %d walks, want the cached walk",
				u.WorktreeBytes, g.walks.Load())
		}

		time.Sleep(time.Minute)
		if u := g.read(t); u.WorktreeBytes != seededWorktreeBytes {
			t.Fatalf("expired reading: worktree %d, want the last walk while the next runs", u.WorktreeBytes)
		}
		synctest.Wait()
		if u := g.read(t); u.WorktreeBytes != seededWorktreeBytes || g.walks.Load() != 2 {
			t.Fatalf("reading during a walk: worktree %d after %d walks, want the last walk and one walk running",
				u.WorktreeBytes, g.walks.Load())
		}

		// A walk that outlasts the TTL is still fresh when it finishes.
		time.Sleep(2 * time.Minute)
		g.release <- struct{}{}
		synctest.Wait()
		u := g.read(t)
		synctest.Wait()
		if u.WorktreeBytes != seededWorktreeBytes+1000 || u.RepoBytes != seededRepoBytes+250 || g.walks.Load() != 2 {
			t.Fatalf("reading after the walk: worktree %d, repo %d after %d walks, want its numbers and no third walk",
				u.WorktreeBytes, u.RepoBytes, g.walks.Load())
		}
	})
}

func TestCacheBoundsTheWaitForTheFirstWalk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGatedCache(t, seedDataDir(t))
		start := time.Now()
		if _, err := g.reading(t); !errors.Is(err, ErrMeasuring) || time.Since(start) != firstWalkWait {
			t.Fatalf("reading before the first walk finished: err = %v after %v, want ErrMeasuring after %v",
				err, time.Since(start), firstWalkWait)
		}

		go func() { g.release <- struct{}{} }()
		if u := g.read(t); u.WorktreeBytes != seededWorktreeBytes || g.walks.Load() != 1 {
			t.Fatalf("reading after the first walk: worktree %d after %d walks, want the seeded tree from one walk",
				u.WorktreeBytes, g.walks.Load())
		}
	})
}
