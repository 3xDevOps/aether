package runtime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type reclaimRuntime struct {
	Runtime
	spec      Spec
	exitCode  int
	destroyed bool
}

func (r *reclaimRuntime) Create(_ context.Context, spec Spec) (ID, error) {
	r.spec = spec
	return "reclaim", nil
}

func (r *reclaimRuntime) Start(context.Context, ID) error {
	if r.exitCode != 0 {
		return nil
	}
	host := r.spec.Mounts[0].HostPath
	if err := filepath.WalkDir(host, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			err = os.Chmod(path, 0o700)
		}
		return err
	}); err != nil {
		return err
	}
	entries, err := os.ReadDir(host)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(host, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (r *reclaimRuntime) Wait(context.Context, ID) (ExitStatus, error) {
	return ExitStatus{Code: r.exitCode}, nil
}

func (r *reclaimRuntime) Destroy(context.Context, ID) error {
	r.destroyed = true
	return nil
}

func lockedTree(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root removes read-only directories, so the fallback never runs")
	}
	target := filepath.Join(t.TempDir(), "checkout")
	locked := filepath.Join(target, ".git", "objects", "ab")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "object"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	return target
}

func TestRemoverEmptiesPermissionDeniedTreeAsRoot(t *testing.T) {
	target := lockedTree(t)
	rt := &reclaimRuntime{}

	if err := Remover(rt, "aether-standard:test")(t.Context(), target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after remove = %v, want not exist", err)
	}
	want := Spec{
		Image:   "aether-standard:test",
		User:    "0:0",
		Command: []string{"find", "/reclaim", "-mindepth", "1", "-delete"},
		Mounts:  []Mount{{HostPath: target, ContainerPath: "/reclaim"}},
	}
	if rt.spec.Image != want.Image || rt.spec.User != want.User || !slices.Equal(rt.spec.Command, want.Command) || !slices.Equal(rt.spec.Mounts, want.Mounts) {
		t.Fatalf("container spec = %+v, want %+v", rt.spec, want)
	}
	if !rt.destroyed {
		t.Fatal("reclaim container was not destroyed")
	}
}

func TestRemoverReportsPermissionErrorWhenRootContainerFails(t *testing.T) {
	target := lockedTree(t)
	rt := &reclaimRuntime{exitCode: 1}

	err := Remover(rt, "aether-standard:test")(t.Context(), target)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("remove error = %v, want the permission error", err)
	}
	if !rt.destroyed {
		t.Fatal("reclaim container was not destroyed")
	}
}
