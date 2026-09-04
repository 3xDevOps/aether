package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// FileReader is the SSH server's read-only files seam. The engine resolves
// identifiers to server-owned repository and checkout paths; those paths
// never enter protocol results.
type FileReader interface {
	FilesTree(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, dir string) ([]gitengine.TreeEntry, error)
	FilesRead(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, path string, maxBytes int) ([]byte, bool, bool, error)
	FileDiff(ctx context.Context, run domain.RunID, path string) (gitengine.Patch, error)
}

const filesReadMaxBytes = gitengine.MaxFileBytes

func init() {
	registerGuarded(protocol.MethodFilesTree, permissions.View, filesTarget, (*Server).filesTree)
	registerGuarded(protocol.MethodFilesRead, permissions.View, filesTarget, (*Server).filesRead)
	registerGuarded(protocol.MethodFilesDiff, permissions.View, runTarget, (*Server).filesDiff)
}

// filesTarget chooses the run permission target when a request names a run,
// and otherwise verifies the workspace in the request.
func filesTarget(s *Server, ctx context.Context, params json.RawMessage) (permissions.Target, *protocol.Error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if len(params) != 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return permissions.Target{}, invalidParams("invalid params: " + err.Error())
		}
	}
	if p.RunID != "" {
		return runTarget(s, ctx, params)
	}
	return workspaceTarget(s, ctx, params)
}

func (s *Server) filesTree(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.FilesTreeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if err := gitengine.ValidatePath(p.Path); err != nil {
		return nil, invalidParams(err.Error())
	}
	reader := s.cfg.Services.Files
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.tree: file browsing is not enabled"}
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	ref := ""
	if p.RunID == "" {
		ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
		if err != nil {
			return nil, rpcError(err)
		}
		ref = ws.BaseBranch
	}
	entries, err := reader.FilesTree(ctx, workspace, domain.RunID(p.RunID), ref, p.Path)
	if err != nil {
		return nil, filesReadError(protocol.MethodFilesTree, p.RunID, err)
	}
	out := protocol.FilesTreeResult{Entries: make([]protocol.FilesTreeEntry, 0, len(entries))}
	for _, entry := range entries {
		out.Entries = append(out.Entries, protocol.FilesTreeEntry{
			Name: entry.Name,
			Kind: entry.Kind,
			Size: entry.Size,
		})
	}
	return out, nil
}

func (s *Server) filesRead(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.FilesReadParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if err := gitengine.ValidatePath(p.Path); err != nil {
		return nil, invalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, invalidParams("file path is required")
	}
	reader := s.cfg.Services.Files
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.read: file browsing is not enabled"}
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	ref := ""
	if p.RunID == "" {
		ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
		if err != nil {
			return nil, rpcError(err)
		}
		ref = ws.BaseBranch
	}
	content, truncated, binary, err := reader.FilesRead(ctx, workspace, domain.RunID(p.RunID), ref, p.Path, filesReadMaxBytes)
	if err != nil {
		return nil, filesReadError(protocol.MethodFilesRead, p.RunID, err)
	}
	return protocol.FilesReadResult{
		Content:   string(content),
		Truncated: truncated,
		Binary:    binary,
		Size:      int64(len(content)),
	}, nil
}

func (s *Server) filesDiff(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.FilesDiffParams](params)
	if perr != nil {
		return nil, perr
	}
	if err := gitengine.ValidatePath(p.Path); err != nil {
		return nil, invalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, invalidParams("file path is required")
	}
	reader := s.cfg.Services.Files
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.diff: file browsing is not enabled"}
	}
	patch, err := reader.FileDiff(ctx, domain.RunID(p.RunID), p.Path)
	if err != nil {
		return nil, filesReadError(protocol.MethodFilesDiff, p.RunID, err)
	}
	return protocol.FilesDiffResult{Patch: patch.Text, Truncated: patch.Truncated}, nil
}

func filesReadError(method, run string, err error) *protocol.Error {
	if errors.Is(err, gitengine.ErrInvalidPath) {
		return invalidParams(err.Error())
	}
	if run != "" {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": this run's checkout was removed; pull the branch to see its files"}
	}
	if method == protocol.MethodFilesTree {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": workspace has no repository yet"}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": file could not be read"}
}
