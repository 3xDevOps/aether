package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/serverupdate"
)

func init() {
	registerMethod(protocol.MethodServerUpdate, (*Server).serverUpdate)
	registerMethod(protocol.MethodServerUpdateStatus, (*Server).serverUpdateStatus)
}

// ServerUpdateService is the control channel's view of the server
// self-update service (*serverupdate.Service).
type ServerUpdateService interface {
	// Status reports the running version, the newest release, whether the
	// server can update itself, and any pending or last update.
	Status(ctx context.Context) (protocol.ServerUpdateStatusResult, error)
	// Update applies or schedules an update. The returned restart, when
	// non-nil, re-executes the server and never returns; the caller runs
	// it only once the response has reached the client.
	Update(ctx context.Context, actor domain.MemberID, p protocol.ServerUpdateParams) (protocol.ServerUpdateResult, func(), error)
}

func (s *Server) updates() (ServerUpdateService, *protocol.Error) {
	if s.cfg.Services.ServerUpdate == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "server self-update is not available on this server"}
	}
	return s.cfg.Services.ServerUpdate, nil
}

// serverUpdateStatus is readable by any member: a collaborator seeing the
// dashboard's "server is behind" banner should be told whether an admin
// can press the button or has to run the commands on the host.
func (s *Server) serverUpdateStatus(ctx context.Context, _ domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.updates()
	if perr != nil {
		return nil, perr
	}
	out, err := svc.Status(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	return out, nil
}

// serverUpdate replaces the server's binaries and restarts onto them. The
// role is re-read from the store on every call, like member.role, so a
// demotion lands on connections that are already open.
func (s *Server) serverUpdate(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, protocol.MethodServerUpdate); err != nil {
		return nil, err
	}
	svc, perr := s.updates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ServerUpdateParams](params)
	if perr != nil {
		return nil, perr
	}
	res, restart, err := svc.Update(ctx, member, p)
	if err != nil {
		return nil, serverUpdateError(err)
	}
	if restart != nil {
		// The binaries are already swapped. Restarting here would drop
		// this connection before the client could read the result, so it
		// waits until the response line has been written.
		if !deferUntilResponded(ctx, restart) {
			slog.Warn("sshd: server.update has no response hook; restarting immediately")
			restart()
		}
	}
	return res, nil
}

// serverUpdateError maps the self-update refusals onto wire codes. An
// unprivileged server is CodeInvalidState - the call is well-formed and
// becomes possible again once the install is changed - and its message
// carries the commands to run on the host, which server.update_status
// also returns as manual_commands.
func serverUpdateError(err error) *protocol.Error {
	switch {
	case errors.Is(err, serverupdate.ErrIncapable):
		return &protocol.Error{
			Code:    protocol.CodeInvalidState,
			Message: err.Error() + "; on the server host run: " + joinCommands(serverupdate.ManualCommands()),
		}
	case errors.Is(err, serverupdate.ErrBadTag), errors.Is(err, serverupdate.ErrBadWhen):
		return invalidParams(err.Error())
	case errors.Is(err, serverupdate.ErrBusy):
		return &protocol.Error{Code: protocol.CodeConflict, Message: err.Error()}
	}
	return rpcError(err)
}

func joinCommands(cmds []string) string {
	out := ""
	for i, c := range cmds {
		if i > 0 {
			out += ", then "
		}
		out += "`" + c + "`"
	}
	return out
}
