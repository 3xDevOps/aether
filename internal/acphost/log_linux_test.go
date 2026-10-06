package acphost

import (
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
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	limited := syscall.Rlimit{Cur: uint64(size) + 10, Max: old.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
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
	if err := l.Close(); err != nil {
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
