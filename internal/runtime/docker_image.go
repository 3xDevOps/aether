package runtime

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

const localImageRepo = "aether/"

func localOnlyImage(ref string) bool {
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
// environment is saved.
func (d *Docker) Commit(ctx context.Context, id ID, tag string) error {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("runtime: inspect container %s: %w", id, err)
	}
	user, err := d.ImageUser(ctx, info.Container.Image)
	if err != nil {
		return err
	}
	_, err = d.cli.ContainerCommit(ctx, string(id), client.ContainerCommitOptions{
		Reference: tag,
		Changes:   []string{"CMD []", "ENTRYPOINT []", "USER " + cmp.Or(user, "0:0")},
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
