package runtime

import (
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestCreateCgroupParentFollowsDriver(t *testing.T) {
	for _, tc := range []struct{ driver, want string }{
		{"systemd", "aether.slice"},
		{"cgroupfs", "/aether"},
		{"none", ""},
		{"", ""},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			api := &fakeDockerAPI{cgroupDriver: tc.driver}
			srv := api.serve(t, "1.52")
			cli, err := client.New(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithAPIVersion("1.52"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			d := &Docker{cli: cli}
			for range 2 {
				if _, err := d.Create(t.Context(), Spec{Image: "busybox", Command: []string{"true"}}); err != nil {
					t.Fatal(err)
				}
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.infos != 1 {
				t.Errorf("info requests = %d, want 1", api.infos)
			}
			for _, body := range api.bodies {
				var request struct{ HostConfig container.HostConfig }
				if err := json.Unmarshal([]byte(body), &request); err != nil {
					t.Fatal(err)
				}
				if got := request.HostConfig.CgroupParent; got != tc.want {
					t.Errorf("CgroupParent = %q, want %q", got, tc.want)
				}
			}
		})
	}
}
