// Package sysbox installs and reports the Sysbox container runtime, which
// lets runs start their own Docker daemon.
package sysbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/scripts"
)

// unsupportedExit is the install script's exit status for a host that cannot
// run Sysbox, as opposed to an install that failed.
const unsupportedExit = 3

// UnsupportedError is the install script's refusal to run on this host.
type UnsupportedError struct{ Reason string }

func (e *UnsupportedError) Error() string { return e.Reason }

// Status reports protocol.SysboxOn when the Docker daemon lists the
// sysbox-runc runtime and protocol.SysboxOff when it does not.
func Status(ctx context.Context) (string, error) {
	d, err := runtime.NewDocker()
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Close() }()
	found, err := d.SysboxRegistered(ctx)
	switch {
	case err != nil:
		return "", err
	case found:
		return protocol.SysboxOn, nil
	}
	return protocol.SysboxOff, nil
}

// Install runs the embedded installer as the current user, which must be
// root, and streams its output to out. A host Sysbox cannot run on returns
// an *UnsupportedError carrying the script's last message.
func Install(ctx context.Context, out io.Writer) error {
	return runScript(ctx, scripts.InstallSysbox, out)
}

// waitDelay bounds how long Run waits for output after the process is gone.
const waitDelay = 10 * time.Second

// unitName is the transient systemd unit the installer runs in. A fixed name
// keeps a second install from starting beside a running one.
const unitName = "aether-sysbox-install"

// systemdRun is the systemd-run path, or empty when this host does not run
// systemd. Tests clear it.
var systemdRun = findSystemdRun()

func findSystemdRun() string {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return ""
	}
	path, _ := exec.LookPath("systemd-run")
	return path
}

func runScript(ctx context.Context, script string, out io.Writer) error {
	// A file, not stdin: apt-get and docker read stdin and would swallow
	// the rest of the script.
	f, err := os.CreateTemp("", "install-sysbox-*.sh")
	if err != nil {
		return fmt.Errorf("sysbox: write installer: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.WriteString(script); err == nil {
		err = f.Close()
	}
	if err != nil {
		return fmt.Errorf("sysbox: write installer: %w", err)
	}
	last := &lastLine{}
	w := io.MultiWriter(out, last)
	if systemdRun != "" {
		err = runInUnit(ctx, f.Name(), w)
	} else {
		cmd := exec.CommandContext(ctx, "/bin/sh", f.Name())
		cmd.Stdout, cmd.Stderr, cmd.WaitDelay = w, w, waitDelay
		err = cmd.Run()
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == unsupportedExit:
		return &UnsupportedError{Reason: last.String()}
	}
	if msg := last.String(); msg != "" {
		return fmt.Errorf("sysbox: install: %w: %s", err, msg)
	}
	return fmt.Errorf("sysbox: install: %w", err)
}

// runInUnit runs the script in its own systemd unit, outside aether-server's
// cgroup, so restarting the server does not SIGTERM apt and dpkg mid-install.
// The unit writes to a file that is followed here, not to a pipe: once the
// server is gone a pipe would hand the script SIGPIPE and kill it anyway.
// systemd-run --wait exits with the unit's status.
func runInUnit(ctx context.Context, script string, out io.Writer) error {
	logFile, err := os.CreateTemp("", "install-sysbox-*.log")
	if err != nil {
		return fmt.Errorf("sysbox: create installer log: %w", err)
	}
	defer func() { _ = os.Remove(logFile.Name()) }()
	defer func() { _ = logFile.Close() }()
	cmd := exec.CommandContext(ctx, systemdRun, "--unit="+unitName, "--collect", "--wait", "--quiet",
		"-p", "StandardOutput=append:"+logFile.Name(), "-p", "StandardError=append:"+logFile.Name(),
		"/bin/sh", script)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = out, out, waitDelay
	done := make(chan struct{})
	followed := make(chan struct{})
	go func() {
		defer close(followed)
		follow(logFile, out, done)
	}()
	err = cmd.Run()
	close(done)
	<-followed
	return err
}

// follow copies a growing file to out until done closes, then once more.
func follow(f *os.File, out io.Writer, done <-chan struct{}) {
	for {
		_, _ = io.Copy(out, f)
		select {
		case <-done:
			_, _ = io.Copy(out, f)
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// lastLine keeps the last non-empty line written to it, minus the script's
// "install-sysbox: " prefix.
type lastLine struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	last string
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			break
		}
		if line = strings.TrimSpace(line); line != "" {
			l.last = strings.TrimPrefix(line, "install-sysbox: ")
		}
	}
	return len(p), nil
}

func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}
