package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func modelsFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "models", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type modelsClock struct{ nanos atomic.Int64 }

func (c *modelsClock) now() time.Time          { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *modelsClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

func newModelsEnv(t *testing.T) (*shareEnv, *modelsClock) {
	t.Helper()
	clock := &modelsClock{}
	clock.nanos.Store(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC).UnixNano())
	e := newShareEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{TUIArgs: []string{"fake-codex", "{task}"}}
		cfg.Now = clock.now
	})
	return e, clock
}

func modelIDs(r protocol.AgentModelsResult) []string {
	ids := []string{}
	for _, m := range r.Models {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestAgentModelsClaude(t *testing.T) {
	t.Parallel()
	e, clock := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) {
		return 0, modelsFixture(t, "claude-initialize.jsonl"), nil
	}
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != protocol.ModelSourceAgent || got.Error != "" || got.FetchedAt != clock.now().Format(time.RFC3339) {
		t.Fatalf("result = %+v", got)
	}
	if got.DefaultLabel != "Opus 5.5 · Best for everyday, complex tasks" {
		t.Fatalf("DefaultLabel = %q", got.DefaultLabel)
	}
	if ids := modelIDs(got); len(ids) != 12 || slices.Contains(ids, "default") || ids[0] != "opus" {
		t.Fatalf("models = %v", ids)
	}
	calls := e.rt.execRuns()
	if len(calls) != 1 || !strings.Contains(calls[0].argv[2], "--no-session-persistence") || calls[0].id != e.rt.byName(terminalContainerName(e.member.ID)).id {
		t.Fatalf("exec calls = %+v, want one claude probe in the member's terminal", calls)
	}
}

func TestAgentModelsCodex(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	out := modelsFixture(t, "codex-app-server.jsonl")
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 0, out, nil }
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 8 || got.DefaultLabel != "GPT-6.1-Sol" || got.Models[0].ID != "gpt-6.1-sol" {
		t.Fatalf("result = %+v", got)
	}

	out = strings.Replace(out, `"hidden":false`, `"hidden":true`, 1)
	got, err = e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 7 {
		t.Fatalf("with one hidden model, got %d models", len(got.Models))
	}
}

func TestAgentModelsCodexFallsBackToItsCache(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 127, "", nil }
	e.rt.execStderr = "sh: 1: codex: not found"
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != protocol.ModelSourceNone || len(got.Models) != 0 || !strings.Contains(got.Error, "codex exited 127: sh: 1: codex: not found") {
		t.Fatalf("without a cache file: %+v", got)
	}

	cache := filepath.Join(e.adaHome, ".codex", "models_cache.json")
	if err = os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cache, []byte(modelsFixture(t, "codex-models_cache.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", true)
	if err != nil {
		t.Fatal(err)
	}
	ids := modelIDs(got)
	if got.Source != protocol.ModelSourceCache || got.FetchedAt != "2026-10-13T23:02:00Z" || got.DefaultLabel != "GPT-6.1-Sol" ||
		ids[0] != "gpt-6.1-sol" || len(ids) != 8 || slices.Contains(ids, "codex-auto-review") || !strings.Contains(got.Error, "sh: 1: codex: not found") {
		t.Fatalf("with the cache file: %+v", got)
	}
}

func TestAgentModelsClaudeFailureFallsBack(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 127, "", nil }
	e.rt.execStderr = "sh: 1: claude: not found"
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != protocol.ModelSourceFallback || !slices.Equal(modelIDs(got), []string{"opus", "sonnet", "haiku"}) ||
		got.Error != "claude exited 127: sh: 1: claude: not found" {
		t.Fatalf("result = %+v", got)
	}
}

func TestAgentModelsCache(t *testing.T) {
	t.Parallel()
	e, clock := newModelsEnv(t)
	var fail atomic.Bool
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) {
		if fail.Load() {
			return 1, "", nil
		}
		return 0, modelsFixture(t, "claude-initialize.jsonl"), nil
	}
	models := func(refresh bool) protocol.AgentModelsResult {
		t.Helper()
		got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", refresh)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := models(false)
	models(false)
	if n := len(e.rt.execRuns()); n != 1 {
		t.Fatalf("a second call within the TTL ran %d probes, want 1", n)
	}
	clock.advance(agentModelsTTL + time.Second)
	models(false)
	models(true)
	if n := len(e.rt.execRuns()); n != 3 {
		t.Fatalf("after the TTL and a refresh, %d probes, want 3", n)
	}

	fail.Store(true)
	e.rt.execStderr = "boom"
	got := models(true)
	if got.Source != protocol.ModelSourceAgent || got.Error != "claude exited 1: boom" || len(got.Models) != len(first.Models) ||
		got.FetchedAt != clock.now().Format(time.RFC3339) {
		t.Fatalf("failed refresh = %+v, want the previous list with the error", got)
	}
	clock.advance(time.Minute)
	if got = models(false); got.FetchedAt != time.Date(2026, 10, 13, 13, 0, 1, 0, time.UTC).Format(time.RFC3339) || got.Error == "" {
		t.Fatalf("a cached failure = %+v, want the old FetchedAt and the error", got)
	}
	if n := len(e.rt.execRuns()); n != 4 {
		t.Fatalf("%d probes, want the failed one served from cache", n)
	}
}

