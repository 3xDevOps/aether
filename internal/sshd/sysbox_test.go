package sshd

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type stubSysbox struct{ statuses, installs atomic.Int32 }

func (s *stubSysbox) Status(context.Context) protocol.SysboxStatus {
	s.statuses.Add(1)
	return protocol.SysboxStatus{State: protocol.SysboxOff}
}

func (s *stubSysbox) Install(context.Context) protocol.SysboxStatus {
	s.installs.Add(1)
	return protocol.SysboxStatus{State: protocol.SysboxInstalling, Log: "working"}
}

func TestSysboxMethodsRequireAdmin(t *testing.T) {
	t.Parallel()
	svc := &stubSysbox{}
	e := newTestEnv(t, func(c *Config) { c.Services.Sysbox = svc })
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)

	for _, method := range []string{protocol.MethodServerSysboxStatus, protocol.MethodServerSysboxInstall} {
		wantDenied(t, controlAs(t, e, bob).Call(method, struct{}{}, nil), method+" as a collaborator")
	}
	if svc.statuses.Load()+svc.installs.Load() != 0 {
		t.Fatal("a denied call reached the Sysbox service")
	}

	var got protocol.SysboxStatus
	if err := controlClient(t, e).Call(protocol.MethodServerSysboxInstall, struct{}{}, &got); err != nil {
		t.Fatalf("%s as an admin: %v", protocol.MethodServerSysboxInstall, err)
	}
	if got.State != protocol.SysboxInstalling || got.Log != "working" || svc.installs.Load() != 1 {
		t.Fatalf("install = %+v after %d installs, want the service's status", got, svc.installs.Load())
	}
	if err := controlClient(t, e).Call(protocol.MethodServerSysboxStatus, struct{}{}, &got); err != nil || got.State != protocol.SysboxOff {
		t.Fatalf("status = %+v, %v; want the service's status", got, err)
	}
}
