package acphost

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A full disk makes a write stop part way through a line. RLIMIT_FSIZE
// produces exactly that short write.
func TestLogRecoversFromAShortWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 3, KindUsage)
	_, size, _ := l.state()

	var old syscall.Rlimit
	if err = syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	limited := syscall.Rlimit{Cur: uint64(size) + 10, Max: old.Max}
	if err = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Skipf("cannot limit file size: %v", err)
	}
	err = l.Append(&Item{Kind: KindNotice, Notice: &Notice{Title: strings.Repeat("x", 100)}})
	if rerr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil {
		t.Fatal("append past the file size limit succeeded")
	}

	appendN(t, l, 1, KindUsage)
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	its, err := l.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(its); len(got) != 4 || got[3] != 4 || its[3].Kind != KindUsage {
		t.Fatalf("items after reopen %v", got)
	}
}

func TestOpenLogIsOwnerOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runs", "run_1", "items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	for p, want := range map[string]os.FileMode{
		filepath.Join(root, "runs"):          0o700,
		filepath.Join(root, "runs", "run_1"): 0o700,
		path:                                 0o600,
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", p, got, want)
		}
	}
}

func TestFailedCompactionLeavesCanonicalHistoryIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	appendN(t, l, 20, KindUsage)
	var old syscall.Rlimit
	if limitErr := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); limitErr != nil {
		t.Fatal(limitErr)
	}
	if limitErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 16, Max: old.Max}); limitErr != nil {
		t.Skipf("cannot limit file size: %v", limitErr)
	}
	err = l.compact(l.size - l.offsets[15])
	if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil {
		t.Fatal("compaction unexpectedly succeeded")
	}
	if _, statErr := os.Stat(path + ".compact"); !os.IsNotExist(statErr) {
		t.Fatalf("failed copy remains: %v", statErr)
	}
	appendN(t, l, 1, KindMessage)
	ro, err := OpenLogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	items, err := ro.ReadAfter(0, 0)
	if err != nil || len(items) != 21 || items[0].Seq != 1 || items[20].Seq != 21 {
		t.Fatalf("canonical history after failed copy: %v, %v", seqs(items), err)
	}
}
