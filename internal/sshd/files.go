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

// FileReader is the SSH server's files seam. The engine resolves identifiers
// to server-owned repository and checkout paths; those paths never enter
// protocol results. Write authorization remains in this package because the
// authenticated actor is not part of the repository engine.
type FileReader interface {
	FilesTree(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, dir string) ([]gitengine.TreeEntry, error)
	FilesRead(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, path string, maxBytes int) (gitengine.FileRead, error)
	FilesWrite(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, path string, content []byte, revision string, author domain.GitIdentity, signingKey []byte) (gitengine.FileRead, error)
	FileDiff(ctx context.Context, run domain.RunID, path string) (gitengine.Patch, error)
}

const filesReadMaxBytes = gitengine.MaxFileBytes

func init() {
	registerGuarded(protocol.MethodFilesTree, permissions.View, filesTarget, (*Server).filesTree)
	registerGuarded(protocol.MethodFilesRead, permissions.View, filesTarget, (*Server).filesRead)
	registerGuarded(protocol.MethodFilesDiff, permissions.View, runTarget, (*Server).filesDiff)
	registerMethod(protocol.MethodFilesWrite, (*Server).filesWrite)
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
func (s *Server) filesRead(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
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
	result, err := reader.FilesRead(ctx, workspace, domain.RunID(p.RunID), ref, p.Path, filesReadMaxBytes)
	if err != nil {
		return nil, filesReadError(protocol.MethodFilesRead, p.RunID, err)
	}
	result.Writable = result.Writable && s.filesWritable(ctx, member, workspace, domain.RunID(p.RunID))
	return protocol.FilesReadResult{
		Content:   string(result.Content),
		Truncated: result.Truncated,
		Binary:    result.Binary,
		Size:      result.Size,
		Revision:  result.Revision,
		Writable:  result.Writable,
	}, nil
}

func (s *Server) filesWritable(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, run domain.RunID) bool {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return false
	}
	if run != "" {
		target, err := resolveRunTarget(ctx, s.cfg.Store, run)
		if err != nil {
			return false
		}
		return permissions.Check(permissions.Steer, actor, target) == nil
	}
	return permissions.Check(permissions.Push, actor, permissions.Target{Workspace: workspace}) == nil
}

func (s *Server) filesWrite(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.FilesWriteParams](params)
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
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.write: file editing is not enabled"}
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	workspace := domain.WorkspaceID(p.WorkspaceID)
	ref := ""
	var target permissions.Target
	cap := permissions.Push
	if p.RunID != "" {
		cap = permissions.Steer
		target, err = resolveRunTarget(ctx, s.cfg.Store, domain.RunID(p.RunID))
		if err != nil {
			return nil, rpcError(err)
		}
	} else {
		ws, workspaceErr := s.cfg.Store.GetWorkspace(ctx, workspace)
		if workspaceErr != nil {
			return nil, rpcError(workspaceErr)
		}
		ref = ws.BaseBranch
		target = permissions.Target{Workspace: workspace}
	}
	if err = permissions.Check(cap, actor, target); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodFilesWrite + ": " + err.Error()}
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	var signingKey []byte
	if s.cfg.Homes != nil {
		signingKey, err = s.cfg.Homes.SigningKey(member)
		if err != nil {
			return nil, rpcError(err)
		}
	}
	result, err := reader.FilesWrite(ctx, workspace, domain.RunID(p.RunID), ref, p.Path, []byte(p.Content), p.Revision, m.GitIdentity(), signingKey)
	if err != nil {
		return nil, filesWriteError(p.RunID, err)
	}
	result.Writable = true
	return protocol.FilesReadResult{
		Content:   string(result.Content),
		Truncated: result.Truncated,
		Binary:    result.Binary,
		Size:      result.Size,
		Revision:  result.Revision,
		Writable:  true,
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
	switch {
	case errors.Is(err, gitengine.ErrInvalidPath):
		return invalidParams(err.Error())
	case errors.Is(err, gitengine.ErrWorkspaceMismatch):
		return invalidParams("workspace_id does not own run_id")
	case errors.Is(err, gitengine.ErrRevisionConflict):
		return &protocol.Error{Code: protocol.CodeConflict, Message: method + ": file changed; reload before saving"}
	}
	if run != "" {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": this run's checkout was removed; pull the branch to see its files"}
	}
	if method == protocol.MethodFilesTree {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": workspace has no repository yet"}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": file could not be read"}
}

func filesWriteError(run string, err error) *protocol.Error {
	switch {
	case errors.Is(err, gitengine.ErrInvalidPath):
		return invalidParams(err.Error())
	case errors.Is(err, gitengine.ErrWorkspaceMismatch):
		return invalidParams("workspace_id does not own run_id")
	case errors.Is(err, gitengine.ErrRevisionConflict):
		return &protocol.Error{Code: protocol.CodeConflict, Message: "files.write: file changed; reload before saving"}
	}
	if run != "" {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.write: this run's checkout was removed; pull the branch to edit its files"}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: "files.write: workspace repository is unavailable"}
}
