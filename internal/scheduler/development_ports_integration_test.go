//go:build integration

package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationPortListDocker launches a real run whose agent serves one
// port on every interface and one on loopback alone, and reads both through
// dev.port.list.
func TestIntegrationPortListDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.Control = control.New(control.Config{})
		cfg.Harnesses = map[string]HarnessSpec{"fake": {TUIArgs: []string{"sh", "-c",
			"httpd -f -p 0.0.0.0:8000 & httpd -f -p 127.0.0.1:5173 & sleep 3600"}}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "serve two ports", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), runtime.ID(sc.ContainerID)) })

	var ports []protocol.DevPort
	for deadline := time.Now().Add(waitTimeout); len(ports) != 2; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("ports = %+v, want the two the agent serves", ports)
		}
		result, err := e.sched.CallDevelopment(ctx, run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevPortList, json.RawMessage(`{"run_id":"`+string(run.ID)+`"}`), func() error { return nil })
		if err != nil {
			t.Fatalf("dev.port.list: %v", err)
		}
		ports = result.(protocol.DevPortListResult).Ports
	}
	// Busybox applets are links to one binary, so the kernel may name either.
	for i, want := range []protocol.DevPort{{Port: 5173, Address: "127.0.0.1"}, {Port: 8000, Address: "0.0.0.0"}} {
		got := ports[i]
		if got.Port != want.Port || got.Address != want.Address || (got.Process != "httpd" && got.Process != "busybox") {
			t.Fatalf("ports = %+v", ports)
		}
	}
}
