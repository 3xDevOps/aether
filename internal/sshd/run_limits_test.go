package sshd

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestServerRunLimits(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.RunLimits = domain.RunLimits{CPUs: 4, MemoryBytes: 8 << 30, Pids: 4096} })
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	cpus, mem := 2.0, int64(1<<30)

	wantDenied(t, controlAs(t, e, bob).Call(protocol.MethodServerRunLimits, protocol.ServerRunLimitsParams{CPUs: &cpus}, nil), "set as a collaborator")
	var got protocol.ServerRunLimits
	if err := controlClient(t, e).Call(protocol.MethodServerRunLimits, protocol.ServerRunLimitsParams{CPUs: &cpus, MemoryBytes: &mem}, &got); err != nil {
		t.Fatalf("set as an admin: %v", err)
	}
	want := protocol.ServerRunLimits{
		Limits:    protocol.RunLimits{CPUs: 2, MemoryBytes: 1 << 30},
		Effective: protocol.RunLimits{CPUs: 2, MemoryBytes: 1 << 30, Pids: 4096},
	}
	if got != want {
		t.Fatalf("after set = %+v, want %+v", got, want)
	}
	if err := controlAs(t, e, bob).Call(protocol.MethodServerRunLimits, struct{}{}, &got); err != nil || got != want {
		t.Fatalf("read as a collaborator = %+v, %v; want %+v", got, err, want)
	}
	negative := int64(-1)
	if err := controlClient(t, e).Call(protocol.MethodServerRunLimits, protocol.ServerRunLimitsParams{Pids: &negative}, nil); err == nil {
		t.Fatal("a negative pid limit was accepted")
	}
}
