//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitengine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckoutConfigFIFOIsRejectedWithoutBlocking(t *testing.T) {
	e := newUnitEngine(t)
	checkout := filepath.Join(e.cfg.CheckoutsDir, "run-fifo")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(checkout, ".git", "config")
	if err := syscall.Mkfifo(config, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := e.checkoutObjectFormat(checkout)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("FIFO checkout config was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("checkout config FIFO read blocked")
	}
}

func TestCheckoutMetadataSymlinkCannotReadOutsideRoot(t *testing.T) {
	e := newUnitEngine(t)
	checkout := filepath.Join(e.cfg.CheckoutsDir, "run-link")
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideConfig := filepath.Join(outside, "config")
	if err := os.WriteFile(outsideConfig, []byte("[extensions]\nobjectformat = sha256\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideConfig, filepath.Join(checkout, ".git", "config")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkoutObjectFormat(checkout); err == nil {
		t.Fatal("checkout config symlink was accepted")
	}

	outsideExclude := filepath.Join(outside, "exclude")
	if err := os.WriteFile(outsideExclude, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideExclude, filepath.Join(checkout, ".git", "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	err := e.syncCheckoutExclude(checkout, filepath.Join(t.TempDir(), "scratch"))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout exclude symlink error = %v, want refusal", err)
	}
}

func TestResolveCheckoutHEADReadsPackedRef(t *testing.T) {
	e := newUnitEngine(t)
	checkout := filepath.Join(e.cfg.CheckoutsDir, "run-packed")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	oid := strings.Repeat("a", 40)
	if err := os.WriteFile(filepath.Join(checkout, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packed := "# pack-refs with: peeled fully-peeled sorted\n" + oid + " refs/heads/main\n"
	if err := os.WriteFile(filepath.Join(checkout, ".git", "packed-refs"), []byte(packed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.resolveCheckoutHEAD(checkout)
	if err != nil || got != oid {
		t.Fatalf("resolve packed checkout HEAD = %q, %v; want %q", got, err, oid)
	}
}

func TestCheckoutMetadataReadsAreBounded(t *testing.T) {
	e := newUnitEngine(t)
	checkout := filepath.Join(e.cfg.CheckoutsDir, "run-bounded")
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "config"), []byte(strings.Repeat("x", maxCheckoutConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkoutObjectFormat(checkout); err == nil {
		t.Fatal("oversized checkout config was accepted")
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "info", "exclude"), []byte(strings.Repeat("x", maxCheckoutExcludeBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.syncCheckoutExclude(checkout, filepath.Join(t.TempDir(), "scratch")); err == nil {
		t.Fatal("oversized checkout exclude was accepted")
	}
}

// Git 2.44 and later write a new repository's config before its HEAD. The
// wrapper holds that window open on any Git version.
func TestCheckoutHeadWaitsForAScratchInitInProgress(t *testing.T) {
	e := newUnitEngine(t)
	seedEvidenceWorkspace(t, e, "ws-init")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws-init", "run-init", "main", "init race", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := e.snapshotStorePath("run-init")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"case \" $* \" in *\" init \"*)\n" +
		"  \"" + git + "\" \"$@\" || exit\n" +
		"  mv \"$last/HEAD\" \"$last/HEAD.hold\"\n" +
		"  sleep 1\n" +
		"  mv \"$last/HEAD.hold\" \"$last/HEAD\"\n" +
		"  exit 0;;\n" +
		"esac\n" +
		"exec \"" + git + "\" \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	e.cfg.GitPath = wrapper
	first := make(chan error, 1)
	go func() {
		_, headErr := e.checkoutHead(ctx, "run-init", checkout)
		first <- headErr
	}()
	for {
		if _, statErr := os.Stat(filepath.Join(store, "HEAD.hold")); statErr == nil {
			break
		}
		select {
		case firstErr := <-first:
			t.Fatalf("the first lookup ended before its init held HEAD: %v", firstErr)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err = e.checkoutHead(ctx, "run-init", checkout); err != nil {
		t.Fatalf("lookup during another's init: %v", err)
	}
	if err = <-first; err != nil {
		t.Fatalf("lookup that ran the init: %v", err)
	}
}
