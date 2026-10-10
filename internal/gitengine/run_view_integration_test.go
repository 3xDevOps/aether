//go:build integration

package gitengine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffStatsStopAtTheFileBound(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "many files", "")
	if err != nil {
		t.Fatal(err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	for i := range maxDiffStatFiles {
		name := filepath.Join(checkout, fmt.Sprintf("generated-%04d.txt", i))
		if err = os.WriteFile(name, []byte("line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, truncated, err := e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if truncated || len(files) != maxDiffStatFiles {
		t.Fatalf("stat set at the bound = %d files, truncated %v", len(files), truncated)
	}
	if err = os.WriteFile(filepath.Join(checkout, "one-more.txt"), []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, truncated, err = e.diffStats(ctx, "run1", checkout, base)
	if err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	if !truncated || len(files) != maxDiffStatFiles {
		t.Fatalf("stat set past the bound = %d files, truncated %v", len(files), truncated)
	}
}
