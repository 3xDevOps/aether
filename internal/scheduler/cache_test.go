package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func writeCacheFixture(t *testing.T, e *testEnv, member domain.MemberID, pool string, size int64) string {
	t.Helper()
	unlock := e.sched.cfg.Homes.LockCaches(member)
	defer unlock()
	path, err := e.sched.cfg.Homes.CachePath(member, pool)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(path, "packages"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireCacheExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("protected cache disappeared: %v", err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eligible cache still exists: %v", err)
	}
}

func TestCachePressureSeparatesRunAndPermanentTerminalPools(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sched.cfg.Runtime = &cacheImageRuntime{fakeRuntime: e.rt}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	runs := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
	terminal := writeCacheFixture(t, e, e.member.ID, "terminal", 4096)
	home, err := e.sched.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, ".npm", "_cacache")
	if err = os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, runs, false)
	requireCacheExists(t, terminal, true)
	requireCacheExists(t, legacy, true)
	view, err := e.sched.CacheRetentions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view[cacheKey(e.member.ID, "runs")].Protected || !view[cacheKey(e.member.ID, "terminal")].Protected {
		t.Fatalf("pool ownership conflated: %+v", view)
	}
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, terminal, false)
	requireCacheExists(t, legacy, false)
}

func TestCachePressurePreservesActiveAndRetainedRuns(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "cache owner")
	path := writeCacheFixture(t, e, run.HomeMember(), "runs", 4096)
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, true)
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, true)
}

func TestCacheUnknownSidecarProtectsAllPools(t *testing.T) {
	e := newTestEnv(t, nil)
	path := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
	unknown := filepath.Join(e.sched.cfg.StateDir, "unknown.json")
	if err := os.WriteFile(unknown, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, true)
	view, err := e.sched.CacheRetentions(t.Context())
	if err == nil || !view[cacheKey(e.member.ID, "runs")].Protected || view[cacheKey(e.member.ID, "runs")].Error == "" {
		t.Fatalf("unknown owner treated as inactive: %+v, %v", view, err)
	}
}

func TestCacheTerminalCreateGapDoesNotBlockSweepOrLoseData(t *testing.T) {
	e := newTestEnv(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	e.rt.createHook = func() { close(entered); <-release }
	result := make(chan error, 1)
	go func() { _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); result <- err }()
	<-entered
	path := filepath.Join(e.sched.cfg.Homes.CacheRoot(), string(e.member.ID), "terminal", "data")
	if err := os.WriteFile(filepath.Join(path, "in-flight"), []byte("creating"), 0o600); err != nil {
		t.Fatal(err)
	}
	// This must return without waiting for the deliberately blocked Create.
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, true)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestCachePoolBudgetAndAgeReclaimWithoutPressure(t *testing.T) {
	for _, age := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool bytes", true: "inactive age"}[age], func(t *testing.T) {
			e := newTestEnv(t, nil)
			size := int64(cachePoolTarget + 1)
			if age {
				size = 4096
			}
			path := writeCacheFixture(t, e, e.member.ID, "runs", size)
			if age {
				e.sched.cfg.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
			}
			e.sched.sweepCaches(t.Context(), false)
			requireCacheExists(t, path, false)
		})
	}
}

func TestCacheDiskAdmissionCleansOnceThenRemeasures(t *testing.T) {
	e := newTestEnv(t, nil)
	path := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
	calls := 0
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		calls++
		free := uint64(1)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			free = 80 << 30
		}
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free}, nil
	}
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if calls != 2 {
		t.Fatalf("capacity probes = %d, want before and after eligible cleanup", calls)
	}
	requireCacheExists(t, path, false)
}

func TestCacheDefaultsRespectExplicitToolPathsAndHome(t *testing.T) {
	e := newTestEnv(t, nil)
	e.ws.Environment.Variables = map[string]string{"npm_config_cache": "/custom/npm", "PIP_CACHE_DIR": "", "XDG_CACHE_HOME": "/custom/xdg", "PATH": "/custom/bin"}
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"npm_config_cache": "/custom/npm", "PIP_CACHE_DIR": "", "XDG_CACHE_HOME": "/custom/xdg", "HOME": "/root", "PATH": "/root/.local/bin:/custom/bin", "UV_CACHE_DIR": "/aether-cache/uv"} {
		if plan.Env[key] != want {
			t.Fatalf("%s = %q, want %q", key, plan.Env[key], want)
		}
	}
}

