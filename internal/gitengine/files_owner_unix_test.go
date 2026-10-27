//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitengine

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteCheckoutPreservesOwnerAndNestedParents(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise server-owned replacement")
	}
	e := newUnitEngine(t)
	checkout := fileCheckout(t, e)
	if err := os.Chown(checkout, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(checkout, "config.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	revision := fileRevision([]byte("old\n"))
	if _, err := writeCheckoutFile(e, checkout, "missing.txt", []byte("new\n"), revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("missing target with revision error = %v, want ErrRevisionConflict", err)
	}
	if _, err := writeCheckoutFile(e, checkout, "config.txt", []byte("new\n"), revision); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 1000 || info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement owner/mode = %#v/%o, want uid 1000/0600", info.Sys(), info.Mode().Perm())
	}

	if _, err := writeCheckoutFile(e, checkout, "nested/deep/new.txt", []byte("nested\n"), ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(checkout, "nested"),
		filepath.Join(checkout, "nested", "deep"),
		filepath.Join(checkout, "nested", "deep", "new.txt"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 1000 {
			t.Fatalf("%s owner = %#v, want uid 1000", path, info.Sys())
		}
	}
}
