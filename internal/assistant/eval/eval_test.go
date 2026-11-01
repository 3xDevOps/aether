//go:build assistanteval

package eval

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestAssistantEval drives the real Claude Code agent through every
// scenario. It spends model tokens, so it never runs in CI; see
// docs/testing.md.
func TestAssistantEval(t *testing.T) {
	scenarios, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := strconv.Atoi(envOr("AETHER_ASSISTANT_EVAL_RUNS", "3"))
	if err != nil || runs < 1 {
		t.Fatalf("AETHER_ASSISTANT_EVAL_RUNS must be a positive number")
	}
	parallel, err := strconv.Atoi(envOr("AETHER_ASSISTANT_EVAL_PARALLEL", "4"))
	if err != nil || parallel < 1 {
		t.Fatalf("AETHER_ASSISTANT_EVAL_PARALLEL must be a positive number")
	}
	model := os.Getenv("AETHER_ASSISTANT_EVAL_MODEL")
	label := strings.NewReplacer("/", "-", " ", "-").Replace(envOr("AETHER_ASSISTANT_EVAL_LABEL", "unlabeled"))
	out := envOr("AETHER_ASSISTANT_EVAL_OUT", filepath.Join(os.TempDir(), "assistant-eval"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	picked := scenarios
	if only := os.Getenv("AETHER_ASSISTANT_EVAL_ONLY"); only != "" {
		picked = nil
		for id := range strings.SplitSeq(only, ",") {
			i := slices.IndexFunc(scenarios, func(s Scenario) bool { return s.ID == strings.TrimSpace(id) })
			if i < 0 {
				t.Fatalf("AETHER_ASSISTANT_EVAL_ONLY: unknown scenario %q", id)
			}
			picked = append(picked, scenarios[i])
		}
	}
	agent := Claude(model)
	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
		slots   = make(chan struct{}, parallel)
	)
	write := func() string {
		md := Summary("real Claude Code (model "+modelName(results, model)+"), "+label+", "+strconv.Itoa(runs)+" sessions per scenario", picked, results)
		raw, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(filepath.Join(out, label+".json"), raw, 0o600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(out, label+".md"), []byte(md), 0o600); err != nil {
			t.Error(err)
		}
		return md
	}
	deadline, hasDeadline := t.Deadline()
	for _, s := range picked {
		for n := 1; n <= runs; n++ {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if hasDeadline {
					var stop context.CancelFunc
					ctx, stop = context.WithDeadline(ctx, deadline.Add(-time.Minute))
					defer stop()
				}
				r := RunOnce(ctx, agent, s, n)
				mu.Lock()
				results = append(results, r)
				write()
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	t.Logf("results written to %s\n%s", out, write())
}

func modelName(results []Result, requested string) string {
	for _, r := range results {
		if r.Model != "" {
			return r.Model
		}
	}
	return cmp.Or(requested, "default")
}
