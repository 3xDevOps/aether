package sshd

import (
	"cmp"
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func init() {
	registerMethod(protocol.MethodServerRunLimits, (*Server).serverRunLimits)
}

func wireRunLimits(l domain.RunLimits) protocol.RunLimits {
	return protocol.RunLimits{CPUs: l.CPUs, MemoryBytes: l.MemoryBytes, Pids: l.Pids}
}

func (s *Server) serverRunLimits(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.ServerRunLimitsParams](raw)
	if perr != nil {
		return nil, perr
	}
	limits, err := s.cfg.Store.ServerRunLimits(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	if p.CPUs != nil || p.MemoryBytes != nil || p.Pids != nil {
		if perr := s.requireAdmin(ctx, member, protocol.MethodServerRunLimits); perr != nil {
			return nil, perr
		}
		if p.CPUs != nil {
			limits.CPUs = *p.CPUs
		}
		if p.MemoryBytes != nil {
			limits.MemoryBytes = *p.MemoryBytes
		}
		if p.Pids != nil {
			limits.Pids = *p.Pids
		}
		if err := scheduler.ValidateRunBudgets(limits.CPUs, limits.MemoryBytes, limits.Pids); err != nil {
			return nil, invalidParams(err.Error())
		}
		if err := s.cfg.Store.PutServerRunLimits(ctx, limits); err != nil {
			return nil, rpcError(err)
		}
	}
	auto := s.cfg.RunLimits
	return protocol.ServerRunLimits{
		Limits: wireRunLimits(limits),
		Effective: protocol.RunLimits{
			CPUs:        cmp.Or(limits.CPUs, auto.CPUs),
			MemoryBytes: cmp.Or(limits.MemoryBytes, auto.MemoryBytes),
			Pids:        cmp.Or(limits.Pids, auto.Pids),
		},
	}, nil
}