func TestAgentModelsConcurrentCallsShareOneProbe(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	release := make(chan struct{})
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) {
		<-release
		return 0, modelsFixture(t, "claude-initialize.jsonl"), nil
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", false); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := len(e.rt.execRuns()); n != 1 {
		t.Fatalf("%d probes, want 1", n)
	}
}

func TestAgentModelsSharedAccount(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) {
		return 0, modelsFixture(t, "claude-initialize.jsonl"), nil
	}
	if _, err := e.sched.AgentModels(t.Context(), e.member.ID, e.owner.ID, "claude", false); err == nil || !strings.Contains(err.Error(), "is not logged in to claude") {
		t.Fatalf("without the owner's login: %v, want the launch refusal", err)
	}
	if n := len(e.rt.execRuns()); n != 0 {
		t.Fatalf("%d probes ran for a refused account", n)
	}

	writeHomeFiles(t, e.ownerHome, claudeLogin)
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.owner.ID, "claude", false)
	if err != nil || got.Source != protocol.ModelSourceFallback || !strings.Contains(got.Error, "the account owner's environment is not running") {
		t.Fatalf("with the owner's terminal stopped: %+v, %v, want the fallback and why", got, err)
	}
	if n := len(e.rt.execRuns()); n != 0 || e.rt.byName(terminalContainerName(e.owner.ID)) != nil {
		t.Fatalf("%d probes ran or the owner's terminal was started", n)
	}

	if _, err = e.sched.EnsureTerminal(t.Context(), e.owner.ID); err != nil {
		t.Fatal(err)
	}
	got, err = e.sched.AgentModels(t.Context(), e.member.ID, e.owner.ID, "claude", true)
	if err != nil || got.Source != protocol.ModelSourceAgent {
		t.Fatalf("with the owner's login: %+v, %v", got, err)
	}
	calls := e.rt.execRuns()
	if len(calls) != 1 || calls[0].id != e.rt.byName(terminalContainerName(e.owner.ID)).id {
		t.Fatalf("exec calls = %+v, want one in the owner's terminal", calls)
	}
}

func TestAgentModelsOtherAgentsHaveNone(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "omp", false)
	if err != nil || got.Source != protocol.ModelSourceNone || got.Models == nil || len(e.rt.execRuns()) != 0 {
		t.Fatalf("got %+v, %v, %d execs", got, err, len(e.rt.execRuns()))
	}
}

func TestAgentModelsFailuresExpireSooner(t *testing.T) {
	t.Parallel()
	e, clock := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 127, "", nil }
	for range 2 {
		if _, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", false); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.rt.execRuns()); n != 1 {
		t.Fatalf("%d probes within the failure TTL, want 1", n)
	}
	clock.advance(agentModelsFailTTL + time.Second)
	if _, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "claude", false); err != nil {
		t.Fatal(err)
	}
	if n := len(e.rt.execRuns()); n != 2 {
		t.Fatalf("%d probes after the failure TTL, want 2", n)
	}
}

func TestAgentModelsFallbackRereadsCodexCache(t *testing.T) {
	t.Parallel()
	e, _ := newModelsEnv(t)
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 127, "", nil }
	cache := filepath.Join(e.adaHome, ".codex", "models_cache.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := modelsFixture(t, "codex-models_cache.json")
	if err := os.WriteFile(cache, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", false); err != nil || got.Source != protocol.ModelSourceCache {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	got, err := e.sched.AgentModels(t.Context(), e.member.ID, e.member.ID, "codex", true)
	if err != nil || got.Source != protocol.ModelSourceNone || len(got.Models) != 0 {
		t.Fatalf("after the cache file went away: %+v, %v", got, err)
	}
}

func TestCodexModelsScriptAnswersWithoutStdinEOF(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not on PATH")
	}
	fake := filepath.Join(t.TempDir(), "codex")
	body := "#!/bin/sh\nread a; read b\nprintf '%s\\n' '{\"id\":2,\"result\":{\"data\":[{\"model\":\"m1\",\"displayName\":\"'\"$AETHER_TEST_KEY\"'\",\"description\":\"d\",\"hidden\":false,\"isDefault\":true}]}}'\ncat >/dev/null\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	out, err := exec.Command("/bin/sh", "-c", codexModelsScript, "sh", fake, `{"id":1}`, codexModelsList, "AETHER_TEST_KEY=secret").Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("script took %s", took)
	}
	list, err := parseCodexModels(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.models) != 1 || list.models[0].ID != "m1" || list.models[0].Name != "secret" {
		t.Fatalf("models = %+v", list.models)
	}
}
