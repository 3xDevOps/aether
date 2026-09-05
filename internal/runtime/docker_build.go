package runtime

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
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

// RemoveImage removes a local image tag. Removing a missing tag is harmless.
func (d *Docker) RemoveImage(ctx context.Context, tag string) error {
	_, err := d.cli.ImageRemove(ctx, tag, image.RemoveOptions{PruneChildren: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: remove image %s: %w", tag, err)
	}
	return nil
}
