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
	cmd := exec.CommandContext(ctx, "/bin/sh", f.Name())
	cmd.Stdout = io.MultiWriter(out, last)
	cmd.Stderr = cmd.Stdout
	err = cmd.Run()
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
