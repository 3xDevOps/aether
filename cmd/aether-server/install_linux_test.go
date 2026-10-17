//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/serversetup"
	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal: what is written to slave is read from
// master.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	t.Cleanup(func() { _ = master.Close() })
	ptn, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func TestNextStepsAreBoldOrangeOnATerminal(t *testing.T) {
	const orange = "\x1b[1m\x1b[38;2;245;130;49m"
	for noColor, want := range map[string]bool{"": true, "1": false} {
		t.Setenv("NO_COLOR", noColor)
		master, slave := openPTY(t)
		if err := printNextSteps(slave, serversetup.DefaultConfigPath, optionsOf(t, map[string]string{"addr": ":2222"}), "", true); err != nil {
			t.Fatal(err)
		}
		_ = slave.Close()
		if err := master.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		var buf [512]byte
		for {
			n, err := master.Read(buf[:])
			got.Write(buf[:n])
			if errors.Is(err, syscall.EIO) {
				break
			}
			if err != nil {
				t.Fatalf("read the terminal: %v; got %q", err, got.String())
			}
		}
		if strings.Contains(got.String(), orange+"aether link <this-host>:2222\x1b[0m") != want ||
			strings.Contains(got.String(), "\x1b") != want {
			t.Errorf("NO_COLOR=%q: want bold orange commands %v, got %q", noColor, want, got.String())
		}
	}
}
