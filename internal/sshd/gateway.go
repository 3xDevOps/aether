package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodRunPatch, (*Server).runPatch)
	registerMethod(protocol.MethodServerDisk, (*Server).serverDisk)
}

// RunPatcher renders a run checkout's diff against its recorded fork
// point. The git engine implements it.
type RunPatcher interface {
	RunPatch(ctx context.Context, run domain.RunID, maxBytes int) (gitengine.Patch, error)
}

// DiskReader reads the data directory's disk usage. disk.Cache implements
// it.
type DiskReader interface {
	Usage() (disk.Usage, error)
}

// runPatchMaxBytes caps the diff text one run.patch reply carries. The
// dashboard renders it inline; a diff past this is downloaded, not read.
const runPatchMaxBytes = 512 << 10

func (s *Server) runPatch(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	patcher := s.cfg.Services.Patch
	if patcher == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.patch: diff rendering is not enabled"}
	}
	id, perr := runIDParams(params)
	if perr != nil {
		return nil, perr
	}
	if _, err := s.cfg.Store.GetRun(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	p, err := patcher.RunPatch(ctx, id, runPatchMaxBytes)
	if err != nil {
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

func (s *Server) serverDisk(_ context.Context, _ domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
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
	return protocol.ServerDiskResult{
		UsedBytes:       usage.UsedBytes,
		TotalBytes:      usage.TotalBytes,
		FreeBytes:       usage.FreeBytes,
		WorktreeBytes:   usage.WorktreeBytes,
		TranscriptBytes: usage.TranscriptBytes,
		DatabaseBytes:   usage.DatabaseBytes,
		RepoBytes:       usage.RepoBytes,
	}, nil
}
