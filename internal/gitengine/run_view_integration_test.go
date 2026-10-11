//go:build integration

package gitengine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The stat set stops reading untracked content at the bound a snapshot
// stages under, without dropping a path: the overlap index reads them all.
func TestDiffStatsListEveryPathPastTheReadBudget(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "large files", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	// One byte short of the budget in whole files, then one more whole file.
	sizes := make([]int64, MaxSnapshotInputBytes/countBytesCap+1)
	for i := range sizes {
		sizes[i] = countBytesCap
	}
	sizes[len(sizes)-2]--
	for i, size := range sizes {
		name := filepath.Join(checkout, fmt.Sprintf("large-%02d.bin", i))
		if err = os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err = os.Truncate(name, size); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(checkout, "small.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if !truncated || len(files) != len(sizes)+1 {
		t.Fatalf("stat set past the read budget = %d files, truncated %v; want every path and truncated", len(files), truncated)
	}
	for i, file := range files {
		// A file of NUL bytes is one unterminated line.
		want := 1
		if i == len(sizes)-1 {
			want = 0
		}
		if file.Additions != want {
			t.Errorf("%s counts %d lines, want %d", file.Path, file.Additions, want)
		}
	}
}
