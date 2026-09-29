package edgeagent

import (
	"fmt"
	"log/slog"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// claimAttempt checks a claim code the client of the claim connection
// grant opened presented inside SSH. The grant names the account; the
// code, which only this server can check and the edge never sees,
// authorizes it. On success the agent records the owner and reports it on
// the control connection s that opened the claim.
func (a *Agent) claimAttempt(s *session, grant edgeproto.Grant) func(code string, claim func() error) error {
	return func(code string, claim func() error) error {
		if err := a.state.attemptClaim(code, grant.Account, time.Now(), claim); err != nil {
			slog.Warn("edge: refused claim", "conn", grant.ConnID, "error", err)
			return err
		}
		slog.Info("edge: server claimed", "conn", grant.ConnID,
			"provider", grant.Account.Provider, "subject", grant.Account.Subject, "device", grant.DeviceID)
		a.reply(s, edgeproto.Claimed{ConnID: grant.ConnID, Owner: edgeproto.AccountPrincipal(grant.Account)})
		return nil
	}
}

// TransferOwner records owner, an admin's edge identity, as this server's
// owner and reports it on the live control connection. It refuses a
// server that has no owner, which only a claim code from this host gives
// one, and keeps the previous owner when the report cannot be sent.
func (a *Agent) TransferOwner(owner edgeproto.Account) error {
	a.mu.Lock()
	s := a.live
	a.mu.Unlock()
	if s == nil {
		return fmt.Errorf("the server is not connected to %s; retry once `aether-server edge status` shows it connected", a.origin)
	}
	return a.state.transferOwner(a.origin, owner, func() error {
		return s.send(edgeproto.OwnerTransferred{Owner: edgeproto.AccountPrincipal(owner)})
	})
}

// isMember reports whether owner is a member in the directory entries.
func isMember(entries []edgeproto.DirectoryEntry, owner edgeproto.Account) bool {
	for _, e := range entries {
		if e.Kind == edgeproto.EntryMember && e.Provider == owner.Provider && e.Subject == owner.Subject {
			return true
		}
	}
	return false
}

// disown forgets owner, whose identity left this server, and tells the
// edge the server is ownerless.
func (a *Agent) disown(s *session, owner edgeproto.Account) error {
	cleared, err := a.state.clearOwnerIf(owner)
	if err != nil || !cleared {
		return err
	}
	slog.Warn("edge: the owner's identity is no longer a member here; the server is ownerless until a code from `aether-server edge claim-code` claims it",
		"edge", a.origin, "owner_provider", owner.Provider, "owner_subject", owner.Subject)
	return s.send(edgeproto.Ownerless{})
}
