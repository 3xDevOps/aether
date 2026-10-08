package scheduler

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Attribution comes only from the server-built managed mount, not the current
// run owner or a shared vendor account. No client-provided owner ID is needed.
func (s *Scheduler) verificationCacheOwner(spec *runtime.Spec) (domain.MemberID, string, error) {
	var member domain.MemberID
	for _, mount := range spec.Mounts {
		if mount.ContainerPath != "/aether-cache" {
			continue
		}
		if s.cfg.Homes == nil || member != "" || mount.ReadOnly {
			return "", "", errors.New("scheduler: invalid verification cache mount")
		}
		rel, err := filepath.Rel(s.cfg.Homes.CacheRoot(), mount.HostPath)
		parts := strings.Split(rel, string(filepath.Separator))
		if err != nil || len(parts) != 3 || parts[0] == ".." || parts[0] == "." || parts[0] == "" ||
			parts[1] != memberhome.CachePoolRuns || parts[2] != "data" {
			return "", "", errors.New("scheduler: verification cache mount is not an owned runs pool")
		}
		member = domain.MemberID(parts[0])
	}
	if member == "" {
		return "", "", nil
	}
	return member, memberhome.CachePoolRuns, nil
}
