package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func TestSweepScratchRemovesOnlyOrphans(t *testing.T) {
	t.Parallel()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Scheduler{
		cfg:       Config{Store: db, StateDir: t.TempDir(), RemoveAll: func(_ context.Context, p string) error { return os.RemoveAll(p) }},
		runs:      map[domain.RunID]*supervised{"run-live": {}},
		pending:   map[domain.RunID]*pendingRun{},
		terminals: map[domain.MemberID]*terminalSupervision{"ada": {}},
	}
	old := time.Now().Add(-2 * scratchOrphanAge)
	for _, name := range []string{"run-live", "run-gone", "run-young", "terminal-ada", "terminal-bob"} {
		dir, err := s.ensureScratch(name)
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(dir); info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
			t.Fatalf("%s mode = %v, want 1777", name, info.Mode())
		}
		if name != "run-young" {
			if err := os.Chtimes(dir, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.sweepScratch(context.Background())
	for name, want := range map[string]bool{"run-live": true, "run-gone": false, "run-young": true, "terminal-ada": true, "terminal-bob": false} {
		_, err := os.Stat(filepath.Join(s.scratchRoot(), name))
		if (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}
