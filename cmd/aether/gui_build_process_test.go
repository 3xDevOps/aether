//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
)

// This invokes the real CLI entry point in a child whose inherited SIGINT is
// ignored, as it is when the installer launches an interactive command. The
// fake Node tools keep npm local and observable, so no network or Electron
// download is part of the cancellation check.
func TestGUIBuildCLIExitStatuses(t *testing.T) {
	if os.Getenv("AETHER_GUI_BUILD_HELPER") == "1" {
		os.Args = []string{
			os.Args[0],
			"gui", "build", "--json",
			"--build-dir", os.Getenv("AETHER_GUI_BUILD_DIR"),
		}
		main()
		return
	}

	for _, tc := range []struct {
		name   string
		mode   string
		signal syscall.Signal
		want   int
	}{
		{name: "SIGINT", mode: "wait", signal: syscall.SIGINT, want: 130},
		{name: "SIGTERM", mode: "wait", signal: syscall.SIGTERM, want: 143},
		{name: "child-reported SIGINT", mode: "child-int", want: 130},
		{name: "ordinary failure", mode: "fail", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0o755); err != nil {
				t.Fatal(err)
			}
			buildDir := filepath.Join(root, "build")
			marker := filepath.Join(root, "npm.started")
			pidFile := filepath.Join(root, "npm.pid")
			node := filepath.Join(tools, "node")
			npm := filepath.Join(tools, "npm")
			npx := filepath.Join(tools, "npx")
			aether := filepath.Join(tools, "aether")
			writeTestExecutable(t, node, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'v22.23.2\\n'; fi\n")
			writeTestExecutable(t, npm, "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$AETHER_FAKE_NPM_PID\"\n: > \"$AETHER_FAKE_NPM_MARKER\"\ncase \"$AETHER_FAKE_NPM_MODE\" in\nfail)\n  printf 'controlled npm failure (exit 23)\\n' >&2\n  exit 23\n  ;;\nchild-int)\n  exit 130\n  ;;\nwait)\n  trap 'exit 130' INT\n  trap 'exit 143' TERM\n  while :; do sleep 1; done\n  ;;\nesac\n")
			writeTestExecutable(t, npx, "#!/bin/sh\nexit 0\n")
			writeTestExecutable(t, aether, "#!/bin/sh\nexit 0\n")

			var stdout, stderr bytes.Buffer
			cmd := exec.Command("sh", "-c",
				"trap '' INT; exec \"$1\" -test.run=TestGUIBuildCLIExitStatuses",
				"aether-test", os.Args[0])
			cmd.Env = append(os.Environ(),
				"AETHER_GUI_BUILD_HELPER=1",
				"AETHER_GUI_BUILD_DIR="+buildDir,
				"AETHER_FAKE_NPM_MODE="+tc.mode,
				"AETHER_FAKE_NPM_MARKER="+marker,
				"AETHER_FAKE_NPM_PID="+pidFile,
				"AETHER_BIN="+aether,
				"HOME="+filepath.Join(root, "home"),
				"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
				"XDG_DATA_HOME="+filepath.Join(root, "data"),
				"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					_ = cmd.Wait()
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					_, _ = waitForTestCommand(cmd, 5*time.Second)
					waited = true
					t.Fatal("npm did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.signal != 0 {
				if err := syscall.Kill(-cmd.Process.Pid, tc.signal); err != nil {
					t.Fatalf("signal process group: %v", err)
				}
			}
			waitErr, timedOut := waitForTestCommand(cmd, 10*time.Second)
			waited = true
			if timedOut {
				t.Fatalf("CLI process did not exit after cancellation: %v", waitErr)
			}
			if waitErr != nil && cmd.ProcessState.ExitCode() < 0 {
				t.Fatalf("CLI process failed to exit normally: %v", waitErr)
			}
			if got := cmd.ProcessState.ExitCode(); got != tc.want {
				t.Fatalf("exit status = %d, want %d; stdout=%q stderr=%q", got, tc.want, stdout.String(), stderr.String())
			}
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(3 * time.Second)
			for {
				if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("npm process %d survived CLI cancellation", pid)
				}
				time.Sleep(10 * time.Millisecond)
			}

			var errorEvent bool
			for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
				var event buildEvent
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatalf("invalid JSON event %q: %v", line, err)
				}
				if event.Phase == localops.PhaseDone {
					t.Fatal("canceled build emitted a successful done event")
				}
				if event.Phase == localops.PhaseError {
					errorEvent = true
					if tc.mode == "fail" && !strings.Contains(event.Error, "exit status 23") {
						t.Fatalf("error event lost the npm failure status: %q", event.Error)
					}
				}
			}
			if !errorEvent {
				t.Fatalf("events = %q, want an error event", stdout.String())
			}
			if tc.mode == "fail" && !strings.Contains(stderr.String(), "controlled npm failure (exit 23)") {
				t.Fatalf("stderr lost the npm error: %q", stderr.String())
			}
		})
	}
}

func writeTestExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForTestCommand(cmd *exec.Cmd, timeout time.Duration) (error, bool) {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, false
	case <-timer.C:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return <-done, true
	}
}
