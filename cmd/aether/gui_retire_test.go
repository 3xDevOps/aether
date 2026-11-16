//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
)

// A shell names itself in AETHER_SHELL_EXE. While the product name is the
// legacy one, `aether gui --json` serves as before: the handshake is the
// first stdout line and the app that started it stays installed.
func TestGUIKeepsTheAppThatStartedIt(t *testing.T) {
	if os.Getenv("AETHER_GUI_RETIRE_HELPER") == "1" {
		os.Args = []string{os.Args[0], "gui", "--json", "--port", "0"}
		main()
		return
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	exe := localops.DesktopAppExe("linux", filepath.Join(home, ".local", "share", "aether", "desktop"))
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, exe, "#!/bin/sh\nexit 0\n")

	cmd := exec.Command(os.Args[0], "-test.run=^TestGUIKeepsTheAppThatStartedIt$")
	cmd.Env = append(os.Environ(),
		"AETHER_GUI_RETIRE_HELPER=1",
		localops.ShellExeEnv+"="+exe,
		"HOME="+home,
		"XDG_DATA_HOME=",
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"AETHER_CONFIG_DIR=",
		// The update nudge must not reach the network.
		"HTTPS_PROXY=http://127.0.0.1:1",
		"HTTP_PROXY=http://127.0.0.1:1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	stop := func() string {
		if !waited {
			waited = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return stderr.String()
	}
	t.Cleanup(func() { stop() })
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var handshake struct{ URL, Addr string }
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(line), &handshake); err != nil || handshake.URL == "" {
			t.Fatalf("handshake %q: %v; stderr: %s", line, err, stop())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no handshake; stderr: %s", stop())
	}
	// SIGTERM only cancels the gateway's context, so the step after the
	// handshake still runs before the process exits.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, stdout)
	waited = true
	if err := cmd.Wait(); err != nil {
		t.Fatalf("aether gui: %v; stderr: %s", err, stderr.String())
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("the app that started the gateway was removed: %v; stderr: %s", err, stderr.String())
	}
}
