package runtime

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func runtimeInfoDocker(t *testing.T, containerRuntime string, infoStatus func(call int32) (int, system.Info)) (*Docker, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("unix", capacitySocketPath(t, "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, info := infoStatus(calls.Add(1))
		if status != http.StatusOK {
			http.Error(w, "daemon unavailable", status)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	cli, err := client.New(client.WithHost("unix://"+listener.Addr().String()), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &Docker{cli: cli, containerRuntime: containerRuntime}, &calls
}

func TestResolveRuntime(t *testing.T) {
	sysbox := system.Info{Runtimes: map[string]system.RuntimeWithStatus{"runc": {}, runtimeSysbox: {}}}
	for _, tc := range []struct {
		name, option string
		info         system.Info
		want         string
		wantCalls    int32
	}{
		{"auto with sysbox", RuntimeAuto, sysbox, runtimeSysbox, 1},
		{"auto without sysbox", RuntimeAuto, system.Info{Runtimes: map[string]system.RuntimeWithStatus{"runc": {}}}, "", 1},
		{"explicit", "runc", sysbox, "runc", 0},
		{"daemon default", "", sysbox, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, calls := runtimeInfoDocker(t, tc.option, func(int32) (int, system.Info) { return http.StatusOK, tc.info })
			for range 2 {
				got, err := d.resolveRuntime(t.Context())
				if err != nil || got != tc.want {
					t.Fatalf("resolveRuntime() = %q, %v; want %q", got, err, tc.want)
				}
			}
			if calls.Load() != tc.wantCalls {
				t.Errorf("info calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestResolveRuntimeDoesNotCacheInfoError(t *testing.T) {
	d, calls := runtimeInfoDocker(t, RuntimeAuto, func(call int32) (int, system.Info) {
		if call == 1 {
			return http.StatusInternalServerError, system.Info{}
		}
		return http.StatusOK, system.Info{Runtimes: map[string]system.RuntimeWithStatus{runtimeSysbox: {}}}
	})
	if _, err := d.resolveRuntime(t.Context()); err == nil || !strings.Contains(err.Error(), "daemon unavailable") {
		t.Fatalf("first resolveRuntime() error = %v, want the daemon error", err)
	}
	if got, err := d.resolveRuntime(t.Context()); err != nil || got != runtimeSysbox {
		t.Fatalf("second resolveRuntime() = %q, %v; want %q", got, err, runtimeSysbox)
	}
	if calls.Load() != 2 {
		t.Errorf("info calls = %d, want 2", calls.Load())
	}
}

func TestContainerConfigNestedDocker(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	spec := validSpec()
	spec.SetupScript = ""

	cfg, hostCfg := d.containerConfig(spec, "")
	for _, key := range []string{labelSetupScript, labelSetupSentinel, labelNestedDocker, labelSecretEnv} {
		if value, ok := cfg.Labels[key]; !ok || value != "" {
			t.Errorf("label %s = %q, present %v; want an empty value that overrides a saved image's", key, value, ok)
		}
	}
	if hostCfg.Runtime != "" || cfg.Labels[labelNestedDocker] != "" || cfg.Labels[labelSetupSentinel] != "" {
		t.Errorf("plain spec got runtime %q, labels %v", hostCfg.Runtime, cfg.Labels)
	}

	spec.NestedDocker = true
	cfg, hostCfg = d.containerConfig(spec, "runc")
	if hostCfg.Runtime != "runc" || cfg.Labels[labelNestedDocker] != "" || cfg.Labels[labelSetupSentinel] != "" {
		t.Errorf("runc got runtime %q, labels %v; want no dockerd label or gate", hostCfg.Runtime, cfg.Labels)
	}

	cfg, hostCfg = d.containerConfig(spec, runtimeSysbox)
	sentinel := cfg.Labels[labelSetupSentinel]
	if hostCfg.Runtime != runtimeSysbox || cfg.Labels[labelNestedDocker] != "1" || sentinel == "" {
		t.Fatalf("sysbox got runtime %q, labels %v; want dockerd label and gate", hostCfg.Runtime, cfg.Labels)
	}
	if !slices.Equal(cfg.Entrypoint, gateEntrypoint(sentinel)) || !slices.Equal(cfg.Cmd, spec.Command) {
		t.Errorf("Entrypoint %v, Cmd %v; want the gate in front of %v", cfg.Entrypoint, cfg.Cmd, spec.Command)
	}
	if cfg.Labels[labelSetupScript] != "" {
		t.Errorf("setup label = %q, want none", cfg.Labels[labelSetupScript])
	}

	spec.NestedDocker = false
	if _, hostCfg = d.containerConfig(spec, runtimeSysbox); hostCfg.Runtime != "" {
		t.Errorf("non-nested spec got runtime %q", hostCfg.Runtime)
	}
}
