//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// The gateway hands the shell's executable to the retire step after the
// handshake, prints its error to stderr, and leaves the variable out of
// what it starts.
func TestGUIRetiresTheLegacyAppAfterTheHandshake(t *testing.T) {
	if os.Getenv("AETHER_GUI_RETIRE_HELPER") == "2" {
		retireLegacyDesktop = func(goos, shellExe string) error {
			return fmt.Errorf("retire %s %s, env %q", goos, shellExe, os.Getenv(localops.ShellExeEnv))
		}
		os.Args = []string{os.Args[0], "gui", "--json", "--port", "0"}
		main()
		return
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGUIRetiresTheLegacyAppAfterTheHandshake$")
	cmd.Env = append(os.Environ(),
		"AETHER_GUI_RETIRE_HELPER=2",
		localops.ShellExeEnv+"=/apps/cloudfield/desktop/aether-desktop",
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"AETHER_CONFIG_DIR=",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"HTTP_PROXY=http://127.0.0.1:1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, `{"url":`) {
		t.Fatalf("first stdout line = %q, %v", line, err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, stdout)
	_ = cmd.Wait()
	want := `aether gui: retire linux /apps/cloudfield/desktop/aether-desktop, env ""`
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}