func TestDeletedMemberImageFailureRetainsDiscoverableRetry(t *testing.T) {
	e := newTestEnv(t, nil)
	path := writeCacheFixture(t, e, e.member.ID, "terminal", 4096)
	tag := memberImageRepo(e.member.ID) + ":obsolete"
	e.rt.images = map[string]string{tag: "image"}
	e.rt.holdImage(tag, true)
	if err := e.db.DeleteMember(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, false)
	info, err := e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if err != nil || info.CleanupError == "" || !e.rt.hasImage(tag) {
		t.Fatalf("failed image retry lost: %+v, %v", info, err)
	}
	e.rt.holdImage(tag, false)
	e.sched.sweepCaches(t.Context(), false)
	if e.rt.hasImage(tag) {
		t.Fatal("normal maintenance did not retry removed-member image")
	}
	info, err = e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if err != nil || info.CleanupError != "" {
		t.Fatalf("successful retry status: %+v, %v", info, err)
	}
}

func TestPeriodicImagesProtectEveryCurrentReference(t *testing.T) {
	e := newTestEnv(t, nil)
	repo := memberImageRepo(e.member.ID)
	base, browser, saved, terminal, obsolete := repo+":base", repo+":browser", repo+":saved", repo+":terminal", repo+":old"
	e.rt.images = map[string]string{base: "base", browser: "browser", saved: "saved", terminal: "terminal", obsolete: "old", "unrelated:latest": "unrelated"}
	e.sched.cfg.StandardImage, e.sched.cfg.BrowserImage = base, browser
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, saved); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), false)
	for _, tag := range []string{base, browser, saved, terminal, "unrelated:latest"} {
		if !e.rt.hasImage(tag) {
			t.Fatalf("current or unrelated image removed: %s", tag)
		}
	}
	if e.rt.hasImage(obsolete) {
		t.Fatal("obsolete exact member tag not removed")
	}
}

