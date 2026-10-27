//go:build assistanteval

package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
// docs/assistant.md.
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
	model := envOr("AETHER_ASSISTANT_EVAL_MODEL", "sonnet")
	label := envOr("AETHER_ASSISTANT_EVAL_LABEL", "unlabeled")
	only := strings.Split(os.Getenv("AETHER_ASSISTANT_EVAL_ONLY"), ",")
	out := envOr("AETHER_ASSISTANT_EVAL_OUT", filepath.Join(os.TempDir(), "assistant-eval"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	var picked []Scenario
	for _, s := range scenarios {
		if os.Getenv("AETHER_ASSISTANT_EVAL_ONLY") == "" || contains(only, s.ID) {
			picked = append(picked, s)
		}
	}
	agent := Claude(model)
	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
		slots   = make(chan struct{}, parallel)
	)
	for _, s := range picked {
		for n := 1; n <= runs; n++ {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				r := RunOnce(ctx, agent, s, n)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	md := Summary("real Claude Code ("+model+"), "+label+", "+strconv.Itoa(runs)+" runs per scenario", picked, results)
	raw, _ := json.MarshalIndent(results, "", " ")
	if err := os.WriteFile(filepath.Join(out, label+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, label+".md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("results written to %s\n%s", out, md)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
