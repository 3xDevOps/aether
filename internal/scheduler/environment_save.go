package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
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
	if s.cfg.Homes != nil {
		unlock := s.cfg.Homes.LockCaches(member)
		defer unlock()
		if err := s.cfg.Homes.EnsureCacheMetadata(member, "terminal"); err != nil {
			return "", fmt.Errorf("scheduler: preserve saved image cleanup owner: %w", err)
		}
	}

	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return "", ErrTerminalNotRunning
	}
	if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
		return "", fmt.Errorf("scheduler: get member to save environment: %w", err)
	}
	tag := memberImageRepo(member) + ":" + strings.ToLower(rand.Text())
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
	if s.cfg.Homes != nil {
		unlock := s.cfg.Homes.LockCaches(member)
		defer unlock()
		if err := s.cfg.Homes.EnsureCacheMetadata(member, "terminal"); err != nil {
			return fmt.Errorf("scheduler: preserve saved image cleanup owner: %w", err)
		}
	}

	if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
		return fmt.Errorf("scheduler: get member to reset environment: %w", err)
	}
	if err := s.stopTerminalLocked(ctx, member); err != nil {
		return fmt.Errorf("scheduler: reset environment: %w", err)
	}
	if s.cfg.Homes != nil {
		if err := s.cfg.Homes.TouchCache(member, "terminal"); err != nil {
			return fmt.Errorf("scheduler: record environment cache release: %w", err)
		}
	}
	if err := s.cfg.Store.UpdateMemberImage(ctx, member, ""); err != nil {
		return fmt.Errorf("scheduler: reset environment: clear image: %w", err)
	}
	s.sweepMemberImages(ctx, member, "")
	return nil
}

// sweepMemberImages runs under terminalLock then the member cache lock, like
// periodic maintenance. Every current reference is protected, not just keep.
func (s *Scheduler) sweepMemberImages(ctx context.Context, member domain.MemberID, keep string) {
	refs, err := s.currentImageReferences(ctx)
	if err == nil {
		refs[keep] = true
		err = s.removeObsoleteMemberImages(ctx, member, refs)
	}
	if err != nil {
		slog.Warn("scheduler: clean member environment images", "member", member, "error", err)
	}
	if s.cfg.Homes != nil {
		message := ""
		if err != nil {
			message = "Saved image cleanup failed; automatic retry pending"
		}
		if metaErr := s.cfg.Homes.SetCacheImageCleanupError(member, message); metaErr != nil {
			slog.Warn("scheduler: preserve image cleanup status", "member", member, "error", metaErr)
		}
	}
}

func (s *Scheduler) currentImageReferences(ctx context.Context) (map[string]bool, error) {
	refs := map[string]bool{s.cfg.StandardImage: true, s.cfg.DefaultStandardImage: true, s.cfg.BrowserImage: true}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		refs[member.Image] = true
		terminal, err := s.cfg.Store.GetTerminal(ctx, member.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if terminal != nil {
			refs[terminal.Image] = true
		}
	}
	s.mu.Lock()
	for _, terminal := range s.terminals {
		refs[terminal.image] = true
	}
	s.mu.Unlock()
	return refs, nil
}

func (s *Scheduler) removeObsoleteMemberImages(ctx context.Context, member domain.MemberID, refs map[string]bool) error {
	repo := memberImageRepo(member)
	tags, err := s.cfg.Runtime.ListImageTags(ctx, repo)
	if err != nil {
		return err
	}
	var failures []error
	for _, tag := range tags {
		if refs[tag] || !strings.HasPrefix(tag, repo+":") {
			continue
		}
		if err := s.cfg.Runtime.RemoveImage(ctx, tag); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
