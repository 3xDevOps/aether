package scheduler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
)

// recordingCoordinator is the fake coordinator plus the config bytes each
// run was provisioned with - where the harness MCP registration lands.
type recordingCoordinator struct {
	fakeCoordinator

	mu      sync.Mutex
	configs map[domain.RunID][]byte
}

func (r *recordingCoordinator) Provision(ctx context.Context, run domain.RunID, config []byte) (string, error) {
	r.mu.Lock()
	r.configs[run] = config
	r.mu.Unlock()
	return r.fakeCoordinator.Provision(ctx, run, config)
}

func (r *recordingCoordinator) config(run domain.RunID) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configs[run]
}

// TestHarnessMCPRegistration is the launch-time half of the registration
// contract: a harness whose profile carries the flag is given a config
// naming the staged bridge and the argument pointing at it, and one that
// does not is launched untouched - notice-only, with no config written.
func TestHarnessMCPRegistration(t *testing.T) {
	fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, nil)
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		configs:         make(map[domain.RunID][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))

	registered, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "add OAuth login", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch claude run: %v", err)
	}
	argv := e.rt.byName(string(registered.ID)).spec.Command
	if n := len(argv); n < 2 || argv[n-2] != "--mcp-config" || argv[n-1] != mcpConfigPath {
		t.Fatalf("registered harness argv = %v, want it to end with --mcp-config %s", argv, mcpConfigPath)
	}

	var doc struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(coord.config(registered.ID), &doc); err != nil {
		t.Fatalf("decode written MCP config: %v", err)
	}
	srv, ok := doc.Servers[mcpbridge.ServerName]
	if !ok || srv.Type != "stdio" || srv.Command != mcpbridge.BinaryPath || !slices.Equal(srv.Args, []string{bridgeSubcommand}) {
		t.Fatalf("MCP config = %+v, want a stdio %s server running %s %s",
			doc.Servers, mcpbridge.ServerName, mcpbridge.BinaryPath, bridgeSubcommand)
	}

	unsupported, _ := e.launchFake(t, "fix the auth bug")
	argv = e.rt.byName(string(unsupported.ID)).spec.Command
	if slices.Contains(argv, "--mcp-config") {
		t.Fatalf("unsupported harness argv = %v, want no MCP registration", argv)
	}
	if cfg := coord.config(unsupported.ID); cfg != nil {
		t.Fatalf("unsupported harness had an MCP config written: %s", cfg)
	}
}
