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

func (s *Server) serverRunLimits(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.ServerRunLimitsParams](raw)
	if perr != nil {
		return nil, perr
	}
	var (
		limits domain.RunLimits
		err    error
	)
	if p.CPUs != nil || p.MemoryBytes != nil || p.Pids != nil {
		if perr := s.requireAdmin(ctx, member, protocol.MethodServerRunLimits); perr != nil {
			return nil, perr
		}
		var cpus float64
		var memory, pids int64
		if p.CPUs != nil {
			cpus = *p.CPUs
		}
		if p.MemoryBytes != nil {
			memory = *p.MemoryBytes
		}
		if p.Pids != nil {
			pids = *p.Pids
		}
		if err = scheduler.ValidateRunBudgets(cpus, memory, pids); err != nil {
			return nil, invalidParams(err.Error())
		}
		limits, err = s.cfg.Store.UpdateServerRunLimits(ctx, p.CPUs, p.MemoryBytes, p.Pids)
	} else {
		limits, err = s.cfg.Store.ServerRunLimits(ctx)
	}
	if err != nil {
		return nil, rpcError(err)
	}
	auto := s.cfg.RunLimits
	return protocol.ServerRunLimits{
		Limits:  protocol.RunLimits{CPUs: limits.CPUs, MemoryBytes: limits.MemoryBytes, Pids: limits.Pids},
		Default: protocol.RunLimits{CPUs: auto.CPUs, MemoryBytes: auto.MemoryBytes, Pids: auto.Pids},
		Effective: protocol.RunLimits{
			CPUs:        cmp.Or(limits.CPUs, auto.CPUs),
			MemoryBytes: cmp.Or(limits.MemoryBytes, auto.MemoryBytes),
			Pids:        cmp.Or(limits.Pids, auto.Pids),
		},
	}, nil
}
