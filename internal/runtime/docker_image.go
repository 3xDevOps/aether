package runtime

import (
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
// interactive shell command.
func (d *Docker) Commit(ctx context.Context, id ID, tag string) error {
	_, err := d.cli.ContainerCommit(ctx, string(id), client.ContainerCommitOptions{
		Reference: tag,
		Changes:   []string{"CMD []", "ENTRYPOINT []"},
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

// ImageCacheEnvironment returns only the five tool-cache settings whose image
// defaults must survive managed-cache defaults. It never exports unrelated
// image environment (which may contain credentials). ImageUser has already
// ensured the image is local before the scheduler assembles its plan.
func (d *Docker) ImageCacheEnvironment(ctx context.Context, ref string) (map[string]string, error) {
	info, err := d.cli.ImageInspect(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect image cache environment: %w", err)
	}
	env := make(map[string]string, 5)
	if info.Config != nil {
		for _, pair := range info.Config.Env {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			switch key {
			case "npm_config_cache", "PIP_CACHE_DIR", "UV_CACHE_DIR", "GOCACHE", "GOMODCACHE":
				env[key] = value
			}
		}
	}
	return env, nil
}
