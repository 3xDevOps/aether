package vmimage

import (
	"archive/tar"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type entry struct {
	name, link, body string
	typ              byte
	mode             int64
	uid, gid         int
	mtime            time.Time
	xattrs           map[string]string
	size             int64
}

func tarOf(t *testing.T, entries ...entry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: e.mode,
			Uid: e.uid, Gid: e.gid, ModTime: e.mtime, Size: int64(len(e.body)),
			Format: tar.FormatPAX,
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if e.size != 0 {
			hdr.Size = e.size
		}
		for k, v := range e.xattrs {
			if hdr.PAXRecords == nil {
				hdr.PAXRecords = map[string]string{}
			}
			hdr.PAXRecords["SCHILY.xattr."+k] = v
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.size != 0 {
			// The data of a header with a forced size never follows, like a
			// truncated layer, so it must be the last entry.
			return bytes.NewReader(buf.Bytes())
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

// extractInto extracts into root/fs and returns that directory. Entries are
// owned by the test's own uid and gid, so no root is needed.
func extractInto(t *testing.T, entries ...entry) (string, error) {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "fs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].uid == 0 && entries[i].gid == 0 {
			entries[i].uid, entries[i].gid = os.Geteuid(), os.Getegid()
		}
	}
	// Read-only directories would stop TempDir's cleanup.
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	return dir, extract(tarOf(t, entries...), root)
}

func TestExtractRefusesEscapes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []entry
	}{
		{"dot-dot path", []entry{{name: "../outside", body: "x"}}},
		{"nested dot-dot path", []entry{{name: "a/../../outside", body: "x"}}},
		{"write through absolute symlink", []entry{
			{name: "link", typ: tar.TypeSymlink, link: "/"},
			{name: "link/outside", body: "x"},
		}},
		{"write through relative symlink", []entry{
			{name: "a", typ: tar.TypeDir, mode: 0o755},
			{name: "a/link", typ: tar.TypeSymlink, link: "../.."},
			{name: "a/link/outside", body: "x"},
		}},
		{"directory through symlink", []entry{
			{name: "link", typ: tar.TypeSymlink, link: "../"},
			{name: "link/outside", typ: tar.TypeDir, mode: 0o755},
		}},
		{"hard link to a file outside", []entry{{name: "hard", typ: tar.TypeLink, link: "../outside-target"}}},
		{"hard link through symlink", []entry{
			{name: "link", typ: tar.TypeSymlink, link: ".."},
			{name: "hard", typ: tar.TypeLink, link: "link/outside-target"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := extractInto(t, tt.entries...)
			if err == nil {
				t.Fatal("extract() succeeded, want an error")
			}
			parent := filepath.Dir(dir)
			if _, statErr := os.Lstat(filepath.Join(parent, "outside")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("an entry escaped the root: %v", statErr)
			}
		})
	}
}

func TestExtractAbsoluteNamesStayInside(t *testing.T) {
	dir, err := extractInto(t, entry{name: "/etc/hostname", body: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "etc/hostname")); err != nil || string(b) != "guest" {
		t.Fatalf("etc/hostname = %q, %v", b, err)
	}
}

func TestExtractSymlinkToOutsideIsKeptAsALink(t *testing.T) {
	dir, err := extractInto(t,
		entry{name: "etc/passwd-link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		entry{name: "hard", typ: tar.TypeLink, link: "etc/passwd-link"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"etc/passwd-link", "hard"} {
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || target != "/etc/passwd" {
			t.Fatalf("%s -> %q, %v; want a symlink to /etc/passwd", name, target, err)
		}
	}
}

func TestExtractDeviceFiles(t *testing.T) {
	dir, err := extractInto(t, entry{name: "dev/null", typ: tar.TypeChar, mode: 0o666})
	if err != nil {
		t.Fatalf("a device under /dev: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dev/null")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dev/null was created: %v", err)
	}
	for _, typ := range []byte{tar.TypeChar, tar.TypeBlock} {
		if _, err := extractInto(t, entry{name: "usr/sda", typ: typ, mode: 0o666}); err == nil || !strings.Contains(err.Error(), "device file outside /dev") {
			t.Fatalf("device outside /dev: %v, want refusal", err)
		}
	}
}

func TestExtractRefusesConflictsAndBadEntries(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []entry
		want    string
	}{
		{"duplicate file", []entry{{name: "a", body: "1"}, {name: "a", body: "2"}}, "file exists"},
		{"file then directory", []entry{{name: "a", body: "1"}, {name: "a", typ: tar.TypeDir, mode: 0o755}}, "listed twice"},
		{"symlink then directory", []entry{{name: "a", typ: tar.TypeSymlink, link: "b"}, {name: "a", typ: tar.TypeDir, mode: 0o755}}, "listed twice"},
		{"missing hard link target", []entry{{name: "a", typ: tar.TypeLink, link: "nowhere"}}, "no such file"},
		{"owner out of range", []entry{{name: "a", body: "1", uid: 1 << 33, gid: 1}}, "has owner"},
		{"unsupported type", []entry{{name: "a", typ: 'V'}}, "unsupported type"},
		{"image too large", []entry{{name: "big", size: maxImageBytes + 1}}, "larger than"},
		{"truncated file", []entry{{name: "cut", size: 4096}}, "unexpected EOF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractInto(t, tt.entries...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("extract() = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestExtractKeepsMetadata(t *testing.T) {
	mtime := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	dir, err := extractInto(t,
		entry{name: "usr/bin/tool", body: "#!/bin/sh\n", mode: 0o4755, mtime: mtime, xattrs: map[string]string{"user.aether": "kept"}},
		entry{name: "usr/bin/alias", typ: tar.TypeLink, link: "usr/bin/tool"},
		entry{name: "usr/bin/sh", typ: tar.TypeSymlink, link: "dash"},
		entry{name: "run/fifo", typ: tar.TypeFifo, mode: 0o620},
		// Upper layers come first: the directory follows its contents.
		entry{name: "usr/bin", typ: tar.TypeDir, mode: 0o2775, mtime: mtime},
		entry{name: "usr", typ: tar.TypeDir, mode: 0o555, mtime: mtime},
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "usr/bin/tool")
	info, err := os.Lstat(tool)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode(); got != 0o755|fs.ModeSetuid {
		t.Errorf("tool mode = %v, want -rwsr-xr-x", got)
	}
	if !info.ModTime().Equal(mtime) {
		t.Errorf("tool mtime = %v, want %v", info.ModTime(), mtime)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Nlink != 2 {
		t.Errorf("tool has %d links, want 2 (the hard link)", st.Nlink)
	}
	buf := make([]byte, 16)
	if n, err := unix.Getxattr(tool, "user.aether", buf); err != nil {
		t.Logf("user xattrs unsupported here: %v", err)
	} else if string(buf[:n]) != "kept" {
		t.Errorf("user.aether = %q, want kept", buf[:n])
	}
	if target, err := os.Readlink(filepath.Join(dir, "usr/bin/sh")); err != nil || target != "dash" {
		t.Errorf("usr/bin/sh -> %q, %v", target, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "run/fifo")); err != nil || info.Mode() != fs.ModeNamedPipe|0o620 {
		t.Errorf("run/fifo = %v, %v", info, err)
	}
	for name, want := range map[string]fs.FileMode{"usr/bin": fs.ModeDir | fs.ModeSetgid | 0o775, "usr": fs.ModeDir | 0o555} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%s mode = %v, want %v", name, info.Mode(), want)
		}
		if !info.ModTime().Equal(mtime) {
			t.Errorf("%s mtime = %v, want %v", name, info.ModTime(), mtime)
		}
	}
	if info, err := os.Lstat(filepath.Join(dir, "run")); err != nil || info.Mode() != fs.ModeDir|0o755 {
		t.Errorf("implied directory run = %v, %v; want drwxr-xr-x", info, err)
	}
}

func TestExtractHardLinkChainAcrossLayers(t *testing.T) {
	// Three layers flattened upper first: c links to b, b links to a.
	dir, err := extractInto(t,
		entry{name: "c", typ: tar.TypeLink, link: "b"},
		entry{name: "b", typ: tar.TypeLink, link: "a"},
		entry{name: "a", body: "shared"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != "shared" {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}
	if got := nlink(t, filepath.Join(dir, "a")); got != 3 {
		t.Errorf("a has %d links, want 3", got)
	}
}

// TestExtractOwnersAsRoot needs root: it hands files to other users and sets
// a file capability, as the server does.
func TestExtractOwnersAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to set other owners and security.capability")
	}
	// cap_net_raw=ep, as `setcap cap_net_raw=ep` writes it.
	capNetRaw := string([]byte{0, 0, 0, 2, 0, 0x20, 0, 0, 0, 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	dir, err := extractInto(t,
		entry{name: "usr/bin/ping", body: "elf", mode: 0o755, uid: 0, gid: 0, xattrs: map[string]string{"security.capability": capNetRaw}},
		entry{name: "usr/bin/sudo", body: "elf", mode: 0o4755, uid: 0, gid: 0},
		entry{name: "home/agent", typ: tar.TypeDir, mode: 0o700, uid: 1000, gid: 1000},
		entry{name: "home/agent/link", typ: tar.TypeSymlink, link: "x", uid: 1000, gid: 1001},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][2]uint32{"usr/bin/ping": {0, 0}, "home/agent": {1000, 1000}, "home/agent/link": {1000, 1001}} {
		info, statErr := os.Lstat(filepath.Join(dir, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != want[0] || st.Gid != want[1] {
			t.Errorf("%s owner = %d:%d, want %d:%d", name, st.Uid, st.Gid, want[0], want[1])
		}
	}
	buf := make([]byte, 64)
	n, err := unix.Getxattr(filepath.Join(dir, "usr/bin/ping"), "security.capability", buf)
	if err != nil || string(buf[:n]) != capNetRaw {
		t.Errorf("ping security.capability = %x, %v", buf[:max(n, 0)], err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "usr/bin/sudo")); err != nil || info.Mode()&fs.ModeSetuid == 0 {
		t.Errorf("sudo lost its set-user-ID bit: %v, %v", info, err)
	}
}
