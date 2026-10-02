//go:build !linux

package scheduler

import (
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// applyRunOwnership needs Unix ownership semantics (hardlink inodes,
// chown); the server only ships for Linux. Root runs (user == "") need no
// pass and keep non-Linux development hosts working.
func (s *Scheduler) applyRunOwnership(_ *domain.Workspace, _ *domain.Run, _ []runtime.Mount, user string) error {
	if user == "" {
		return nil
	}
	return errors.New("scheduler: non-root run users require a linux host")
}

// applyLoginOwnership has nothing to do here: applyRunOwnership already
// refused a non-root user.
func (s *Scheduler) applyLoginOwnership(_ *supervised, _ domain.MemberID, _ []runtime.Mount, _ string) error {
	return nil
}
