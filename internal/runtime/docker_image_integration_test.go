//go:build integration

package runtime

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestCommittedImageExportsStoredOwnersAndLabels(t *testing.T) {
	d := newTestDocker(t)
	ctx := t.Context()
	id := createContainer(t, d, Spec{Name: fmt.Sprintf("it-image-%d", time.Now().UnixNano()), Image: testImage, User: "0:0", Command: []string{"sleep", "3600"}})
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, err := d.Exec(ctx, id, []string{"/bin/sh", "-ec", "touch /shifted && chown 231072:231072 /shifted"}, "/")
	if err != nil || code != 0 {
		t.Fatalf("chown: exit %d, %v: %s", code, err, stderr)
	}
	pending, saved := "aether-image-test-pending:one", "aether-image-test:one"
	if err := d.Commit(ctx, id, pending, map[string]string{"aether.previous-image": "aether-image-test:zero"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.RemoveImage(context.Background(), pending)
		_ = d.RemoveImage(context.Background(), saved)
	})
	if err := d.TagImage(ctx, pending, saved); err != nil {
		t.Fatal(err)
	}
	labels, err := d.ImageLabels(ctx, saved)
	if err != nil || labels["aether.previous-image"] != "aether-image-test:zero" {
		t.Fatalf("ImageLabels = %v, %v, want the committed label", labels, err)
	}
	if _, err := d.ImageLabels(ctx, "aether-image-test:missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ImageLabels of a missing image = %v, want ErrNotFound", err)
	}

	export, err := d.ExportImage(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	var owner []int
	tr := tar.NewReader(export)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "shifted" {
			owner = []int{h.Uid, h.Gid}
		}
	}
	if err := export.Close(); err != nil {
		t.Fatal(err)
	}
	if len(owner) != 2 || owner[0] != 231072 || owner[1] != 231072 {
		t.Fatalf("exported owner of /shifted = %v, want 231072:231072", owner)
	}
	if _, err := d.cli.ContainerInspect(ctx, string(ExportContainer("aether-image-test")), client.ContainerInspectOptions{}); err == nil {
		t.Fatal("the export container outlived Close")
	}
}
