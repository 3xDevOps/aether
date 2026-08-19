package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// DashboardTokens is the seam for the dashboard gateway's bearer tokens
// (*dashboard.Tokens). The HTTP transport has no login of its own, so the
// tokens that carry a member's authority onto it are minted here, on the
// authenticated SSH channel. Nil disables both methods with
// CodeUnavailable, the same degradation every other service seam uses.
type DashboardTokens interface {
	// Mint issues a token acting as member for ttl (zero means the
	// gateway default), returning it with its expiry.
	Mint(member domain.MemberID, ttl time.Duration) (token string, expires time.Time, err error)
	// Revoke drops a token minted for member.
	Revoke(member domain.MemberID, token string) error
	// URL renders the direct-exposure URL carrying token, empty when the
	// gateway is loopback-only.
	URL(token string) string
}

func init() {
	registerMethod(protocol.MethodDashTokenMint, (*Server).dashTokenMint)
	registerMethod(protocol.MethodDashTokenRevoke, (*Server).dashTokenRevoke)
}

func (s *Server) dashTokens() (DashboardTokens, *protocol.Error) {
	if s.cfg.Services.Dashboard == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the dashboard gateway is not enabled on this server"}
	}
	return s.cfg.Services.Dashboard, nil
}

// A member mints one token per `aether dash`, so the bucket is generous
// for people while bounding what a scripted mint loop can pile into the
// gateway's in-memory token table: at most mintBurst plus one entry per
// mintRefill for a token's whole TTL.
const (
	mintBurst  = 8
	mintRefill = time.Minute
)

// mintBucket is one member's dash-token mint allowance.
type mintBucket struct {
	tokens float64
	last   time.Time
}

// allowMint spends one mint token for member, refilling first.
func (s *Server) allowMint(member domain.MemberID, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mintBuckets == nil {
		s.mintBuckets = make(map[domain.MemberID]*mintBucket)
	}
	b := s.mintBuckets[member]
	if b == nil {
		b = &mintBucket{tokens: mintBurst, last: now}
		s.mintBuckets[member] = b
	}
	b.tokens = min(mintBurst, b.tokens+now.Sub(b.last).Seconds()/mintRefill.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Server) dashTokenMint(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	tokens, perr := s.dashTokens()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.DashTokenMintParams](params)
	if perr != nil {
		return nil, perr
	}
	if !s.allowMint(member, time.Now()) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: fmt.Sprintf("dash.token.mint: rate limit exceeded (burst %d, 1 per %s)", mintBurst, mintRefill),
		}
	}
	token, expires, err := tokens.Mint(member, time.Duration(p.TTLSeconds)*time.Second)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.DashTokenMintResult{
		Token:     token,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		URL:       tokens.URL(token),
	}, nil
}

func (s *Server) dashTokenRevoke(_ context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	tokens, perr := s.dashTokens()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.DashTokenRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Token == "" {
		return nil, invalidParams("token is required")
	}
	if err := tokens.Revoke(member, p.Token); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "dash.token.revoke: no such token for this member"}
	}
	return struct{}{}, nil
}
