package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// checkLifetime bounds an image check's VM, which a server crash would
// otherwise leave running.
const checkLifetime = 10 * time.Minute

// base is the store's base disk tagged ref, if it has one. The store holds
// every image a VM booted from and every Save environment; Docker holds the
// rest, such as environments saved before the server ran VMs.
func (r *Runtime) base(ref string) (vmimage.Base, bool, error) {
	b, err := r.vms.cfg.Images.LookupBase(ref)
	if errors.Is(err, runtime.ErrNotFound) {
		return vmimage.Base{}, false, nil
	}
	return b, err == nil, err
}

func (r *Runtime) ImageExists(ctx context.Context, ref string) (bool, error) {
	if _, ok, err := r.base(ref); ok || err != nil {
		return ok, err
	}
	return r.Docker.ImageExists(ctx, ref)
}

func (r *Runtime) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	if b, ok, err := r.base(ref); ok || err != nil {
		return b.Config.Labels, err
	}
	return r.Docker.ImageLabels(ctx, ref)
}

func (r *Runtime) ListImageTags(ctx context.Context, repo string) ([]string, error) {
	tags, err := r.vms.cfg.Images.Tags(repo)
	if err != nil {
		return nil, err
	}
	docker, err := r.Docker.ListImageTags(ctx, repo)
	if err != nil {
		return nil, err
	}
	tags = append(tags, docker...)
	slices.Sort(tags)
	return slices.Compact(tags), nil
}

func (r *Runtime) TagImage(ctx context.Context, source, target string) error {
	if _, ok, err := r.base(source); err != nil {
		return err
	} else if ok {
		return r.vms.cfg.Images.Tag(source, target)
	}
	return r.Docker.TagImage(ctx, source, target)
}

// RemoveImage untags ref in both stores; the image store then deletes the
// base disks no tag and no VM uses.
func (r *Runtime) RemoveImage(ctx context.Context, tag string) error {
	return errors.Join(r.vms.cfg.Images.Untag(tag), r.Docker.RemoveImage(ctx, tag))
}

// ImageUser reads the base disk VMs boot from, building it on first use.
func (r *Runtime) ImageUser(ctx context.Context, ref string) (string, error) {
	b, err := r.vms.cfg.Images.BaseDisk(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("vm runtime: image %s: %w", ref, err)
	}
	return b.Config.User, nil
}

func (r *Runtime) ImageCacheEnvironment(ctx context.Context, ref string) (map[string]string, error) {
	b, err := r.vms.cfg.Images.BaseDisk(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("vm runtime: image %s: %w", ref, err)
	}
	return runtime.CacheEnvironment(b.Config.Env), nil
}

// CheckImage boots ref in a VM with no network and runs a shell in it as
// user, which is what every session and terminal does first. Each image
// repository has one check VM, so a check a crash left behind is removed by
// the next.
func (r *Runtime) CheckImage(ctx context.Context, ref, user string) (err error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("vm runtime: check image %s: %w", ref, err)
	}
	sum := sha256.Sum256([]byte(parsed.Context().Name()))
	key := "check-" + hex.EncodeToString(sum[:8])
	if err = r.vms.destroy(ctx, key); err != nil {
		return err
	}
	// In /: the image's own directory may be the member's home, which the
	// check does not mount.
	spec := runtime.Spec{Image: ref, Command: []string{"/bin/sh", "-c", "exit 0"}, WorkingDir: "/", User: user, CPULimit: 1, MemoryLimitBytes: 1 << 30}
	if _, err = r.vms.create(ctx, key, spec, options{Offline: true, Lifetime: checkLifetime}); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerOffTimeout)
		defer cancel()
		err = errors.Join(err, r.vms.destroy(cleanup, key))
	}()
	// A boot that fails because of the image is ErrUnstartableImage already;
	// any other failure is the host's and says so.
	if err = r.vms.start(ctx, key); err != nil {
		return err
	}
	st, err := r.vms.wait(ctx, key)
	if err == nil && st.Code != 0 {
		err = fmt.Errorf("vm runtime: a shell in the checked image exited %d: %w", st.Code, runtime.ErrUnstartableImage)
	}
	return err
}

func (r *Runtime) Commit(ctx context.Context, id runtime.ID, tag string, labels map[string]string) error {
	if key, ok := vmKey(id); ok {
		return r.vms.commit(ctx, key, tag, labels)
	}
	return r.Docker.Commit(ctx, id, tag, labels)
}
