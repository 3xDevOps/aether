package memory

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Capabilities lists the memory methods run's socket serves: reading
// always, adding while the run's owner may push.
func (s *Service) Capabilities(ctx context.Context, id domain.RunID) ([]string, error) {
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	capabilities := []string{protocol.MethodMemorySearch, protocol.MethodMemoryShow, protocol.MethodMemoryBrief}
	if err := s.authorize(ctx, run.MemberID, run.WorkspaceID); err == nil {
		capabilities = append([]string{protocol.MethodMemoryAdd}, capabilities...)
	} else if !errors.Is(err, permissions.ErrDenied) {
		return nil, err
	}
	return capabilities, nil
}

// HandleAgent answers a run's agent. The run is the socket's identity: the
// workspace, author, harness and commit all come from it.
func (s *Service) HandleAgent(ctx context.Context, id domain.RunID, method string, params json.RawMessage) (any, error) {
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, fail(method, err)
	}
	workspace := string(run.WorkspaceID)
	switch method {
	case protocol.MethodMemoryAdd:
		p, err := decode[protocol.MemoryAddParams](method, params)
		if err != nil {
			return nil, err
		}
		p.WorkspaceID = workspace
		return s.add(ctx, run.MemberID, run, p)
	case protocol.MethodMemorySearch:
		p, err := decode[protocol.MemorySearchParams](method, params)
		if err != nil {
			return nil, err
		}
		p.WorkspaceID = workspace
		return s.Search(ctx, p)
	case protocol.MethodMemoryShow:
		p, err := decode[protocol.MemoryShowParams](method, params)
		if err != nil {
			return nil, err
		}
		p.WorkspaceID = workspace
		return s.Show(ctx, p)
	case protocol.MethodMemoryBrief:
		text, err := s.Brief(ctx, run.WorkspaceID, run.MemberID, run.ID)
		if err != nil {
			return nil, err
		}
		return protocol.MemoryBriefResult{Text: text}, nil
	}
	return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
}

func decode[T any](method string, raw json.RawMessage) (T, error) {
	p, err := protocol.DecodeParams[T](raw)
	if err != nil {
		return p, invalid(method, err.Error())
	}
	return p, nil
}
