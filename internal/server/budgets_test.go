package server

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidRunBudgetsDoNotInitializeServerState(t *testing.T) {
	for _, cfg := range []Config{
		{RunCPULimit: -1}, {RunCPULimit: math.NaN()}, {RunCPULimit: math.Inf(1)},
		{RunMemoryBytes: -1}, {RunPidsLimit: -1},
	} {
		t.Run("invalid budget", func(t *testing.T) {
			cfg.DataDir = filepath.Join(t.TempDir(), "uninitialized")
			srv, err := New(t.Context(), cfg)
			if err == nil {
				_ = srv.Close()
				t.Fatal("invalid budget accepted")
			}
			if !strings.Contains(err.Error(), "run-") {
				t.Fatalf("budget validation happened after initialization: %v", err)
			}
			if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
				t.Fatalf("invalid config initialized server state: %v", err)
			}
		})
	}
}
