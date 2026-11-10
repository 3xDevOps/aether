package scheduler

import (
	"archive/tar"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// maxContainerID is the highest uid or gid a container's user namespace
// maps. A file owned above it has no owner inside the container, and
// Sysbox refuses to set up a root filesystem that holds one.
const maxContainerID = 65535

const savedImageCheckTimeout = 10 * time.Minute

// checkSavedImage reads every entry of the committed image, so a save that
// captured host-shifted owners never becomes the member's image.
func (s *Scheduler) checkSavedImage(ctx context.Context, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, savedImageCheckTimeout)
	defer cancel()
	export, err := s.cfg.Runtime.ExportImage(ctx, tag)
	if err != nil {
		return err
	}
	return errors.Join(checkImageOwners(export), export.Close())
}

// errImageOwners reports image entries whose owner a container cannot map.
// It counts the first three owners it meets; the rest only add to total.
type errImageOwners struct {
	total    int
	owners   map[string]int
	examples []string
}

func (e *errImageOwners) Error() string {
	owners := slices.SortedFunc(maps.Keys(e.owners), func(a, b string) int {
		return cmp.Or(cmp.Compare(e.owners[b], e.owners[a]), cmp.Compare(a, b))
	})
	parts := make([]string, 0, len(owners)+1)
	counted := 0
	for _, owner := range owners {
		parts = append(parts, fmt.Sprintf("%d owned by %s", e.owners[owner], owner))
		counted += e.owners[owner]
	}
	if other := e.total - counted; other > 0 {
		parts = append(parts, fmt.Sprintf("%d by other owners", other))
	}
	return fmt.Sprintf("%d paths are owned by a uid or gid above %d, which no container can map (%s; for example %s)",
		e.total, maxContainerID, strings.Join(parts, ", "), strings.Join(e.examples, ", "))
}

func checkImageOwners(r io.Reader) error {
	bad := &errImageOwners{owners: make(map[string]int)}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read image contents: %w", err)
		}
		if h.Uid <= maxContainerID && h.Gid <= maxContainerID && h.Uid >= 0 && h.Gid >= 0 {
			continue
		}
		bad.total++
		owner := fmt.Sprintf("%d:%d", h.Uid, h.Gid)
		if _, ok := bad.owners[owner]; ok || len(bad.owners) < 3 {
			bad.owners[owner]++
		}
		if len(bad.examples) < 3 {
			bad.examples = append(bad.examples, "/"+strings.TrimSuffix(h.Name, "/"))
		}
	}
	if bad.total > 0 {
		return bad
	}
	return nil
}

// previousImage is the member tag created just before current: the image a
// save replaced. tags are newest first.
func previousImage(tags []string, current string) string {
	i := slices.Index(tags, current)
	if i < 0 || i+1 >= len(tags) {
		return ""
	}
	return tags[i+1]
}

// unstartableImageCause matches the runtime errors of a root filesystem
// that holds owners the container cannot map.
func unstartableImageCause(err error) string {
	if _, setup := errors.AsType[*runtime.SetupError](err); setup {
		return ""
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "create container: ") && !strings.HasPrefix(msg, "start container: ") {
		return ""
	}
	if strings.Contains(msg, "value too large for defined data type") {
		return "value too large for defined data type"
	}
	if strings.Contains(msg, "mkdirall") && strings.Contains(msg, "permission denied") {
		return "root filesystem: permission denied"
	}
	return ""
}

// markUnstartable records that member's saved image failed to start a
// session container with err, and reports whether the launch should be
// planned again, which then avoids it. Every image is marked once, so the
// retries end at the standard image.
func (s *Scheduler) markUnstartable(entry *supervised, member *domain.Member, image string, err error) bool {
	cause := unstartableImageCause(err)
	if cause == "" || !strings.HasPrefix(image, memberImageRepo(member.ID)+":") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.destroyPending || s.unstartableImages[image] != "" {
		return false
	}
	s.unstartableImages[image] = cause
	if entry.userReservation != nil {
		delete(s.credentialUsers, entry.userReservation)
		entry.userReservation = nil
	}
	entry.runUser = ""
	slog.Warn("scheduler: saved environment image cannot start a session; retrying from an older image", "run", entry.runID, "image", image, "error", err)
	return true
}

// sessionImage is the image a session of member starts from and, when it is
// not the member's saved image, the notice that says why.
func (s *Scheduler) sessionImage(ctx context.Context, member *domain.Member) (string, string, error) {
	s.mu.Lock()
	cause := s.unstartableImages[member.Image]
	s.mu.Unlock()
	if cause == "" {
		return member.Image, "", nil
	}
	tags, err := s.cfg.Runtime.ListImageTags(ctx, memberImageRepo(member.ID))
	if err != nil {
		return "", "", fmt.Errorf("scheduler: list saved environment images: %w", err)
	}
	image, from := "", "the standard image"
	if previous := previousImage(tags, member.Image); previous != "" {
		s.mu.Lock()
		usable := s.unstartableImages[previous] == ""
		s.mu.Unlock()
		if usable {
			image, from = previous, "your previous saved image "+previous
		}
	}
	return image, fmt.Sprintf("Your saved environment image cannot start a container (%s), so this session uses %s. Run aether env reset to use the standard image.", cause, from), nil
}
