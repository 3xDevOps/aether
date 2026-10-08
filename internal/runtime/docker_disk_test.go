package runtime

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func TestDockerStorageSharedLayersAndBindVolumes(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("storage report attempted mutation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			_, _ = io.WriteString(w, `{"LayersSize":100,"Images":[{"Id":"a","Size":100,"SharedSize":100,"Containers":1},{"Id":"b","Size":100,"SharedSize":100,"Containers":0}],"Containers":[{"Id":"c","SizeRw":30,"State":"exited"}],"Volumes":[{"Name":"home","Driver":"local","Options":{"device":"/private/member/home","o":"bind","type":"none"},"UsageData":{"Size":400,"RefCount":0}},{"Name":"data","Driver":"local","UsageData":{"Size":50,"RefCount":0}}],"BuildCache":[{"ID":"shared","Size":100,"Shared":true},{"ID":"unique","Size":20,"Shared":false}]}`)
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = io.WriteString(w, `{"DockerRootDir":"/private/docker"}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	cli, err := client.New(client.WithHost("tcp://"+api.Listener.Addr().String()), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	u := (&Docker{cli: cli}).StorageUsage(t.Context(), t.TempDir())
	if u.ImagesBytes == nil || *u.ImagesBytes != 100 || u.BuildCacheBytes == nil || *u.BuildCacheBytes != 20 || u.ContainersBytes == nil || *u.ContainersBytes != 30 || u.VolumesBytes == nil || *u.VolumesBytes != 50 || u.ReclaimableBytes == nil || *u.ReclaimableBytes != 100 {
		t.Fatalf("shared layers or home bind were double counted: %+v", u)
	}
	if u.TotalBytes != nil || u.SharedFilesystem != nil || !strings.Contains(u.Error, "remote daemon") || strings.Contains(u.Error, "/private") {
		t.Fatalf("remote daemon filesystem was guessed or leaked: %+v", u)
	}
}

func TestDockerStorageUnknownVolumeIsNotZero(t *testing.T) {
	u := dockerCategoryUsage(client.DiskUsageResult{Volumes: client.VolumesDiskUsage{TotalCount: 1, Items: []volume.Volume{{Driver: "local"}}}})
	if u.VolumesBytes != nil || u.ReclaimableBytes != nil || u.Error == "" {
		t.Fatalf("unknown volume usage treated as known: %+v", u)
	}
}

func TestDockerStorageUnavailableHasNoInventedTotals(t *testing.T) {
	cli, err := client.New(client.WithHost("unix://"+t.TempDir()+"/absent.sock"), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	u := (&Docker{cli: cli}).StorageUsage(t.Context(), t.TempDir())
	if u.ImagesBytes != nil || u.VolumesBytes != nil || u.UsedBytes != nil || u.ReclaimableBytes != nil || !strings.Contains(u.Error, "connection failed") {
		t.Fatalf("unavailable daemon: %+v", u)
	}
}

func TestDockerStorageExclusiveBuildCacheAcrossAPIs(t *testing.T) {
	for _, total := range []int64{20, 120} {
		usage := client.DiskUsageResult{}
		usage.BuildCache.TotalSize = total
		usage.BuildCache.TotalCount = 2
		usage.BuildCache.Reclaimable = 120
		usage.BuildCache.Items = []build.CacheRecord{{Shared: true, Size: 100}, {Size: 20}}
		got := dockerCategoryUsage(usage)
		if got.BuildCacheBytes == nil || *got.BuildCacheBytes != 20 || got.ReclaimableBytes == nil || *got.ReclaimableBytes != 20 {
			t.Fatalf("aggregate %d double-counted shared cache: %+v", total, got)
		}
		usage.BuildCache.TotalCount = 3
		if got := dockerCategoryUsage(usage); got.BuildCacheBytes != nil || got.ReclaimableBytes != nil || got.Error == "" {
			t.Fatalf("incomplete records yielded known cache: %+v", got)
		}
		usage.BuildCache.TotalCount = 2
		usage.BuildCache.Items[1].Size = -1
		if got := dockerCategoryUsage(usage); got.BuildCacheBytes != nil || got.ReclaimableBytes != nil || got.Error == "" {
			t.Fatalf("unknown size yielded known cache: %+v", got)
		}
	}
}

func TestDockerFilesystemRequiresVerifiedLocalLayerStore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "engine-id"), []byte("local-engine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layers := filepath.Join(root, "overlay2")
	if err := os.Mkdir(layers, 0o700); err != nil {
		t.Fatal(err)
	}
	info := system.Info{ID: "local-engine", DockerRootDir: root, Driver: "overlay2"}
	if got, err := dockerFilesystemRoots(t.Context(), "unix:///daemon.sock", info); err != nil || len(got) != 2 || got[0].path != root || got[1].path != layers {
		t.Fatalf("verified classic store: %v, %v", got, err)
	}
	for _, tc := range []struct {
		name string
		host string
		info system.Info
	}{
		{"forwarded", "unix:///daemon.sock", system.Info{ID: "remote-engine", DockerRootDir: root, Driver: "overlay2"}},
		{"containerd", "unix:///daemon.sock", system.Info{ID: info.ID, DockerRootDir: root, Driver: "overlay2", DriverStatus: [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}}},
		{"unverified driver", "unix:///daemon.sock", system.Info{ID: info.ID, DockerRootDir: root, Driver: "overlayfs"}},
		{"tcp", "tcp://daemon:2375", info},
		{"missing identity", "unix:///daemon.sock", system.Info{ID: info.ID, DockerRootDir: t.TempDir(), Driver: "overlay2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := dockerFilesystemRoots(t.Context(), tc.host, tc.info); err == nil || got != nil {
				t.Fatalf("unverified filesystem accepted: %v, %v", got, err)
			}
		})
	}
}

func TestDockerStorageErrorsRetainCausesWithoutPrivateValues(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "open", Path: "/private/member/credential", Err: os.ErrPermission}, "permission denied"},
		{errors.New("Get \"https://alice:password@example.test/private?token=secret\": permission denied"), "permission denied"},
		{errors.New("API unsupported: client version 1.51 is too old; minimum supported API version is 1.52"), "minimum supported API version is 1.52"},
		{errors.New("daemon failure: dial unix /private/member/daemon.sock: connect: connection refused"), "connection refused"},
		{errors.New("daemon failure: open /private/member/credential: input/output error"), "input"},
		{errors.New("authorization denied for volume 'private-volume'"), "authorization denied"},
	} {
		got := dockerStorageError("disk usage", tc.err)
		if !strings.Contains(got, tc.want) {
			t.Errorf("lost actual error %q: %s", tc.want, got)
		}
		for _, secret := range []string{"alice", "password", "example.test", "token=", "secret", "/private", "credential", "daemon.sock", "private-volume"} {
			if strings.Contains(got, secret) {
				t.Errorf("error leaked %q: %s", secret, got)
			}
		}
	}
}
