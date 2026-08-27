package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/pkg/jsonmessage"
)

// localImageRepo is the repository prefix of images the server builds
// itself (aether/ws-<workspace-id>:<version>). These tags exist only on
// the local daemon: they are never pulled from a registry, and a missing
// one means the workspace image must be rebuilt from its stored
// definition.
const localImageRepo = "aether/"

// localOnlyImage reports whether ref names a locally built Aether image.
// Registry-qualified references (ghcr.io/aether/...) do not match: the
// bare aether/ repository is reserved for the server's own builds.
func localOnlyImage(ref string) bool {
	return strings.HasPrefix(ref, localImageRepo)
}

// BuildImage implements Runtime against the Docker daemon. The build
// context sent to the daemon is a tar holding only the Dockerfile.
func (d *Docker) BuildImage(ctx context.Context, dockerfile, tag string, progress io.Writer) error {
	resp, err := d.cli.ImageBuild(ctx, dockerfileTar(dockerfile), build.ImageBuildOptions{
		Tags:        []string{tag},
		Dockerfile:  "Dockerfile",
		Remove:      true,
		ForceRemove: true,
		Labels:      map[string]string{labelManaged: "true"},
	})
	if err != nil {
		return fmt.Errorf("runtime: build image %s: %w", tag, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := streamBuildProgress(resp.Body, progress); err != nil {
		return fmt.Errorf("runtime: build image %s: %w", tag, err)
	}
	return nil
}

// dockerfileTar wraps the Dockerfile text in the single-file tar archive
// the daemon expects as a build context.
func dockerfileTar(dockerfile string) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}
	// Writes to a bytes.Buffer cannot fail; the sizes are consistent by
	// construction.
	if err := tw.WriteHeader(hdr); err != nil {
		panic(err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	return &buf
}

// streamBuildProgress relays the daemon's JSON progress stream to
// progress line by line and surfaces the daemon's error detail when the
// build fails.
func streamBuildProgress(body io.Reader, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	dec := json.NewDecoder(body)
	for {
		var msg jsonmessage.JSONMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode daemon progress: %w", err)
		}
		if msg.Error != nil {
			return fmt.Errorf("daemon build failed: %w", msg.Error)
		}
		line := msg.Stream
		if line == "" && msg.Status != "" {
			line = msg.Status + "\n"
		}
		if line == "" {
			continue
		}
		if _, err := io.WriteString(progress, line); err != nil {
			return fmt.Errorf("write build progress: %w", err)
		}
	}
}

// RemoveImage implements Runtime. Removing a tag the daemon does not
// know is not an error, so retention passes are idempotent.
func (d *Docker) RemoveImage(ctx context.Context, tag string) error {
	_, err := d.cli.ImageRemove(ctx, tag, image.RemoveOptions{PruneChildren: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: remove image %s: %w", tag, err)
	}
	return nil
}
