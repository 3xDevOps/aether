//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSetupCommandStopsAtConfirmationWithoutWriting(t *testing.T) {
	if os.Getenv("AETHER_SERVER_SETUP_COMMAND") == "1" {
		os.Args = []string{
			"aether-server", "setup",
			"--config", os.Getenv("AETHER_SERVER_SETUP_CONFIG"),
			"--unit", os.Getenv("AETHER_SERVER_SETUP_UNIT"),
		}
		main()
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("interactive server setup requires root")
	}
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		code int
	}{
		{name: "SIGINT", sig: syscall.SIGINT, code: 130},
		{name: "SIGTERM", sig: syscall.SIGTERM, code: 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "server.conf")
			unitPath := filepath.Join(dir, "aether-server.service")
			dataPath := filepath.Join(dir, "data")

			fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			master := os.NewFile(uintptr(fd), "/dev/ptmx")
			t.Cleanup(func() { _ = master.Close() })
			ptn, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = slave.Close() })

			cmd := exec.Command("/bin/sh", "-c", "trap '' INT; exec \"$@\"", "setup-test",
				os.Args[0], "-test.run=^TestSetupCommandStopsAtConfirmationWithoutWriting$")
			cmd.Env = append(os.Environ(),
				"AETHER_SERVER_SETUP_COMMAND=1",
				"AETHER_SERVER_SETUP_CONFIG="+configPath,
				"AETHER_SERVER_SETUP_UNIT="+unitPath,
			)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = slave.Close()
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-exited
				}
			})

			var transcript strings.Builder
			waitFor := func(prompt string) {
				if err := master.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var buf [512]byte
				for !strings.Contains(transcript.String(), prompt) {
					n, err := master.Read(buf[:])
					transcript.Write(buf[:n])
					if err != nil {
						t.Fatalf("waiting for %q: %v; output=%q", prompt, err, transcript.String())
					}
				}
			}
			waitFor("SSH listen address")
			if _, err := master.Write([]byte("127.0.0.1:2222\n" + dataPath + "\nfalse\ntrue\n")); err != nil {
				t.Fatal(err)
			}
			waitFor("Write it (yes/no)")
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				waited = true
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.code {
					t.Fatalf("setup exit = %v, want %d; output=%q", err, tc.code, transcript.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("setup did not stop after cancellation")
			}
			for _, path := range []string{configPath, unitPath, dataPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s exists after cancellation: %v", path, err)
				}
			}
		})
	}
}
