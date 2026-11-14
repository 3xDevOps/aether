package vm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func (r *Runtime) DiskUse(context.Context) (map[runtime.ID]int64, error) {
	return r.vms.diskUse()
}

func (e *engine) diskUse() (map[runtime.ID]int64, error) {
	keys, err := e.keys()
	if err != nil {
		return nil, err
	}
	use := make(map[runtime.ID]int64, len(keys))
	for _, key := range keys {
		rec, err := e.load(key)
		if errors.Is(err, runtime.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		paths := []string{rec.Root.Path, rec.Data.Path, rec.Cache.Path, rec.State.Path}
		for _, c := range rec.CheckoutDisks {
			paths = append(paths, c.Disk.Path)
		}
		var n int64
		for _, p := range paths {
			if p == "" {
				continue
			}
			info, err := os.Stat(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			n += info.Sys().(*syscall.Stat_t).Blocks * 512
		}
		use[runtime.ID(idPrefix+key)] = n
	}
	return use, nil
}
