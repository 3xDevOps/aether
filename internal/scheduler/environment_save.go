package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrTerminalNotRunning reports that an environment save needs an open terminal.
var ErrTerminalNotRunning = errors.New("terminal: environment terminal is not running; open it first")

func memberImageRepo(member domain.MemberID) string {
	return "aether/member-" + string(member)
}

// SaveEnvironment snapshots the running environment terminal and records its image for the member.
func (s *Scheduler) SaveEnvironment(ctx context.Context, member domain.MemberID) (string, error) {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

	sup := s.lookupTerminal(member)
	if sup == nil {
		return "", ErrTerminalNotRunning
	}
	if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
		return "", fmt.Errorf("scheduler: get member to save environment: %w", err)
	}
	tag := fmt.Sprintf("%s:%d", memberImageRepo(member), time.Now().Unix())
	if err := s.cfg.Runtime.Commit(ctx, sup.containerID, tag); err != nil {
		return "", fmt.Errorf("scheduler: save environment: %w", err)
	}
	if err := s.cfg.Store.UpdateMemberImage(ctx, member, tag); err != nil {
		return "", fmt.Errorf("scheduler: save environment: persist image: %w", err)
	}
	s.sweepMemberImages(ctx, member, tag)
	return tag, nil
}

// ResetEnvironment stops the environment terminal and returns the member to the standard image.
func (s *Scheduler) ResetEnvironment(ctx context.Context, member domain.MemberID) error {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

	if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
		return fmt.Errorf("scheduler: get member to reset environment: %w", err)
	}
	if err := s.stopTerminalLocked(ctx, member); err != nil {
		return fmt.Errorf("scheduler: reset environment: %w", err)
	}
	if err := s.cfg.Store.UpdateMemberImage(ctx, member, ""); err != nil {
		return fmt.Errorf("scheduler: reset environment: clear image: %w", err)
	}
	s.sweepMemberImages(ctx, member, "")
	return nil
}

// sweepMemberImages untags every saved image of the member except keep.
// A tag a live container still uses cannot be removed yet; the daemon
// refuses, and the next save or reset retries it. Removal never fails the
// save or reset that triggered it: the member's record is already current.
func (s *Scheduler) sweepMemberImages(ctx context.Context, member domain.MemberID, keep string) {
	tags, err := s.cfg.Runtime.ListImageTags(ctx, memberImageRepo(member))
	if err != nil {
		slog.Warn("scheduler: list member environment images", "member", member, "error", err)
		return
	}
	for _, tag := range tags {
		if tag == keep {
			continue
		}
		if err := s.cfg.Runtime.RemoveImage(ctx, tag); err != nil {
			slog.Warn("scheduler: remove stale member environment image", "member", member, "image", tag, "error", err)
		}
	}
}
