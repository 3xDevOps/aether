//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCheckDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := checkDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("created data directory: %v %v, want mode 0700", info.Mode(), err)
	}

	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "data directory " + open + " has mode 0755, which lets every user on this machine into the directory of the edge's signing key; run chmod o-rwx " + open
	if err := checkDataDir(open); err == nil || err.Error() != want {
		t.Errorf("world-readable data directory: %v, want %s", err, want)
	}

	if os.Geteuid() == 0 {
		// Root writes a directory of any mode; what it must not do is open
		// the files of the uid the edge runs as.
		foreign := t.TempDir()
		if err := os.Chmod(foreign, 0o700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(foreign, "edge_key")
		if err := os.WriteFile(key, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(key, 65532, 65532); err != nil {
			t.Fatal(err)
		}
		want := key + " belongs to uid 65532 and aether-edge runs as uid 0; the edge opens only files it created, so run chown -R 0 " + foreign
		if err := checkDataDir(foreign); err == nil || err.Error() != want {
			t.Errorf("another user's signing key: %v, want %s", err, want)
		}
		t.Skip("root writes a directory of any mode")
	}
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDir(readOnly); err == nil || !strings.Contains(err.Error(), ", mode 0500) is not writable by uid ") ||
		!strings.Contains(err.Error(), "give that user write access as the directory's owner or through its group") {
		t.Errorf("read-only data directory: %v", err)
	}
	if err := checkDataDir(filepath.Join(readOnly, "data")); err == nil || !strings.Contains(err.Error(), "create it for uid") {
		t.Errorf("data directory in a read-only parent: %v", err)
	}
}

// TestServeInAGroupOwnedDataDir runs the edge as uid 65532 on a data
// directory root:65533 mode 2770, as a container platform that grants a
// volume to a group does.
func TestServeInAGroupOwnedDataDir(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to run the edge as another uid")
	}
	// The edge's uid must reach the binary and its files: t.TempDir's
	// parent is private to root.
	base, err := os.MkdirTemp("", "aether-edge-group-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	bin := filepath.Join(base, "aether-edge")
	empty, data, secret := filepath.Join(base, "empty"), filepath.Join(base, "data"), filepath.Join(base, "secret")
	for _, setupErr := range []error{
		os.Chmod(base, 0o755),
		os.Mkdir(empty, 0o555),
		os.WriteFile(secret, []byte(fakeSecret), 0o440),
		os.Chown(secret, 65532, 65533),
		os.Mkdir(data, 0o700),
		os.Chown(data, 0, 65533),
		os.Chmod(data, 0o770|os.ModeSetgid),
	} {
		if setupErr != nil {
			t.Fatal(setupErr)
		}
	}
	copyFile(t, os.Args[0], bin)
	serveAs := func(uid uint32) *exec.Cmd {
		cmd := serveCommand(data, empty, secret)
		cmd.Path, cmd.Args[0] = bin, bin
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{65533}}}
		return cmd
	}

	p := startEdgeCommand(t, serveAs(65532))
	p.fingerprint(t)
	p.stop(t)
	for _, name := range []string{"edge_key", "edge.db"} {
		info, statErr := os.Stat(filepath.Join(data, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != 65532 || st.Gid != 65533 || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: uid %d gid %d mode %04o, want 65532, 65533 (the directory's group) and 0600", name, st.Uid, st.Gid, info.Mode().Perm())
		}
	}

	// Another uid of the group reaches the directory but not the files
	// the edge made 0600.
	out, err := serveAs(65534).CombinedOutput()
	want := "aether-edge: " + filepath.Join(data, "edge_key") + " belongs to uid 65532 and aether-edge runs as uid 65534; the edge opens only files it created, so run chown -R 65534 " + data + "\n"
	if exit := (*exec.ExitError)(nil); !errors.As(err, &exit) || string(out) != want {
		t.Errorf("another uid of the group: %v\n%s\nwant %s", err, out, want)
	}

	if err = os.Chmod(data, 0o750|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	out, err = serveAs(65532).CombinedOutput()
	want = "aether-edge: data directory " + data + " (uid 0, gid 65533, mode 0750) is not writable by uid 65532, the user aether-edge runs as: permission denied; " +
		"give that user write access as the directory's owner or through its group, or name another with --data or AETHER_EDGE_DATA\n"
	if exit := (*exec.ExitError)(nil); !errors.As(err, &exit) || string(out) != want {
		t.Errorf("directory its group cannot write: %v\n%s\nwant %s", err, out, want)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close() //nolint:errcheck // read-only
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
