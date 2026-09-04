package main

import (
	"errors"
	"os"
	"testing"
)

// term.GetSize on Windows is GetConsoleScreenBufferInfo, which only accepts a
// console output handle. Querying stdin there always fails, so the probe order
// has to be stdout first with stdin as the redirected-stdout fallback.
func TestTermSizePrefersStdout(t *testing.T) {
	stdout, stdin := int(os.Stdout.Fd()), int(os.Stdin.Fd())
	restore := termSizeOf
	t.Cleanup(func() { termSizeOf = restore })

	probed := []int{}
	termSizeOf = func(fd int) (int, int, error) {
		probed = append(probed, fd)
		switch fd {
		case stdout:
			return 120, 40, nil
		case stdin:
			return 100, 30, nil
		}
		return 0, 0, errors.New("unknown fd")
	}

	cols, rows := termSize()
	if cols != 120 || rows != 40 {
		t.Fatalf("termSize() = %dx%d, want 120x40", cols, rows)
	}
	if len(probed) != 1 || probed[0] != stdout {
		t.Fatalf("probed fds = %v, want stdout (%d) only", probed, stdout)
	}
}

func TestTermSizeFallsBackToStdin(t *testing.T) {
	stdin := int(os.Stdin.Fd())
	restore := termSizeOf
	t.Cleanup(func() { termSizeOf = restore })

	termSizeOf = func(fd int) (int, int, error) {
		if fd == stdin {
			return 100, 30, nil
		}
		return 0, 0, errors.New("not a console")
	}

	if cols, rows := termSize(); cols != 100 || rows != 30 {
		t.Fatalf("termSize() = %dx%d, want 100x30", cols, rows)
	}
}

// Under `go test` neither standard handle is a console, so the real call must
// still yield the 80x24 default instead of a zero size or a panic.
func TestTermSizeDefaultsWhenNoConsole(t *testing.T) {
	if cols, rows := termSize(); cols != 80 || rows != 24 {
		t.Fatalf("termSize() = %dx%d, want 80x24", cols, rows)
	}
}
func TestAttachShellFlagUsage(t *testing.T) {
	err := runAttach([]string{"--shell"})
	if err == nil || err.Error() != "usage: aether attach [--read-only] [--shell <tab>] <run>" {
		t.Fatalf("runAttach missing shell value error = %v", err)
	}
}

func TestEnableVirtualTerminalOnNonConsole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	for name, f := range map[string]*os.File{
		"pipe": w,
		"file": mustTempFile(t),
	} {
		t.Run(name, func(t *testing.T) {
			restore := enableVirtualTerminal(f)
			if restore == nil {
				t.Fatal("enableVirtualTerminal returned a nil restore func")
			}
			restore()
		})
	}
}

func mustTempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "console")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
