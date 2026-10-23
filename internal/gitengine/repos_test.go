package gitengine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestExtraKeysListRetainedHistoryOnlyOnRequest(t *testing.T) {
	e := newUnitEngine(t)
	for _, name := range []string{"run-a-r1", "run-a-r2" + snapshotStoreSuffix, "run-a-r2", "run-a-r3" + snapshotStoreSuffix, "run-ab-r1"} {
		if err := os.MkdirAll(filepath.Join(e.cfg.CheckoutsDir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for history, want := range map[bool][]domain.RunID{false: {"run-a-r1", "run-a-r2"}, true: {"run-a-r1", "run-a-r2", "run-a-r3"}} {
		got, err := e.extraKeys("run-a", history)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("extraKeys(history=%v) = %v, %v; want %v", history, got, err, want)
		}
	}
}

func TestExtraKeysReportAnUnreadableCheckoutsDir(t *testing.T) {
	e := newUnitEngine(t)
	if err := os.WriteFile(e.cfg.CheckoutsDir+"-file", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.cfg.CheckoutsDir += "-file"
	if _, err := e.extraKeys("run-a", false); err == nil {
		t.Fatal("extraKeys on an unreadable directory returned no error")
	}
}
