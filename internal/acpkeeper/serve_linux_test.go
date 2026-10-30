package acpkeeper

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPrivateDirRefusesARootOthersReach(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o733); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{open, link} {
		if err := privateDir(root, filepath.Join(root, "k")); err == nil {
			t.Errorf("privateDir accepted %s", root)
		}
	}

	root := filepath.Join(base, "acp")
	if err := privateDir(root, filepath.Join(root, "k")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("root %v, %v; want mode 0700", info, err)
	}
	if err := privateDir(root, filepath.Join(root, "k")); err == nil {
		t.Fatal("privateDir reused a directory that already exists")
	}
}

func TestCredential(t *testing.T) {
	t.Parallel()
	if c, err := credential("1000:1001"); err != nil || c.Uid != 1000 || c.Gid != 1001 || !slices.Equal(c.Groups, []uint32{1001}) {
		t.Fatalf("credential = %+v, %v; want uid 1000, gid 1001 and groups [1001]", c, err)
	}
	if c, err := credential(""); err != nil || c != nil {
		t.Fatalf("credential(\"\") = %+v, %v; want none", c, err)
	}
	for _, bad := range []string{"1000", "agent:1000", "1000:"} {
		if _, err := credential(bad); err == nil {
			t.Errorf("credential(%q) accepted", bad)
		}
	}
}
