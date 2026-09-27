package edgeagent

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// claim answers one claim attempt the edge forwarded. The grant names the
// account; the code, which only this server can check, authorizes it.
func (a *Agent) claim(ctx context.Context, s *session, m edgeproto.Claim) {
	now := time.Now()
	g, err := edgeproto.VerifyGrant(s.edgeKey, m.Grant,
		edgeproto.GrantScope{ServerID: a.serverID, ConnID: m.ID, Kind: edgeproto.KindClaim}, now)
	if err == nil {
		err = a.firstUse(m.ID, now)
	}
	if err == nil {
		err = a.state.attemptClaim(m.Code, g.Account, now, func() error {
			member, cerr := a.cfg.SSH.ClaimByEdge(ctx, g.Account)
			if cerr == nil {
				slog.Info("edge: server claimed", "member", member.ID, "provider", g.Account.Provider, "subject", g.Account.Subject)
			}
			return cerr
		})
	}
	if err != nil {
		slog.Warn("edge: refused claim", "id", m.ID, "error", err)
		a.reply(s, edgeproto.ClaimResult{ID: m.ID, Error: replyText(err)})
		return
	}
	a.reply(s, edgeproto.ClaimResult{ID: m.ID})
	a.reply(s, edgeproto.Claimed{Owner: g.Account})
}