func TestUpdaterCreationMarkerPreservesUnknownRuntimeAndRetriesExited(t *testing.T) {
	e := newTestEnv(t, nil)
	path := writeCacheFixture(t, e, e.member.ID, "terminal", 4096)
	key := "harness-update-" + string(e.member.ID) + "-fake"
	if err := e.sched.cfg.Homes.AddCacheOwner(e.member.ID, "terminal", key); err != nil {
		t.Fatal(err)
	}
	cid, err := e.rt.Create(t.Context(), runtime.Spec{Image: "fake", CreationKey: key, Command: []string{"sh", "-c", "npm install -g @anthropic-ai/claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rt.Start(t.Context(), cid); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, true)
	if err := e.rt.Stop(context.Background(), cid, 0); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, path, false)
	if _, err := e.rt.FindByCreationKey(t.Context(), key); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exited updater retained: %v", err)
	}
}

type cacheImageRuntime struct {
	*fakeRuntime
	env  map[string]string
	err  error
	user string
}

func (r *cacheImageRuntime) ImageCacheEnvironment(context.Context, string) (map[string]string, error) {
	return r.env, r.err
}

func (r *cacheImageRuntime) ImageUser(context.Context, string) (string, error) {
	return r.user, nil
}

type cacheConfigReadFailure struct{ store.Store }

func (s cacheConfigReadFailure) ListWorkspaces(context.Context) ([]*domain.Workspace, error) {
	return nil, errors.New("configuration unavailable")
}

func TestLegacyCacheExplicitChoicesSurviveInactiveSweep(t *testing.T) {
	for _, source := range []string{"image", "idle workspace edit", "profile plan"} {
		t.Run(source, func(t *testing.T) {
			e := newTestEnv(t, nil)
			rt := &cacheImageRuntime{fakeRuntime: e.rt, user: "1000:1000"}
			e.sched.cfg.Runtime = rt
			home, err := e.sched.cfg.Homes.Path(e.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			explicit := "/home/aether/.cache/uv"
			switch source {
			case "image":
				rt.env = map[string]string{"UV_CACHE_DIR": explicit}
			case "idle workspace edit":
				// No plan has ever been built: GC must consult current storage.
				e.ws.Environment.Variables["UV_CACHE_DIR"] = explicit
				if err = e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
					t.Fatal(err)
				}
			case "profile plan":
				_, err = e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, e.member,
					harness.Profile{Env: map[string]string{"UV_CACHE_DIR": explicit}}, EnvironmentPurposeRun)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, rel := range []string{".cache/uv", ".cache/pip", ".local/bin", ".config", ".ssh"} {
				dir := filepath.Join(home, rel)
				if err = os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "keep"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			managed := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
			e.sched.cfg.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
			e.sched.sweepCaches(t.Context(), false)
			requireCacheExists(t, managed, false)
			requireCacheExists(t, filepath.Join(home, ".cache/pip"), false)
			for _, rel := range []string{".cache/uv/keep", ".local/bin/keep", ".config/keep", ".ssh/keep"} {
				requireCacheExists(t, filepath.Join(home, rel), true)
			}
			// A fresh manager has only the on-disk protection record.
			restarted, err := memberhome.New(e.sched.cfg.Homes.Root(), e.sched.cfg.Homes.CacheRoot(), nil)
			if err != nil {
				t.Fatal(err)
			}
			unlock := restarted.LockCaches(e.member.ID)
			err = restarted.RemoveLegacyCaches(t.Context(), e.member.ID)
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			requireCacheExists(t, filepath.Join(home, ".cache/uv/keep"), true)
		})
	}
}

func TestLegacyCacheConfigurationFailurePreservesHomeOnly(t *testing.T) {
	for _, failure := range []string{"store", "image", "unsupported inspection"} {
		t.Run(failure, func(t *testing.T) {
			e := newTestEnv(t, nil)
			rt := &cacheImageRuntime{fakeRuntime: e.rt}
			e.sched.cfg.Runtime = rt
			switch failure {
			case "store":
				e.sched.cfg.Store = cacheConfigReadFailure{e.db}
			case "image":
				rt.err = errors.New("image unavailable")
			case "unsupported inspection":
				e.sched.cfg.Runtime = e.rt
			}
			home, err := e.sched.cfg.Homes.Path(e.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(home, ".cache/uv")
			if err = os.MkdirAll(legacy, 0o700); err != nil {
				t.Fatal(err)
			}
			managed := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
			e.sched.sweepCaches(t.Context(), true)
			requireCacheExists(t, managed, false)
			requireCacheExists(t, legacy, true)
		})
	}
}

func TestCacheDefaultsPreserveImageSettingsWithExplicitPlanPrecedence(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sched.cfg.Runtime = &cacheImageRuntime{fakeRuntime: e.rt, env: map[string]string{
		"npm_config_cache": "/image/npm", "PIP_CACHE_DIR": "", "GOCACHE": "/image/go",
		"SECRET_TOKEN": "never-copy", "HOME": "/image/home",
	}}
	e.ws.Environment.Variables["GOCACHE"] = "/workspace/go"
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, e.member,
		harness.Profile{Env: map[string]string{"UV_CACHE_DIR": "/profile/uv"}}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"npm_config_cache": "/image/npm", "PIP_CACHE_DIR": "", "GOCACHE": "/workspace/go",
		"UV_CACHE_DIR": "/profile/uv", "GOMODCACHE": "/aether-cache/go-mod", "HOME": "/root",
	} {
		if got := plan.Env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if _, copied := plan.Env["SECRET_TOKEN"]; copied {
		t.Fatal("unrelated image environment leaked into plan")
	}
}

func TestCacheReadDoesNotCreateMissingPools(t *testing.T) {
	e := newTestEnv(t, nil)
	view, err := e.sched.CacheRetentions(t.Context())
	if err != nil || len(view) != 0 {
		t.Fatalf("read of unused cache = %+v, %v", view, err)
	}
	if _, err := os.Stat(e.sched.cfg.Homes.CacheRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retention read created cache root: %v", err)
	}
}

func TestCacheTotalTargetReclaimsEligiblePoolsOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	paths := make([]string, 0, 5)
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		member := &domain.Member{DisplayName: name, PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
		if err := e.db.CreateMember(t.Context(), member); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, writeCacheFixture(t, e, member.ID, "runs", 7<<29))
	}
	e.sched.sweepCaches(t.Context(), false)
	remaining := 0
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			remaining++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if remaining != 4 {
		t.Fatalf("17.5GiB inactive pools retained = %d, want four 3.5GiB pools", remaining)
	}
}

func TestCacheDiskPressureNeverEvictsActiveWorkOrLoops(t *testing.T) {
	e := newTestEnv(t, nil)
	run, container := e.launchFake(t, "keep working under disk pressure")
	path := writeCacheFixture(t, e, run.HomeMember(), "runs", 4096)
	calls := 0
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		calls++
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: 1}, nil
	}
	if _, err := e.sched.reserveCapacity(t.Context()); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("pressure with only protected data = %v, want refusal", err)
	}
	if calls != 2 {
		t.Fatalf("pressure retried %d probes, want exactly two", calls)
	}
	requireCacheExists(t, path, true)
	container.mu.Lock()
	state := container.state
	container.mu.Unlock()
	if state != "running" {
		t.Fatalf("cache pressure changed active process to %s", state)
	}
}

