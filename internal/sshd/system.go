package sshd

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

func init() {
	registerMethod(protocol.MethodSystemStats, (*Server).systemStats)
}

// SystemReader returns host history and the latest container snapshot with
// owners resolved. It must not wait on Docker.
type SystemReader interface {
	Read(ctx context.Context, since time.Time) (sysstats.Snapshot, error)
}

func (s *Server) systemStats(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	reader := s.cfg.Services.System
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "system.stats: the server was not started with system sampling"}
	}
	req, perr := decodeParams[protocol.SystemStatsParams](params)
	if perr != nil {
		return nil, perr
	}
	var since time.Time
	if req.SinceMS > 0 {
		since = time.UnixMilli(req.SinceMS)
	}
	snap, err := reader.Read(ctx, since)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "system.stats: " + err.Error()}
	}
	result := protocol.SystemStatsResult{
		IntervalMS:  sysstats.Interval.Milliseconds(),
		RetentionMS: sysstats.Retention.Milliseconds(),
		Host: protocol.SystemHost{
			CPUCores:         snap.Host.CPUCores,
			MemoryTotalBytes: snap.Host.MemoryTotalBytes,
			SwapTotalBytes:   snap.Host.SwapTotalBytes,
			UptimeSeconds:    snap.Host.UptimeSeconds,
		},
		HostError:       snap.HostError,
		Samples:         make([]protocol.SystemHostSample, 0, len(snap.Samples)),
		ContainersError: snap.ContainersError,
		Containers:      []protocol.SystemContainer{},
	}
	for _, h := range snap.Samples {
		result.Samples = append(result.Samples, protocol.SystemHostSample{
			TMS: h.At.UnixMilli(), CPUUser: h.CPUUser, CPUSystem: h.CPUSystem, CPUIOWait: h.CPUIOWait, CPUSteal: h.CPUSteal,
			MemoryUsedBytes: h.MemoryUsedBytes, MemoryAvailableBytes: h.MemoryAvailableBytes,
			MemoryFreeBytes: h.MemoryFreeBytes, SwapUsedBytes: h.SwapUsedBytes,
			Load1: h.Load1, Load5: h.Load5, Load15: h.Load15,
			NetRxBps: h.NetRxBps, NetTxBps: h.NetTxBps, DiskReadBps: h.DiskReadBps, DiskWriteBps: h.DiskWriteBps,
			PressureCPU: h.PressureCPU, PressureMemory: h.PressureMemory, PressureIO: h.PressureIO,
		})
	}
	if !snap.ContainersAt.IsZero() {
		result.ContainersAtMS = snap.ContainersAt.UnixMilli()
	}
	admin := s.requireAdmin(ctx, member, protocol.MethodSystemStats) == nil
	var others protocol.SystemOtherContainers
	for _, c := range snap.Containers {
		if c.Owner.Kind == "" || (!admin && c.Owner.MemberID != string(member)) {
			others.Containers++
			others.MemoryBytes += c.MemoryBytes
			if c.Rates != nil {
				others.CPUCores += c.Rates.CPUCores
			}
			continue
		}
		result.Containers = append(result.Containers, systemContainer(c))
	}
	if others.Containers > 0 {
		result.Others = &others
	}
	slices.SortStableFunc(result.Containers, func(a, b protocol.SystemContainer) int {
		return cmp.Or(cmp.Compare(deref(b.CPUCores), deref(a.CPUCores)), cmp.Compare(b.MemoryBytes, a.MemoryBytes))
	})
	return result, nil
}

func systemContainer(c sysstats.ContainerUsage) protocol.SystemContainer {
	out := protocol.SystemContainer{
		OwnerKind: c.Owner.Kind, OwnerID: c.Owner.ID, MemberID: c.Owner.MemberID,
		WorkspaceID: c.Owner.WorkspaceID, Title: c.Owner.Title, Status: c.Owner.Status,
		CPULimitCores: c.CPULimitCores, MemoryBytes: c.MemoryBytes, MemoryLimitBytes: c.MemoryLimitBytes,
		Pids: c.Pids, PidsLimit: c.PidsLimit,
	}
	if r := c.Rates; r != nil {
		out.CPUCores, out.CPUThrottledPercent = &r.CPUCores, r.ThrottledPercent
		out.NetRxBps, out.NetTxBps, out.DiskReadBps, out.DiskWriteBps = &r.NetRxBps, &r.NetTxBps, &r.DiskReadBps, &r.DiskWriteBps
	}
	return out
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
