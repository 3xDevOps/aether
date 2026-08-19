package overlay

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mutagen-io/mutagen/pkg/synchronization/core"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core/ignore"
	mutagenignore "github.com/mutagen-io/mutagen/pkg/synchronization/core/ignore/mutagen"
)

func TestConflictPathsFlattensDedupsSorts(t *testing.T) {
	conflicts := []*core.Conflict{
		{
			Root:         "b.txt",
			AlphaChanges: []*core.Change{{Path: "b.txt"}},
			BetaChanges:  []*core.Change{{Path: "b.txt"}},
		},
		{
			Root:         "a.txt",
			AlphaChanges: []*core.Change{{Path: "a.txt"}},
			BetaChanges:  []*core.Change{{Path: "a.txt"}},
		},
		{
			// Whole-root conflict: empty change paths fall back to Root.
			Root:         "dir/c.txt",
			AlphaChanges: []*core.Change{{Path: ""}},
		},
	}
	got := conflictPaths(conflicts)
	want := []string{"a.txt", "b.txt", "dir/c.txt"}
	if len(got) != len(want) {
		t.Fatalf("conflictPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conflictPaths = %v, want %v", got, want)
		}
	}
}

func TestPreserveLocalCreatesTwinAndRemovesOriginal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "sub", "f.txt")
	if err := os.WriteFile(src, []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preserveLocal(root, "sub/f.txt"); err != nil {
		t.Fatalf("preserveLocal: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("original still present (err %v), want removed", err)
	}
	twin, err := os.ReadFile(src + ConflictSuffix)
	if err != nil {
		t.Fatalf("read twin: %v", err)
	}
	if string(twin) != "local edit" {
		t.Fatalf("twin content = %q, want %q", twin, "local edit")
	}
}

func TestPreserveLocalMissingFileIsNoop(t *testing.T) {
	root := t.TempDir()
	if err := preserveLocal(root, "gone.txt"); err != nil {
		t.Fatalf("preserveLocal on missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt"+ConflictSuffix)); !os.IsNotExist(err) {
		t.Fatal("twin created for a missing file")
	}
}

func TestPreserveLocalLeavesDirectoriesAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preserveLocal(root, "d"); err != nil {
		t.Fatalf("preserveLocal on dir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "d")); err != nil || !info.IsDir() {
		t.Fatalf("directory removed or replaced (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "d"+ConflictSuffix)); !os.IsNotExist(err) {
		t.Fatal("twin created for a directory")
	}
}

// Regression: a second conflict on the same path must not destroy the
// version the first one preserved. Twins are numbered from the second
// one on, and every earlier twin keeps its original contents.
func TestPreserveLocalNeverDestroysEarlierTwins(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "f.txt")
	writes := []string{"first local edit", "second local edit", "third local edit"}
	for _, content := range writes {
		if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := preserveLocal(root, "f.txt"); err != nil {
			t.Fatalf("preserveLocal(%q): %v", content, err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatalf("original still present after preserving %q (err %v)", content, err)
		}
	}
	for i, want := range writes {
		name := twinName(src, i+1)
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read twin %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("twin %s = %q, want %q", name, got, want)
		}
	}
}

// A pre-existing twin that the overlay did not write is equally
// off-limits: preserving must step around it, not truncate it.
func TestPreserveLocalSkipsForeignTwin(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "f.txt")
	if err := os.WriteFile(src+ConflictSuffix, []byte("hand-saved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preserveLocal(root, "f.txt"); err != nil {
		t.Fatalf("preserveLocal: %v", err)
	}
	kept, err := os.ReadFile(src + ConflictSuffix)
	if err != nil {
		t.Fatalf("read pre-existing twin: %v", err)
	}
	if string(kept) != "hand-saved" {
		t.Fatalf("pre-existing twin = %q, want %q", kept, "hand-saved")
	}
	fresh, err := os.ReadFile(twinName(src, 2))
	if err != nil {
		t.Fatalf("read fresh twin: %v", err)
	}
	if string(fresh) != "local edit" {
		t.Fatalf("fresh twin = %q, want %q", fresh, "local edit")
	}
}

// Every twin name the overlay can generate must stay out of the sync,
// otherwise the preserved version propagates to the run worktree.
func TestTwinNamesMatchSyncIgnore(t *testing.T) {
	ignorer, err := mutagenignore.NewIgnorer([]string{"*" + ConflictSuffix})
	if err != nil {
		t.Fatalf("build ignorer: %v", err)
	}
	for _, n := range []int{1, 2, 10, maxConflictTwins} {
		for _, base := range []string{"f.txt", "sub/f.txt"} {
			name := twinName(base, n)
			if status, _ := ignorer.Ignore(name, false); status != ignore.IgnoreStatusIgnored {
				t.Errorf("twin %q ignore status = %v, want ignored", name, status)
			}
		}
	}
}

func TestConflictErrorUnwrapsToSentinel(t *testing.T) {
	var err error = &Conflict{SessionID: "sync_x", Files: []string{"f.txt"}}
	if !errors.Is(err, ErrConflict) {
		t.Fatal("Conflict does not unwrap to ErrConflict")
	}
	var c *Conflict
	if !errors.As(err, &c) || len(c.Files) != 1 || c.Files[0] != "f.txt" {
		t.Fatalf("errors.As round-trip = %+v", c)
	}
}

func TestNewSessionValidatesOptions(t *testing.T) {
	dial := func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("unused") }
	if _, err := NewSession(Options{Dial: dial}); err == nil {
		t.Fatal("NewSession accepted empty LocalDir")
	}
	if _, err := NewSession(Options{LocalDir: t.TempDir()}); err == nil {
		t.Fatal("NewSession accepted nil Dial")
	}
	if _, err := NewSession(Options{LocalDir: filepath.Join(t.TempDir(), "missing"), Dial: dial}); err == nil {
		t.Fatal("NewSession accepted a missing directory")
	}
	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSession(Options{LocalDir: file, Dial: dial}); err == nil {
		t.Fatal("NewSession accepted a file as LocalDir")
	}
}

// Teardown trigger: Close is safe before Start and when called twice, and
// removes the owned per-invocation data directory.
func TestSessionCloseIsIdempotentAndRemovesOwnedData(t *testing.T) {
	dial := func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("unused") }
	sess, err := NewSession(Options{LocalDir: t.TempDir(), Dial: dial})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	dataDir := sess.dataDir
	if dataDir == "" {
		t.Fatal("no data dir allocated")
	}
	sess.Close()
	if _, serr := os.Stat(dataDir); !os.IsNotExist(serr) {
		t.Fatalf("data dir still present after Close (err %v)", serr)
	}
	sess.Close() // second Close must not panic
}

// Teardown trigger: an explicit DataDir is caller-owned and survives
// Close (the CLI never sets it; tests do).
func TestSessionCloseKeepsCallerOwnedData(t *testing.T) {
	dial := func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("unused") }
	dataDir := t.TempDir()
	sess, err := NewSession(Options{LocalDir: t.TempDir(), Dial: dial, DataDir: dataDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()
	if _, serr := os.Stat(dataDir); serr != nil {
		t.Fatalf("caller-owned data dir removed: %v", serr)
	}
}