func TestMemberDeletionExcludesEnsureAcrossStoppedTerminalCallback(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	stopped := make(chan error, 1)
	go func() {
		stopped <- e.sched.WithStoppedTerminal(t.Context(), e.member.ID, func() error {
			close(entered)
			if lock := e.sched.terminalLock(e.member.ID); lock.TryLock() {
				lock.Unlock()
				return errors.New("terminal lifecycle lock released before deletion callback")
			}
			<-release
			unlock := e.sched.cfg.Homes.LockCaches(e.member.ID)
			defer unlock()
			if err := e.sched.cfg.Homes.SetCacheCleanupError(e.member.ID, "terminal", ""); err != nil {
				return err
			}
			if err := e.db.DeleteMember(t.Context(), e.member.ID); err != nil {
				return err
			}
			return e.sched.cfg.Homes.Remove(t.Context(), e.member.ID)
		})
	}()
	<-entered
	ensuring, ensured := make(chan struct{}), make(chan error, 1)
	go func() {
		close(ensuring)
		_, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
		ensured <- err
	}()
	<-ensuring
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-ensured; err == nil {
		t.Fatal("terminal provisioned after the member deletion boundary")
	}
	if _, err := e.rt.FindByCreationKey(t.Context(), terminalCreationKey(e.member.ID)); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("removed member retained a newly created terminal: %v", err)
	}
	info, err := e.sched.cfg.Homes.ReadCache(e.member.ID, "terminal")
	if err != nil || info.MetadataError != "" {
		t.Fatalf("deleted member retry key lost: %+v, %v", info, err)
	}
}

func TestStoppedTerminalFailureDoesNotRunDeletionCallback(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.rt.stopErr = errors.New("runtime stop unavailable")
	called := false
	err := e.sched.WithStoppedTerminal(t.Context(), e.member.ID, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("failed terminal stop: err=%v callback=%v", err, called)
	}
	if _, err := e.db.GetMember(t.Context(), e.member.ID); err != nil {
		t.Fatalf("member removed despite failed stop: %v", err)
	}
}

func TestLegacyCacheExplicitPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		uv, pip     bool
		link        bool
	}{
		{name: "exact", value: "/root/.cache/uv", uv: true},
		{name: "ancestor", value: "/root/.cache", uv: true, pip: true},
		{name: "descendant", value: "/root/.cache/uv/custom", uv: true},
		{name: "sibling prefix", value: "/root/.cache/uv-other"},
		{name: "outside home prefix", value: "/root-other/.cache/uv"},
		{name: "symlink alias", value: "/root/cache-alias", uv: true, pip: true, link: true},
		{name: "relative unknown", value: ".cache/uv", uv: true, pip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.sched.cfg.Runtime = &cacheImageRuntime{fakeRuntime: e.rt, env: map[string]string{"UV_CACHE_DIR": tc.value}}
			home, err := e.sched.cfg.Homes.Path(e.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{".cache/uv", ".cache/pip"} {
				if err = os.MkdirAll(filepath.Join(home, rel), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.link {
				if err = os.Symlink(".cache/uv", filepath.Join(home, "cache-alias")); err != nil {
					t.Fatal(err)
				}
			}
			e.sched.sweepCaches(t.Context(), true)
			requireCacheExists(t, filepath.Join(home, ".cache/uv"), tc.uv)
			requireCacheExists(t, filepath.Join(home, ".cache/pip"), tc.pip)
		})
	}
}
