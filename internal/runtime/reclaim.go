package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"
)

const (
	reclaimTimeout     = 2 * time.Minute
	reclaimDestroyWait = 30 * time.Second
	reclaimMountPath   = "/reclaim"
)

// Remover returns an os.RemoveAll for directories that root run containers write
// into. When os.RemoveAll is refused with a permission error and the server is
// not root, it empties the directory from a short-lived root container running
// image, then removes it locally. It warns once per path that still fails.
func Remover(rt Runtime, image string) func(ctx context.Context, path string) error {
	var warned sync.Map
	return func(ctx context.Context, path string) error {
		err := os.RemoveAll(path)
		if err == nil || !errors.Is(err, fs.ErrPermission) || os.Geteuid() == 0 {
			return err
		}
		if rerr := emptyAsRoot(ctx, rt, image, path); rerr != nil {
			err = fmt.Errorf("%w; empty it as root: %w", err, rerr)
		} else if err = os.RemoveAll(path); err == nil {
			warned.Delete(path)
			return nil
		}
		if _, seen := warned.LoadOrStore(path, struct{}{}); !seen {
			slog.Warn("runtime: cannot remove files a root container created", "path", path, "error", err)
		}
		return err
	}
}

func emptyAsRoot(ctx context.Context, rt Runtime, image, path string) error {
	ctx, cancel := context.WithTimeout(ctx, reclaimTimeout)
	defer cancel()
	id, err := rt.Create(ctx, Spec{
		Image:   image,
		User:    "0:0",
		Command: []string{"find", reclaimMountPath, "-mindepth", "1", "-delete"},
		Mounts:  []Mount{{HostPath: path, ContainerPath: reclaimMountPath}},
	})
	if err != nil {
		return err
	}
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reclaimDestroyWait)
		defer cancel()
		_ = rt.Destroy(destroyCtx, id)
	}()
	if err = rt.Start(ctx, id); err != nil {
		return err
	}
	status, err := rt.Wait(ctx, id)
	if err != nil {
		return err
	}
	if status.Code != 0 {
		return fmt.Errorf("runtime: find -delete in %s exited %d", image, status.Code)
	}
	return nil
}
