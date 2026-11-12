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

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// maxContainerID is the highest uid or gid a container's user namespace
// maps. A file owned above it has no owner inside the container, and
// Sysbox refuses to set up a root filesystem that holds one.
const maxContainerID = 65535

// checkSavedImage reads every entry of the committed image, so a save that
// captured host-shifted owners never becomes the member's image.
func (s *Scheduler) checkSavedImage(ctx context.Context, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, protocol.EnvSaveCheckTimeout)
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

// previousImageLabel names, on a saved image, the image sessions started
// from when it was saved: the one to fall back to. Empty means the standard
// image.
const previousImageLabel = "aether.previous-image"

func isMemberImage(member domain.MemberID, image string) bool {
	return strings.HasPrefix(image, memberImageRepo(member)+":")
}

// previousImage is the fallback recorded on image, or "" when there is none
// or image is gone.
func (s *Scheduler) previousImage(ctx context.Context, image string) (string, error) {
	if image == "" {
		return "", nil
	}
	labels, err := s.cfg.Runtime.ImageLabels(ctx, image)
	if errors.Is(err, runtime.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("scheduler: read the previous image of %s: %w", image, err)
	}
	return labels[previousImageLabel], nil
}

// imageChain lists the images a container of member may start from, in
// order: the saved image and the previous one if it still exists. The
// standard image follows them.
func (s *Scheduler) imageChain(ctx context.Context, member *domain.Member) ([]string, error) {
	if member.Image == "" {
		return nil, nil
	}
	previous, err := s.previousImage(ctx, member.Image)
	if err != nil || previous == "" {
		return []string{member.Image}, err
	}
	exists, err := s.cfg.Runtime.ImageExists(ctx, previous)
	if err != nil {
		return nil, fmt.Errorf("scheduler: check previous image %s: %w", previous, err)
	}
	if !exists {
		return []string{member.Image}, nil
	}
	return []string{member.Image, previous}, nil
}

type failedImage struct{ image, cause string }

// sessionImage is the first image of member's chain no launch has found
// unable to start a container, "" for the standard image, with the notice
// that names the images it skipped.
func (s *Scheduler) sessionImage(ctx context.Context, member *domain.Member) (string, string, error) {
	if s.unstartableCause(member.Image) == "" {
		return member.Image, "", nil
	}
	chain, err := s.imageChain(ctx, member)
	if err != nil {
		return "", "", err
	}
	var failed []failedImage
	for _, image := range chain {
		cause := s.unstartableCause(image)
		if cause == "" {
			return image, imageNotice(failed, image), nil
		}
		failed = append(failed, failedImage{image, cause})
	}
	return "", imageNotice(failed, ""), nil
}

func (s *Scheduler) unstartableCause(image string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unstartableImages[image]
}

// imageNotice tells the person which saved images a container skipped and
// what it uses instead. It must fit a run status reason.
func imageNotice(failed []failedImage, using string) string {
	if len(failed) == 0 {
		return ""
	}
	var names, causes []string
	for _, f := range failed {
		_, name, _ := strings.Cut(f.image, ":")
		names = append(names, name)
		if !slices.Contains(causes, f.cause) {
			causes = append(causes, f.cause)
		}
	}
	instead := "the standard image"
	if using != "" {
		_, name, _ := strings.Cut(using, ":")
		instead = "your previous saved image " + name
	}
	subject, it := "Saved image "+names[0], "it"
	if len(names) > 1 {
		subject, it = "Saved images "+strings.Join(names, " and "), "them"
	}
	return fmt.Sprintf("%s cannot start (%s); this session uses %s. Save your environment again to replace %s.",
		subject, strings.Join(causes, "; "), instead, it)
}

// unstartableImageCause matches the runtime errors of a root filesystem
// that holds owners the container cannot map. A mkdirall under one of
// mounts is a mount's fault, not the image's.
func unstartableImageCause(err error, mounts []string) string {
	if _, setup := errors.AsType[*runtime.SetupError](err); setup {
		return ""
	}
	msg := err.Error()
	if !slices.ContainsFunc([]string{"create container: ", "start container: ", "scheduler: create terminal: ", "scheduler: start terminal: "}, func(prefix string) bool {
		return strings.HasPrefix(msg, prefix)
	}) {
		return ""
	}
	if strings.Contains(msg, "value too large for defined data type") {
		return "value too large for defined data type"
	}
	_, target, ok := strings.Cut(msg, "mkdirall ")
	if !ok || !strings.Contains(target, "permission denied") {
		return ""
	}
	target, _, _ = strings.Cut(target, ":")
	for _, mount := range mounts {
		if strings.Contains(target, strings.TrimSuffix(mount, "/")+"/") {
			return ""
		}
	}
	return "rootfs permission denied"
}

func (s *Scheduler) planMounts(plan *EnvironmentPlan) []string {
	mounts := []string{s.cfg.WorktreeMount}
	for _, m := range slices.Concat(plan.Mounts, plan.Worktrees) {
		mounts = append(mounts, m.ContainerPath)
	}
	return mounts
}

// startFromOlderImages calls start with the next image of member's chain
// while the attempt before failed because its image cannot start a
// container, and returns the plan that started. An image is marked
// unstartable only once a later one starts. When the standard image fails
// the same way the image was not the cause, so member's marks are dropped
// and the error is returned as it is.
func (s *Scheduler) startFromOlderImages(ctx context.Context, member *domain.Member, plan *EnvironmentPlan, err error, canRetry func() bool, start func(*domain.Member) (*EnvironmentPlan, error)) (*EnvironmentPlan, error) {
	var failed []failedImage
	substituted := plan != nil && plan.ImageNotice != ""
	for plan != nil && isMemberImage(member.ID, plan.Image) && canRetry() {
		cause := unstartableImageCause(err, s.planMounts(plan))
		if cause == "" {
			return nil, err
		}
		failed = append(failed, failedImage{plan.Image, cause})
		next, nextErr := s.nextImage(ctx, member, plan.Image)
		if nextErr != nil {
			return nil, errors.Join(err, nextErr)
		}
		slog.Warn("scheduler: saved environment image cannot start a container; trying an older image", "member", member.ID, "image", plan.Image, "next", cmp.Or(next, "standard"), "error", err)
		older := *member
		older.Image = next
		if plan, err = start(&older); err == nil {
			s.mu.Lock()
			for _, f := range failed {
				s.unstartableImages[f.image] = f.cause
			}
			s.mu.Unlock()
			_, notice, noticeErr := s.sessionImage(ctx, member)
			if noticeErr != nil {
				notice = imageNotice(failed, next)
			}
			plan.ImageNotice = notice
			return plan, nil
		}
	}
	if plan != nil && (len(failed) > 0 || substituted) && !isMemberImage(member.ID, plan.Image) && unstartableImageCause(err, s.planMounts(plan)) != "" {
		s.mu.Lock()
		for image := range s.unstartableImages {
			if isMemberImage(member.ID, image) {
				delete(s.unstartableImages, image)
			}
		}
		s.mu.Unlock()
	}
	return nil, err
}

// nextImage is the image of member's chain after image that no launch has
// marked unstartable, or "" for the standard image.
func (s *Scheduler) nextImage(ctx context.Context, member *domain.Member, image string) (string, error) {
	chain, err := s.imageChain(ctx, member)
	if err != nil {
		return "", err
	}
	i := slices.Index(chain, image)
	if i < 0 {
		return "", nil
	}
	for _, next := range chain[i+1:] {
		if s.unstartableCause(next) == "" {
			return next, nil
		}
	}
	return "", nil
}
