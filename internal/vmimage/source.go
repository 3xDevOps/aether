package vmimage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// resolve finds ref in its registry, anonymously as Docker pulls it, or for
// an aether/ reference in Docker. cleanup removes the spooled docker save.
func (s *Store) resolve(ctx context.Context, ref string) (v1.Image, func(), error) {
	if runtime.LocalOnlyImage(ref) {
		return s.fromDocker(ctx, ref)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, nil, fmt.Errorf("vmimage: image reference %s: %w", ref, err)
	}
	img, err := remote.Image(parsed, remote.WithContext(ctx),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: goruntime.GOARCH}))
	if err != nil {
		return nil, nil, fmt.Errorf("vmimage: pull %s: %w", ref, err)
	}
	return img, func() {}, nil
}

func (s *Store) fromDocker(ctx context.Context, ref string) (v1.Image, func(), error) {
	if s.docker == nil {
		return nil, nil, fmt.Errorf("vmimage: image %s is built locally and is not in the store: %w", ref, runtime.ErrNotFound)
	}
	rc, err := s.docker.SaveImage(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	spool, err := os.CreateTemp(s.tmpDir(), "docker-save-*.tar")
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("vmimage: spool %s: %w", ref, err), rc.Close())
	}
	cleanup := func() { _ = os.Remove(spool.Name()) }
	_, err = io.Copy(spool, rc)
	if err = errors.Join(err, rc.Close(), spool.Close()); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("vmimage: read %s from Docker: %w", ref, err)
	}
	img, err := tarball.ImageFromPath(spool.Name(), nil)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("vmimage: read %s from Docker: %w", ref, err)
	}
	return img, cleanup, nil
}

// build makes the base disk id of img and tags it ref, or only tags it when
// the base already exists.
func (s *Store) build(ctx context.Context, img v1.Image, id, ref string) error {
	if tagged, err := s.tagIfExists(ref, id); tagged || err != nil {
		return err
	}
	file, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("read image config: %w", err)
	}
	if file.OS != "linux" || file.Architecture != goruntime.GOARCH {
		return fmt.Errorf("image is for %s/%s, this server runs linux/%s", file.OS, file.Architecture, goruntime.GOARCH)
	}
	work, err := os.MkdirTemp(s.tmpDir(), "base-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	stage := filepath.Join(work, "root")
	if err = os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	if err = extractImage(img, stage); err != nil {
		return err
	}
	if err = mkfs(ctx, filepath.Join(work, "disk.raw"), rootSize, "aether-root", stage); err != nil {
		return err
	}
	if err = os.RemoveAll(stage); err != nil {
		return err
	}
	cfg := ImageConfig{
		User:       file.Config.User,
		Env:        file.Config.Env,
		WorkingDir: file.Config.WorkingDir,
		Labels:     file.Config.Labels,
	}
	_, err = s.commit(work, record{ID: id, Config: cfg}, ref)
	return err
}

func (s *Store) tagIfExists(ref, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(filepath.Join(s.basesDir(), dirName(id)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.tagLocked(ref, id)
}

func extractImage(img v1.Image, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rc := mutate.Extract(img)
	err = extract(rc, root)
	if err == nil {
		// mutate.Extract ends the tar cleanly even when a layer fails and
		// reports the failure after it, so read on to the end.
		_, err = io.Copy(io.Discard, rc)
	}
	return errors.Join(err, rc.Close())
}
