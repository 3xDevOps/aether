package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodHomeTree, permissions.Launch, nil, (*Server).homeTree)
	registerGuarded(protocol.MethodHomeRead, permissions.Launch, nil, (*Server).homeRead)
	registerGuarded(protocol.MethodHomeWrite, permissions.Launch, nil, (*Server).homeWrite)
	registerGuarded(protocol.MethodHomeMkdir, permissions.Launch, nil, (*Server).homeMkdir)
	registerGuarded(protocol.MethodHomeRename, permissions.Launch, nil, (*Server).homeRename)
	registerGuarded(protocol.MethodHomeRemove, permissions.Launch, nil, (*Server).homeRemove)
	registerGuarded(protocol.MethodHomeUpload, permissions.Launch, nil, (*Server).homeUpload)
	registerGuarded(protocol.MethodHomeDownload, permissions.Launch, nil, (*Server).homeDownload)
}

// homeCall decodes one home.* request and runs it against the caller's own
// home; no request field selects another member. Messages carry no method
// prefix: the HTTP client adds the method it called.
func homeCall[P, R any](s *Server, params json.RawMessage, op func(*memberhome.Manager, P) (R, error)) (any, *protocol.Error) {
	p, perr := decodeParams[P](params)
	if perr != nil {
		return nil, perr
	}
	if s.cfg.Homes == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "persistent homes are not enabled"}
	}
	result, err := op(s.cfg.Homes, p)
	if err == nil {
		return result, nil
	}
	var refusal *memberhome.HomeError
	if !errors.As(err, &refusal) {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: configFailureDetail(err)}
	}
	return nil, &protocol.Error{Code: configError(refusal.Err).Code, Message: refusal.Reason}
}

func (s *Server) homeTree(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomePathParams) (protocol.HomeTreeResult, error) {
		entries, err := homes.HomeTree(ctx, member, p.Path)
		out := protocol.HomeTreeResult{Entries: make([]protocol.HomeTreeEntry, 0, len(entries))}
		for _, entry := range entries {
			out.Entries = append(out.Entries, protocol.HomeTreeEntry(entry))
		}
		return out, err
	})
}

func (s *Server) homeRead(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomePathParams) (protocol.ConfigFileReadResult, error) {
		read, err := homes.HomeRead(ctx, member, p.Path)
		return configReadResult(read), err
	})
}

func (s *Server) homeWrite(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomeWriteParams) (protocol.ConfigFileReadResult, error) {
		read, err := homes.HomeWrite(member, p.Path, []byte(p.Content), p.Revision)
		return configReadResult(read), err
	})
}

func (s *Server) homeMkdir(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomePathParams) (struct{}, error) {
		return struct{}{}, homes.HomeMkdir(member, p.Path)
	})
}

func (s *Server) homeRename(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomeRenameParams) (struct{}, error) {
		return struct{}{}, homes.HomeRename(member, p.From, p.To)
	})
}

func (s *Server) homeRemove(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomePathParams) (struct{}, error) {
		return struct{}{}, homes.HomeRemove(member, p.Path)
	})
}

func (s *Server) homeUpload(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomeUploadParams) (struct{}, error) {
		return struct{}{}, homes.HomeUpload(member, p.Path, p.Content, p.Overwrite)
	})
}

func (s *Server) homeDownload(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return homeCall(s, params, func(homes *memberhome.Manager, p protocol.HomePathParams) (protocol.HomeDownloadResult, error) {
		content, err := homes.HomeDownload(member, p.Path)
		return protocol.HomeDownloadResult{Content: content, Size: int64(len(content))}, err
	})
}
