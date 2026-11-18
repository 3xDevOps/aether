package vmimage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPreparedDiskLeavesItsJournalUnallocated(t *testing.T) {
	needTools(t, "mkfs.ext4", "qemu-img")
	s := openStore(t, nil)
	fill := func(_ context.Context, dir string) error {
		return os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	}
	if err := s.NewCheckoutDisk(t.Context(), "k", 128<<30, fill, filepath.Join(t.TempDir(), "disk.qcow2")); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(s.preparedDir(), "k", "disk.raw"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Blocks*512 > 32<<20 {
		t.Errorf("a 128 GiB prepared disk takes %d MiB on the host, want its 1 GiB journal unallocated", st.Blocks*512>>20)
	}
}
