package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// ErrTerminalNotRunning reports that an environment save needs an open terminal.
var ErrTerminalNotRunning = errors.New("terminal: environment terminal is not running; open it first")

func memberImageRepo(member domain.MemberID) string {
	return "aether/member-" + string(member)
}

// SaveEnvironment snapshots the environment terminal, starting it if needed,
// and records its image for the member.
func (s *Scheduler) SaveEnvironment(ctx context.Context, member domain.MemberID) (string, error) {
	if _, err := s.EnsureTerminal(ctx, member); err != nil {
		return "", err
	}
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

// ResetEnvironment destroys the environment terminal's container, unsaved changes included, and returns the member to the standard image.
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
	if _, err := s.destroyKeptTerminalLocked(ctx, member, false); err != nil {
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
		// A kept container pins the tag it was created from; Docker refuses to
		// remove that tag while the container exists.
		if cid, err := s.cfg.Runtime.FindByCreationKey(ctx, terminalCreationKey(member.ID)); err == nil {
			info, inspectErr := s.cfg.Runtime.Inspect(ctx, cid)
			if inspectErr != nil && !errors.Is(inspectErr, runtime.ErrNotFound) {
				return nil, inspectErr
			}
			refs[info.Image] = true
		} else if !errors.Is(err, runtime.ErrNotFound) {
			return nil, err
		}
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

// sweepStandardImages untags every tag of Aether's standard image repository
// that nothing references, so the images a server upgrade replaced do not stay
// on disk. Only the default repository is swept: a custom --standard-image may
// share its repository with images Aether does not own. Docker refuses to
// remove an image a container uses; that tag is kept until a later sweep.
func (s *Scheduler) sweepStandardImages(ctx context.Context) {
	repo := imageRepo(s.cfg.DefaultStandardImage)
	if repo == "" {
		return
	}
	refs, err := s.currentImageReferences(ctx)
	if err != nil {
		slog.Warn("scheduler: list standard image references", "error", err)
		return
	}
	tags, err := s.cfg.Runtime.ListImageTags(ctx, repo)
	if err != nil {
		slog.Warn("scheduler: list standard image tags", "repo", repo, "error", err)
		return
	}
	for _, tag := range tags {
		if refs[tag] {
			continue
		}
		if err := s.cfg.Runtime.RemoveImage(ctx, tag); err != nil && !cerrdefs.IsConflict(err) {
			slog.Warn("scheduler: remove unused standard image", "tag", tag, "error", err)
		}
	}
}

// imageRepo returns the repository of a tagged image reference, or "" for a
// reference with no tag or a digest.
func imageRepo(ref string) string {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.ContainsAny(ref[i:], "/@") || strings.Contains(ref, "@") {
		return ""
	}
	return ref[:i]
}
