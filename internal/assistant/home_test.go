package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// fakeHost records the assistant containers removed and runs no adapter.
type fakeHost struct{ removed []domain.MemberID }

func (*fakeHost) AssistantAdapter(string) (scheduler.AssistantLaunch, error) {
	return scheduler.AssistantLaunch{}, errors.New("fakeHost: no adapter")
}

func (*fakeHost) StartAssistantExec(context.Context, domain.MemberID, string, runtime.ExecSpec, func()) (runtime.ManagedExec, error) {
	return nil, errors.New("fakeHost: no adapter")
}

func (*fakeHost) StopAssistantExec(context.Context, runtime.ExecIdentity) error { return nil }

func (h *fakeHost) RemoveAssistantContainer(_ context.Context, member domain.MemberID) error {
	h.removed = append(h.removed, member)
	return nil
}

// TestWipeHomeKeepsOnlyTheInstall discards everything the agent kept in the
// assistant home but the installed tools, without following a symlink out.
func TestWipeHomeKeepsOnlyTheInstall(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		".local/bin/claude-agent-acp",
		".local/share/claude/versions/1",
		".claude/projects/-assistant/session.jsonl",
		".claude/todos/a.json",
		".claude/file-history/a",
		".claude/shell-snapshots/a.sh",
		".claude/debug/a.txt",
		".claude/history.jsonl",
		".claude/.credentials.json",
		".claude.json",
		".codex/sessions/2026/a.jsonl",
		".codex/history.jsonl",
	} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if err := wipeHome(home); err != nil {
		t.Fatal(err)
	}
	var left []string
	err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(home, p)
			left = append(left, rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".local/bin/claude-agent-acp", ".local/share/claude/versions/1"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("wipe followed a symlink out of the home: %v", err)
	}
	if err := wipeHome(filepath.Join(home, "missing")); err != nil {
		t.Fatalf("wipe of a missing home: %v", err)
	}
}
