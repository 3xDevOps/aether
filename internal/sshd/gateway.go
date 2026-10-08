package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodRunPatch, permissions.View, runTarget, (*Server).runPatch)
	registerMethod(protocol.MethodServerDisk, (*Server).serverDisk)
}

// RunPatcher renders a run checkout's diff: against its recorded fork
// point, or between two diff-snapshot trees. The git engine implements it.
type RunPatcher interface {
	RunPatch(ctx context.Context, run domain.RunID, req gitengine.PatchRequest) (gitengine.Patch, error)
}

// DiskReader reads the data directory's disk usage. disk.Cache implements
// it.
type DiskReader interface {
	Usage() (disk.Usage, error)
}

func (s *Server) runPatch(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	patcher := s.cfg.Services.Patch
	if patcher == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.patch: diff rendering is not enabled"}
	}
	req, perr := decodeParams[protocol.RunPatchParams](params)
	if perr != nil {
		return nil, perr
	}
	if req.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	id := domain.RunID(req.RunID)
	if _, err := s.cfg.Store.GetRun(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	p, err := patcher.RunPatch(ctx, id, gitengine.PatchRequest{
		From:     req.From,
		To:       req.To,
		MaxBytes: gitengine.MaxPatchBytes,
	})
	switch {
	case errors.Is(err, gitengine.ErrInvalidObjectID):
		return nil, invalidParams("run.patch: from and to must both be tree ids taken from a run.diff event (full lowercase hex, naming a tree and not a commit), or both empty")
	case errors.Is(err, gitengine.ErrSnapshotTreeMissing):
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.patch: retained snapshot history has expired or is unavailable"}
	case errors.Is(err, gitengine.ErrSnapshotStorageLimit):
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.patch: current diff input exceeds 128 MiB or available disk headroom"}
	case err != nil:
		// The wrapped error names checkout paths on the server, so it is
		// not echoed to the client.
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.patch: this run has no checkout to diff"}
	}
	return protocol.RunPatchResult{
		RunID:     string(id),
		Base:      p.Base,
		Patch:     p.Text,
		Truncated: p.Truncated,
	}, nil
}

func (s *Server) serverDisk(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	reader := s.cfg.Services.Disk
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "server.disk: the server was not told where the data directory is"}
	}
	usage, err := reader.Usage()
	if err != nil {
		// The wrapped error names the data-directory path, so it is not
		// echoed to the client.
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "server.disk: the data directory's filesystem could not be read"}
	}
	result := protocol.ServerDiskResult{
		UsedBytes:       usage.UsedBytes,
		TotalBytes:      usage.TotalBytes,
		FreeBytes:       usage.FreeBytes,
		WorktreeBytes:   usage.WorktreeBytes,
		TranscriptBytes: usage.TranscriptBytes,
		DatabaseBytes:   usage.DatabaseBytes,
		RepoBytes:       usage.RepoBytes,
		HomeBytes:       usage.HomeBytes,
		CacheBytes:      usage.CacheBytes,
		EvidenceBytes:   usage.EvidenceBytes,
		OtherBytes:      usage.OtherBytes,
		SnapshotBytes:   usage.SnapshotBytes,
		Warnings:        usage.Warnings,
		Docker:          (*protocol.ServerDockerDisk)(usage.Docker),
	}
	if s.requireAdmin(ctx, member, protocol.MethodServerDisk) == nil {
		result.Truncated = usage.Truncated || len(usage.Entries) > 50
		for _, entry := range usage.Entries[:min(len(usage.Entries), 50)] {
			wire := protocol.ServerDiskEntry{
				Kind: entry.Kind, OwnerKind: entry.OwnerKind, OwnerID: entry.OwnerID, Pool: entry.Pool,
				Bytes: entry.Bytes, ReclaimableBytes: entry.ReclaimableBytes,
				Reason: entry.Reason, Error: entry.Error,
			}
			if entry.RetainedUntil != nil {
				wire.RetainedUntil = entry.RetainedUntil.UTC().Format(time.RFC3339Nano)
			}
			result.Entries = append(result.Entries, wire)
		}
	}
	return result, nil
}
