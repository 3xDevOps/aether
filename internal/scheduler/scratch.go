package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	scratchDirName     = "scratch"
	terminalScratchTag = "terminal-"
	// A directory is planned before its owner is registered.
	scratchOrphanAge = time.Hour
)

func (s *Scheduler) scratchRoot() string {
	root := s.cfg.DataDir
	if root == "" {
		root = s.cfg.StateDir
	}
	return filepath.Join(root, scratchDirName)
}

func terminalScratchName(member domain.MemberID) string {
	return terminalScratchTag + string(member)
}

// ensureScratch creates the host directory bound at /tmp. Container
// processes run as arbitrary users, so it is world-writable with the sticky
// bit, like /tmp itself.
func (s *Scheduler) ensureScratch(name string) (string, error) {
	dir := filepath.Join(s.scratchRoot(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("scheduler: create scratch directory: %w", err)
	}
	if err := os.Chmod(dir, os.ModeSticky|0o777); err != nil {
		return "", fmt.Errorf("scheduler: set scratch directory mode: %w", err)
	}
	return dir, nil
}

func (s *Scheduler) removeScratch(name string) {
	if err := s.cfg.RemoveAll(context.Background(), filepath.Join(s.scratchRoot(), name)); err != nil {
		slog.Warn("scheduler: remove scratch directory", "name", name, "error", err)
	}
}

// sweepScratch removes scratch directories whose run or terminal no longer
// has a container to own them.
func (s *Scheduler) sweepScratch(ctx context.Context) {
	entries, err := os.ReadDir(s.scratchRoot())
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("scheduler: list scratch directories", "error", err)
		}
		return
	}
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: scratch sweep: list active runs", "error", err)
		return
	}
	owned := make(map[string]bool, len(active))
	for _, run := range active {
		owned[string(run.ID)] = true
	}
	s.mu.Lock()
	for id := range s.runs {
		owned[string(id)] = true
	}
	for id := range s.pending {
		owned[string(id)] = true
	}
	for member := range s.terminals {
		owned[terminalScratchName(member)] = true
	}
	s.mu.Unlock()
	for _, entry := range entries {
		name := entry.Name()
		if owned[name] {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < scratchOrphanAge {
			continue
		}
		if member, ok := strings.CutPrefix(name, terminalScratchTag); ok {
			s.sweepTerminalScratch(ctx, domain.MemberID(member))
			continue
		}
		if _, err := os.Stat(s.sidecarPath(domain.RunID(name))); err == nil {
			continue
		}
		s.removeScratch(name)
	}
}

// sweepTerminalScratch removes a terminal's scratch only under its terminal
// lock, so an open cannot recreate the directory between the ownership check
// and the removal. A busy lock skips it until the next sweep.
func (s *Scheduler) sweepTerminalScratch(ctx context.Context, member domain.MemberID) {
	lock := s.terminalLock(member)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	if s.lookupLiveTerminal(member) != nil {
		return
	}
	if _, err := s.cfg.Store.GetTerminal(ctx, member); err == nil {
		return
	}
	if _, err := os.Stat(s.terminalSidecarPath(member)); err == nil {
		return
	}
	s.removeScratch(terminalScratchName(member))
}
