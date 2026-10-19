package coord

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// isMemoryMethod is the run socket's team memory allowlist. Pinning,
// unpinning and removal are for members only and never reach a run.
func isMemoryMethod(method string) bool {
	switch method {
	case protocol.MethodMemoryAdd, protocol.MethodMemorySearch, protocol.MethodMemoryShow, protocol.MethodMemoryBrief:
		return true
	default:
		return false
	}
}

// memoryCapabilities adds the memory methods a live run may call now.
func (s *Service) memoryCapabilities(ctx context.Context, run *domain.Run, capabilities []string) ([]string, *protocol.Error) {
	if run.Status.Terminal() || s.cfg.Memory == nil {
		return capabilities, nil
	}
	available, err := s.cfg.Memory.Capabilities(ctx, run.ID)
	if err != nil {
		return nil, missionRPCError(protocol.MethodCoordStatus, err)
	}
	for _, capability := range available {
		if isMemoryMethod(capability) {
			capabilities = appendCapability(capabilities, capability)
		}
	}
	return capabilities, nil
}

func (s *Service) handleMemory(ctx context.Context, run domain.RunID, method string, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Memory == nil {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
	}
	if !s.enterRun(run) {
		return nil, runClosing(method)
	}
	defer s.leaveRun(run)
	self, rpcErr := s.resolveRun(ctx, method, run)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if self.Status.Terminal() {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": run has finished"}
	}
	// encoding/json matches field names case-insensitively, so every
	// spelling of an identity field is refused before the service decodes.
	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil || fields == nil {
			return nil, invalidParams(method, "expected an object")
		}
	}
	for field := range fields {
		for _, identity := range []string{"run_id", "workspace_id", "member_id"} {
			if strings.EqualFold(field, identity) {
				return nil, invalidParams(method, identity+" comes from the socket")
			}
		}
	}
	result, err := s.cfg.Memory.HandleAgent(ctx, run, method, params)
	return result, missionRPCError(method, err)
}
