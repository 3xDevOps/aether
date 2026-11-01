//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSetupCommandStopsAtConfirmationWithoutWriting(t *testing.T) {
	if os.Getenv("AETHER_SERVER_SETUP_COMMAND") == "1" {
		os.Args = []string{
			"aether-server", "setup",
			"--config", os.Getenv("AETHER_SERVER_SETUP_CONFIG"),
			"--unit", os.Getenv("AETHER_SERVER_SETUP_UNIT"),
		}
		dockerCheck = func(context.Context) error { return nil }
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

			master, slave := openPTY(t)
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
			if _, err := master.Write([]byte("127.0.0.1:2222\n" + dataPath + "\n")); err != nil {
				t.Fatal(err)
			}
			waitFor("Choose 1 or 2")
			if _, err := master.Write([]byte("2\n")); err != nil {
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

// Ctrl-C during the Sysbox install stops only the installer: the files are
// written by then, so setup still ends on how to start them.
func TestSetupEndsOnNextStepsWhenSysboxIsInterrupted(t *testing.T) {
	fakeSysbox(t, false, nil)
	sysboxInstall = func(ctx context.Context, _ io.Writer) error {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	t.Setenv("SUDO_USER", "")
	dir := t.TempDir()
	var out bytes.Buffer
	if err := writeSetup(&out, filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf"),
		map[string]string{"data-dir": filepath.Join(dir, "data")}, false, true, true, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Docker inside sessions is not installed: context canceled\n") ||
		!strings.HasSuffix(out.String(), "\n       aether link <this-host>:2222\n") {
		t.Errorf("want the cancelled install reported and the next steps last:\n%s", out.String())
	}
}
