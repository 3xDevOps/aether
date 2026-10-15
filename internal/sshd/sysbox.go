package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodServerSysboxStatus, (*Server).sysboxStatus)
	registerMethod(protocol.MethodServerSysboxInstall, (*Server).sysboxInstall)
}

// SysboxService installs the Sysbox runtime on the server host.
type SysboxService interface {
	// Status reports whether Docker has Sysbox and how the last install went.
	Status(ctx context.Context) protocol.SysboxStatus
	// Install starts the installer in the background, at most one at a time.
	Install(ctx context.Context) protocol.SysboxStatus
}

func (s *Server) sysbox(ctx context.Context, member domain.MemberID, method string) (SysboxService, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, method); err != nil {
		return nil, err
	}
	if s.cfg.Services.Sysbox == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "installing Sysbox is not available on this server"}
	}
	return s.cfg.Services.Sysbox, nil
}

func (s *Server) sysboxStatus(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.sysbox(ctx, member, protocol.MethodServerSysboxStatus)
	if perr != nil {
		return nil, perr
	}
	return svc.Status(ctx), nil
}

func (s *Server) sysboxInstall(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.sysbox(ctx, member, protocol.MethodServerSysboxInstall)
	if perr != nil {
		return nil, perr
	}
	return svc.Install(ctx), nil
}
