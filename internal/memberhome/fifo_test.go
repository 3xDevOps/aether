//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package memberhome

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A container can turn a file in its home into a FIFO at any moment. A
// blocking open of a FIFO with no writer never returns, so every read the
// server makes has to come back from one promptly, refusing it.

func TestOpenRegularRefusesAFIFOPromptly(t *testing.T) {
	_, home := newSigningManager(t)
	if err := syscall.Mkfifo(filepath.Join(home, ".gitconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	errs := make(chan error, 1)
	go func() {
		f, _, err := openRegular(root, gitConfigName)
		if f != nil {
			_ = f.Close()
		}
		errs <- err
	}()
	select {
	case err := <-errs:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("openRegular on a FIFO = %v, want a refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("openRegular blocked on a FIFO")
	}
}

func TestSigningKeySurvivesASwapToAFIFO(t *testing.T) {
	manager, home := newSigningManager(t)
	if _, err := manager.EnsureSigningKey("member-1"); err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	path := filepath.Join(home, ".ssh", "aether_signing")
	key, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The swapper keeps replacing the key with a FIFO and putting it back
	// for as long as the reads run, so some reads land on the FIFO in the
	// window after the path check.
	ctx, cancel := context.WithCancel(t.Context())
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for ctx.Err() == nil {
			_ = os.Remove(path)
			_ = syscall.Mkfifo(path, 0o600)
			_ = os.Remove(path)
			_ = os.WriteFile(path, key, 0o600)
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			// Every answer is acceptable except a hang: the key, no key
			// (removed at that instant), or a refusal of the FIFO.
			_, _ = manager.SigningKey("member-1")
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("SigningKey blocked on a FIFO swapped in under it")
	}
	cancel()
	<-swapped
}
