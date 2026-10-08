package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// Verification uses the same uid:gid exclusion as runs and terminals. Cache
// and journal locks serialize repeated preparation/release of a creation key;
// the reservation outlives the short startup capacity allowance.
func (s *Scheduler) reserveVerificationUser(ref verificationBridgeRef) (*credentialUserReservation, bool, error) {
	if ref.CacheMember == "" || ref.CacheUser == "" {
		return nil, false, nil
	}
	s.mu.Lock()
	for reservation := range s.credentialUsers {
		if reservation.verificationKey != ref.CreationKey {
			continue
		}
		s.mu.Unlock()
		if reservation.home != ref.CacheMember || reservation.user != ref.CacheUser {
			return nil, false, errors.New("scheduler: verification user reservation changed")
		}
		return reservation, false, nil
	}
	s.mu.Unlock()
	reservation, err := s.reserveCredentialUser(ref.CacheMember, "", ref.CacheUser, true, "verification "+ref.CreationKey, nil)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	reservation.verificationKey = ref.CreationKey
	s.mu.Unlock()
	return reservation, true, nil
}

func (s *Scheduler) releaseVerificationUser(creationKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for reservation := range s.credentialUsers {
		if reservation.verificationKey == creationKey {
			delete(s.credentialUsers, reservation)
		}
	}
}

// Restore before run/terminal admission or recovery can change cache ownership.
// A malformed published journal is unknown live ownership. Atomic-write
// temporaries cannot own a runtime: creation requires a durable final rename.
func (s *Scheduler) restoreVerificationUsers() error {
	dir := s.verificationBridgeRefDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return readErr
		}
		var ref verificationBridgeRef
		if decodeErr := json.Unmarshal(data, &ref); decodeErr != nil {
			return fmt.Errorf("decode %s: %w", entry.Name(), decodeErr)
		}
		if validateErr := validateVerificationBridgeRef(ref); validateErr != nil {
			return validateErr
		}
		if entry.Name() != verificationBridgeRefName(ref.CreationKey) {
			return errors.New("scheduler: verification user journal name mismatch")
		}
		if _, _, reserveErr := s.reserveVerificationUser(ref); reserveErr != nil {
			return reserveErr
		}
	}
	return nil
}
