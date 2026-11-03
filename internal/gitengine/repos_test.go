package gitengine

import (
	"errors"
	"os"
	"os/exec"
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

func TestCommitAllReachesExtrasWhenMainCheckoutIsGone(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws1")
	ctx := t.Context()
	main, _, err := e.CreateRunCheckout(ctx, "ws1", "run-multi", "main", "multi", "")
	if err != nil {
		t.Fatal(err)
	}
	extra, _, err := e.CreateRunCheckout(ctx, "ws1", domain.RepoKey("run-multi", 1), "main", "multi", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "work.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CommitAll(ctx, "run-multi", "wip", domain.GitIdentity{}, nil); !errors.Is(err, ErrCheckoutNotFound) {
		t.Fatalf("CommitAll = %v, want ErrCheckoutNotFound", err)
	}
	status, err := exec.Command("git", "-C", extra, "status", "--porcelain").Output()
	if err != nil || len(status) != 0 {
		t.Fatalf("extra after CommitAll: status %q, %v; want committed", status, err)
	}
	if _, err := e.PublishRunBranch(ctx, "run-multi"); !errors.Is(err, ErrCheckoutNotFound) {
		t.Fatalf("PublishRunBranch = %v, want ErrCheckoutNotFound", err)
	}
}
