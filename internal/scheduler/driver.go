package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// AgentDriver hosts the agent session of one run: the process the member
// talks to, as opposed to shell tabs and environment terminals, which stay on
// the PTY host. The scheduler picks the driver by the run's launch mode.
type AgentDriver interface {
	// Start opens the agent session on a freshly started container.
	Start(ctx context.Context, entry *supervised, att runtime.Attachment) error
	// Resume reopens the agent session on a container that already ran it:
	// relaunch, restore after a failed close, and recovery after a restart.
	Resume(ctx context.Context, entry *supervised, att runtime.Attachment) error
	Stop(ctx context.Context, run domain.RunID) error
	// LastActivity is when the agent last produced output, zero if never.
	LastActivity(run domain.RunID) time.Time
	// Deliver hands a member's message to the agent as their next prompt.
	Deliver(ctx context.Context, run *domain.Run, member *domain.Member, message string) error
}

// ErrNoAgentDriver rejects a run whose launch mode has no driver.
var ErrNoAgentDriver = errors.New("scheduler: no agent driver for launch mode")

// tuiDriver runs the agent as the child of the container's primary PTY.
type tuiDriver struct {
	pty PTYHost
}

func (d tuiDriver) Start(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	return d.pty.StartSession(ctx, ptyhost.RunSession(entry.runID), att)
}

func (d tuiDriver) Resume(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	return d.pty.StartSession(ctx, ptyhost.RunSession(entry.runID), att)
}

func (d tuiDriver) Stop(ctx context.Context, run domain.RunID) error {
	return d.pty.StopSession(ctx, ptyhost.RunSession(run))
}

func (d tuiDriver) LastActivity(run domain.RunID) time.Time {
	t, _ := d.pty.LastOutput(ptyhost.RunSession(run))
	return t
}

func (d tuiDriver) Deliver(ctx context.Context, run *domain.Run, member *domain.Member, message string) error {
	return d.pty.Inject(ctx, ptyhost.RunSession(run.ID), member.DisplayName, member.Color, message, harness.SubmitSequence(run.Harness))
}

// missingDriver stands in for a launch mode no driver serves, so lifecycle
// code never branches on a nil driver.
type missingDriver struct {
	mode domain.LaunchMode
}

func (d missingDriver) err() error {
	return fmt.Errorf("%w %q", ErrNoAgentDriver, d.mode)
}

func (d missingDriver) Start(context.Context, *supervised, runtime.Attachment) error {
	return d.err()
}

func (d missingDriver) Resume(context.Context, *supervised, runtime.Attachment) error {
	return d.err()
}

func (missingDriver) Stop(context.Context, domain.RunID) error { return nil }

func (missingDriver) LastActivity(domain.RunID) time.Time { return time.Time{} }

func (d missingDriver) Deliver(context.Context, *domain.Run, *domain.Member, string) error {
	return d.err()
}

// driver returns the agent driver for mode. Headless runs also host their
// one-shot agent on the primary PTY.
func (s *Scheduler) driver(mode domain.LaunchMode) AgentDriver {
	switch mode {
	case domain.LaunchTUI, domain.LaunchHeadless:
		return tuiDriver{pty: s.cfg.PTY}
	default:
		return missingDriver{mode: mode}
	}
}
