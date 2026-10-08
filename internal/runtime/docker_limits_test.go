package runtime

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestCreateResourceLimitsSDK(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cpu          float64
		memory, pids int64
		invalid      bool
	}{
		{"unrestricted", 0, 0, 0, false},
		{"fractional", 0.125, 96 << 20, 73, false},
		{"generous", 8, 8 << 30, 4096, false},
		{"negative pid", 1, 64 << 20, -1, true},
		{"nonfinite cpu", math.Inf(1), 64 << 20, 32, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeDockerAPI{}
			srv := api.serve(t, "1.52")
			cli, err := client.New(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithAPIVersion("1.52"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			d := &Docker{cli: cli}
			_, err = d.Create(t.Context(), Spec{Image: "busybox", Command: []string{"true"}, CPULimit: tc.cpu, MemoryLimitBytes: tc.memory, PidsLimit: tc.pids})
			api.mu.Lock()
			defer api.mu.Unlock()
			if tc.invalid {
				if err == nil || len(api.bodies) != 0 {
					t.Fatalf("invalid limit reached Docker: %v, %v", err, api.bodies)
				}
				return
			}
			if err != nil || len(api.bodies) != 1 {
				t.Fatalf("Create = %v, requests %v", err, api.bodies)
			}
			var request struct{ HostConfig container.HostConfig }
			if err := json.Unmarshal([]byte(api.bodies[0]), &request); err != nil {
				t.Fatal(err)
			}
			h := request.HostConfig
			if h.NanoCPUs != int64(tc.cpu*1e9) || h.Memory != tc.memory || h.MemorySwap != tc.memory {
				t.Fatalf("effective CPU/memory/swap = %+v", h.Resources)
			}
			if tc.pids == 0 {
				if h.PidsLimit != nil {
					t.Fatalf("zero PID limit must be omitted: %v", *h.PidsLimit)
				}
			} else if h.PidsLimit == nil || *h.PidsLimit != tc.pids {
				t.Fatalf("PID limit = %v", h.PidsLimit)
			}
			if h.LogConfig.Type != "local" || len(h.LogConfig.Config) != 2 || h.LogConfig.Config["max-size"] != "10m" || h.LogConfig.Config["max-file"] != "3" {
				t.Fatalf("log config = %+v", h.LogConfig)
			}
		})
	}
}
