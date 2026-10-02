package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
)

// QuotaReader reads the selected account's vendor quota without exposing the
// account's credentials or provider responses to the caller.
type QuotaReader interface {
	Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error)
}

func init() {
	registerMethod(protocol.MethodAccountUsage, (*Server).accountUsage)
}

func (s *Server) accountUsage(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	params, perr := decodeParams[protocol.AccountUsageParams](raw)
	if perr != nil {
		return nil, perr
	}
	if err := s.checkMember(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	account, perr := s.launchAccount(ctx, member, params.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	reader := s.cfg.Services.Usage
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "account.usage: usage service not configured"}
	}

	providers, err := reader.Read(ctx, account, params.Refresh)
	if err != nil {
		// Provider implementations return safe per-provider diagnostics in
		// their rows. A top-level error may wrap an HTTP body or credential
		// path, so never echo it over the RPC boundary.
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "account.usage: usage providers unavailable"}
	}

	// The account grant and caller membership are checked again after the
	// potentially slow provider read. A revoke or removal during the read
	// must not turn a private result into a successful response.
	if err := s.checkMember(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	if _, perr := s.launchAccount(ctx, member, string(account)); perr != nil {
		return nil, perr
	}

	out := protocol.AccountUsageResult{
		AccountMemberID: string(account),
		Providers:       make([]protocol.UsageProvider, 0, len(providers)),
	}
	for _, provider := range providers {
		row := protocol.UsageProvider{
			Provider:  provider.Provider,
			Status:    provider.Status,
			Windows:   make([]protocol.UsageWindow, 0, len(provider.Windows)),
			Plan:      provider.Plan,
			CheckedAt: provider.CheckedAt.UTC().Format(time.RFC3339),
			Error:     provider.Error,
		}
		if provider.UpdatedAt != nil {
			updated := provider.UpdatedAt.UTC().Format(time.RFC3339)
			row.UpdatedAt = &updated
		}
		if provider.RetryAt != nil {
			retry := provider.RetryAt.UTC().Format(time.RFC3339)
			row.RetryAt = &retry
		}
		for _, window := range provider.Windows {
			wireWindow := protocol.UsageWindow{
				ID:          window.ID,
				Label:       window.Label,
				UsedPercent: window.UsedPercent,
			}
			if window.ResetsAt != nil {
				resets := window.ResetsAt.UTC().Format(time.RFC3339)
				wireWindow.ResetsAt = &resets
			}
			row.Windows = append(row.Windows, wireWindow)
		}
		out.Providers = append(out.Providers, row)
	}
	return out, nil
}
