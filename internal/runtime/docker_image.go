package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const localImageRepo = "aether/"

// LocalOnlyImage reports whether ref names an image Aether built on this
// host, which no registry has.
func LocalOnlyImage(ref string) bool {
	return strings.HasPrefix(ref, localImageRepo)
}

// ImageExists reports whether the daemon holds ref locally, without pulling.
func (d *Docker) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("runtime: inspect image %s: %w", ref, err)
	}
	return true, nil
}

// Commit snapshots a container as a tagged image without inheriting its
// interactive shell command. It keeps the user of the image the container
// was created from, not the unprivileged one it ran as: the image's user
// decides where the home is mounted, which must not move when an
// environment is saved. labels replace the labels of the same keys the
// container inherited from its image.
func (d *Docker) Commit(ctx context.Context, id ID, tag string, labels map[string]string) error {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("runtime: inspect container %s: %w", id, err)
	}
	user, err := d.ImageUser(ctx, info.Container.Image)
	if err != nil {
		return err
	}
	changes := []string{"CMD []", "ENTRYPOINT []", "USER " + cmp.Or(user, "0:0")}
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		changes = append(changes, fmt.Sprintf("LABEL %s=%q", key, labels[key]))
	}
	_, err = d.cli.ContainerCommit(ctx, string(id), client.ContainerCommitOptions{
		Reference: tag,
		Changes:   changes,
	})
	if err != nil {
		return fmt.Errorf("runtime: commit container %s as %s: %w", id, tag, err)
	}
	return nil
}

// ListImageTags returns every local tag whose repository is repo.
func (d *Docker) ListImageTags(ctx context.Context, repo string) ([]string, error) {
	list, err := d.cli.ImageList(ctx, client.ImageListOptions{
		Filters: make(client.Filters).Add("reference", repo+":*"),
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: list images %s: %w", repo, err)
	}
	var tags []string
	for _, summary := range list.Items {
		for _, tag := range summary.RepoTags {
			if strings.HasPrefix(tag, repo+":") {
				tags = append(tags, tag)
			}
		}
	}
	return tags, nil
}

func (d *Docker) TagImage(ctx context.Context, source, target string) error {
	if _, err := d.cli.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target}); err != nil {
		return fmt.Errorf("runtime: tag image %s as %s: %w", source, target, err)
	}
	return nil
}

// SaveImage streams `docker save` of a local image.
func (d *Docker) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	rc, err := d.cli.ImageSave(ctx, []string{ref})
	if err != nil {
		return nil, fmt.Errorf("runtime: save image %s: %w", ref, err)
	}
	return rc, nil
}

// ImageLabels returns the labels of a local image; a missing image is
// ErrNotFound.
func (d *Docker) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	info, err := d.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return nil, fmt.Errorf("runtime: inspect image %s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect image %s: %w", ref, err)
	}
	if info.Config == nil {
		return nil, nil
	}
	return info.Config.Labels, nil
}

// ExportContainer is the container ExportImage reads an image of repo
// through. Exports of one repository must not overlap; the next export, or
// a Destroy of this name, removes one a crash left behind.
func ExportContainer(repo string) ID {
	return ID("aether-image-check-" + strings.ReplaceAll(repo, "/", "-"))
}

// ExportImage streams the filesystem of a local image as a tar, read through
// a container that is never started and is removed on Close.
func (d *Docker) ExportImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	repo, _, _ := strings.Cut(ref, ":")
	name := ExportContainer(repo)
	if err := d.Destroy(ctx, name); err != nil {
		return nil, err
	}
	// A saved image carries the labels of the container it was committed
	// from; an empty creation key keeps FindByCreationKey from finding this
	// container in place of that one.
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: string(name),
		Config: &container.Config{
			Image:  ref,
			Cmd:    []string{"/bin/true"},
			Labels: map[string]string{labelManaged: "true", labelCreationKey: "", labelFingerprint: ""},
		},
		HostConfig: &container.HostConfig{NetworkMode: "none"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: create container to export image %s: %w", ref, err)
	}
	id := ID(created.ID)
	destroy := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return d.Destroy(cleanupCtx, id)
	}
	export, err := d.cli.ContainerExport(ctx, string(id), client.ContainerExportOptions{})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("runtime: export image %s: %w", ref, err), destroy())
	}
	return &imageExport{ReadCloser: export, destroy: destroy}, nil
}

type imageExport struct {
	io.ReadCloser
	destroy func() error
}

func (e *imageExport) Close() error {
	return errors.Join(e.ReadCloser.Close(), e.destroy())
}

// RemoveImage untags a local image. Removing a missing tag is harmless.
func (d *Docker) RemoveImage(ctx context.Context, tag string) error {
	_, err := d.cli.ImageRemove(ctx, tag, client.ImageRemoveOptions{PruneChildren: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: remove image %s: %w", tag, err)
	}
	return nil
}

// ImageCacheEnvironment returns only the tool-cache and global-install
// settings whose image defaults must survive Aether's defaults. It never
// exports unrelated image environment (which may contain credentials).
// ImageUser has already ensured the image is local before the scheduler
// assembles its plan.
func (d *Docker) ImageCacheEnvironment(ctx context.Context, ref string) (map[string]string, error) {
	info, err := d.cli.ImageInspect(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect image cache environment: %w", err)
	}
	env := make(map[string]string)
	if info.Config != nil {
		for _, pair := range info.Config.Env {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			switch key {
			case "npm_config_cache", "PIP_CACHE_DIR", "UV_CACHE_DIR", "GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "YARN_CACHE_FOLDER", "npm_config_store_dir",
				"NPM_CONFIG_PREFIX", "npm_config_prefix", "CARGO_INSTALL_ROOT":
				env[key] = value
			}
		}
	}
	return env, nil
}
