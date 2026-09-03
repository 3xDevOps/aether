//go:build !windows

package profile

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The fixtures here need POSIX file types - a unix socket, a FIFO - that
// Windows has no equivalent for, and syscall.Mkfifo does not exist there
// at all, so the whole file is tagged rather than skipped at runtime:
// `go vet` compiles test files, and an undefined symbol fails the build
// before any skip could run. The cross-platform half of the same
// behaviour is in preview_test.go, which asserts what the mode check
// names each file type.

// TestInventorySkipsNonRegularFiles covers the blocker: a unix socket in a
// profile root - ~/.codex/ipc/ipc.sock exists on any machine codex has run
// on - aborted the whole walk with "no such device or address", so the
// harness could be neither previewed nor pushed.
func TestInventorySkipsNonRegularFiles(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	sock := filepath.Join(root, "ipc", "ipc.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer func() { _ = listener.Close() }()

	preview, err := Inventory(t.Context(), "claude")
	if err != nil {
		t.Fatalf("a socket in the profile root aborted the walk: %v", err)
	}
	var got Exclusion
	for _, e := range preview.Excluded {
		if e.Path == "ipc/ipc.sock" {
			got = e
		}
	}
	if got.Reason != ExcludeNotRegular {
		t.Fatalf("exclusions = %+v, want ipc/ipc.sock as %s", preview.Excluded, ExcludeNotRegular)
	}
	if !strings.Contains(got.Detail, "socket") {
		t.Errorf("detail does not name the file type: %q", got.Detail)
	}
	if preview.Files != 1 {
		t.Errorf("files = %d, want only settings.json", preview.Files)
	}
	// The push agrees: a socket must not abort discovery either.
	files, _, err := DiscoverFiles(t.Context(), "claude", nil)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "settings.json" {
		t.Errorf("discovered %+v, want only settings.json", files)
	}
}

// TestInventoryDoesNotBlockOnAFifo is the half a socket cannot prove:
// os.ReadFile on a FIFO blocks until a writer appears, and the context
// check runs only between entries, so opening one would hang the walk on
// an fd nothing can reclaim. The walk must refuse it on its mode and
// return promptly.
func TestInventoryDoesNotBlockOnAFifo(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Inventory(t.Context(), "claude")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a FIFO in the profile root failed the walk: %v", err)
		}
	case <-time.After(10 * time.Second):
		// Failing rather than hanging the suite: the point of the test is
		// that nothing waits on a FIFO that will never be written to.
		t.Fatal("the walk blocked on a FIFO instead of skipping it")
	}
}

// TestDiscoverNeverOpensASymlinkTarget proves the safety claim behind
// skipping rather than aborting: the target is not read either way. The
// fixture points the link at a FIFO, which os.ReadFile would block on
// forever, so a walk that returns at all is a walk that did not open it.
func TestDiscoverNeverOpensASymlinkTarget(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	target := filepath.Join(t.TempDir(), "trap")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := DiscoverFiles(t.Context(), "claude", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DiscoverFiles: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the walk opened the symlink's target instead of skipping the link")
	}
}
