//go:build integration

package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// TestDockerSessionCgroupParent runs a container Create places under the
// session parent beside one created without a parent, as before the change,
// and drives exec, pause, resume and stats on both.
func TestDockerSessionCgroupParent(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	info, err := d.cli.Info(t.Context(), client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := d.cgroupParent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want == "" {
		t.Skipf("docker cgroup driver %q has no session parent", info.Info.CgroupDriver)
	}
	spec := func(name string) Spec {
		return Spec{
			Name:        fmt.Sprintf("it-cgroup-%s-%d", name, time.Now().UnixNano()),
			Image:       testImage,
			CreationKey: fmt.Sprintf("it-cgroup-%s-%d", name, time.Now().UnixNano()),
			Command:     []string{"sleep", "600"},
		}
	}
	session := createContainer(t, d, spec("session"))

	legacySpec := spec("legacy")
	cfg, hostCfg := d.containerConfig(legacySpec, "")
	created, err := d.cli.ContainerCreate(t.Context(), client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg, Name: d.namePrefix + legacySpec.Name})
	if err != nil {
		t.Fatal(err)
	}
	legacy := ID(created.ID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Destroy(ctx, legacy)
	})

	for _, tc := range []struct {
		id     ID
		parent string
	}{{session, want}, {legacy, ""}} {
		if err = d.Start(t.Context(), tc.id); err != nil {
			t.Fatalf("Start(%s): %v", tc.id, err)
		}
		var inspect client.ContainerInspectResult
		inspect, err = d.cli.ContainerInspect(t.Context(), string(tc.id), client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := inspect.Container.HostConfig.CgroupParent
		if got != tc.parent {
			t.Fatalf("HostConfig.CgroupParent = %q, want %q", got, tc.parent)
		}
		var cgroup []byte
		cgroup, err = os.ReadFile("/proc/" + strconv.Itoa(inspect.Container.State.Pid) + "/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("docker inspect HostConfig.CgroupParent %q, /proc/%d/cgroup %q", got, inspect.Container.State.Pid, strings.TrimSpace(string(cgroup)))
		leaf := want + "/" + string(tc.id)
		if info.Info.CgroupDriver == "systemd" {
			leaf = "/" + want + "/docker-" + string(tc.id) + ".scope"
		}
		// The legacy container lands wherever the daemon's default parent is,
		// which may be configured to the same place.
		if tc.parent != "" && !strings.Contains(string(cgroup), leaf+"\n") {
			t.Fatalf("/proc/<pid>/cgroup = %q, want it under %s", cgroup, leaf)
		}

		if err = d.Pause(t.Context(), tc.id); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if err = d.Resume(t.Context(), tc.id); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		code, stdout, stderr, execErr := d.Exec(t.Context(), tc.id, []string{"echo", "ok"}, "")
		if execErr != nil || code != 0 || stdout != "ok\n" {
			t.Fatalf("Exec = %d, %q, %q, %v", code, stdout, stderr, execErr)
		}
	}

	// Other tests' containers can go away mid-sample; only these two count.
	stats, err := d.ContainerStats(t.Context())
	if err != nil {
		t.Logf("ContainerStats: %v", err)
	}
	for _, id := range []ID{session, legacy} {
		found := false
		for _, s := range stats {
			if s.ID == string(id) {
				found = true
				if s.CPUUsageNanos == 0 || s.MemoryBytes == 0 || s.Pids == 0 {
					t.Errorf("stats for %s = %+v, want CPU, memory and pids", id, s)
				}
			}
		}
		if !found {
			t.Errorf("ContainerStats has no entry for %s", id)
		}
	}
}
