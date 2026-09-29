package edgeagent

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// claimAttempt checks a claim code the client of the claim connection
// grant opened presented inside SSH. The grant names the account; the
// code, which only this server can check and the edge never sees,
// authorizes it. Once claim succeeds the agent reports the owner on the
// live control connection, which may have replaced the one that opened
// the claim, and only then records it and destroys the code: a claim the
// edge never heard of would leave the edge unclaimed, and the server
// would forget the owner at its next enrollment.
func (a *Agent) claimAttempt(grant edgeproto.Grant) func(code string, claim func(admin string) error) error {
	return func(code string, claim func(admin string) error) error {
		err := a.state.attemptClaim(code, grant.Account, time.Now(), func(admin string) error {
			if err := claim(admin); err != nil {
				return err
			}
			s, err := a.liveSession()
			if err != nil {
				return err
			}
			return s.send(edgeproto.Claimed{ConnID: grant.ConnID, Owner: edgeproto.AccountPrincipal(grant.Account)})
		})
		if err != nil {
			slog.Warn("edge: refused claim", "conn", grant.ConnID, "error", err)
			return err
		}
		slog.Info("edge: server claimed", "conn", grant.ConnID,
			"provider", grant.Account.Provider, "subject", grant.Account.Subject, "device", grant.DeviceID)
		return nil
	}
}

// liveSession returns the enrolled control connection.
func (a *Agent) liveSession() (*session, error) {
	a.mu.Lock()
	s := a.live
	a.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("the server is not connected to %s; retry once `aether-server edge status` shows it connected", a.origin)
	}
	return s, nil
}

// TransferOwner records owner, an admin's edge identity, as this server's
// owner and reports it on the live control connection. It refuses a
// server that has no owner, which only a claim code from this host gives
// one, and keeps the previous owner unless the edge answers that it
// recorded the new one.
func (a *Agent) TransferOwner(owner edgeproto.Account) error {
	s, err := a.liveSession()
	if err != nil {
		return err
	}
	return a.state.transferOwner(a.origin, owner, func() error {
		return s.transfer(edgeproto.AccountPrincipal(owner))
	})
}

// transfer reports owner as the new owner and waits for the edge's answer.
func (s *session) transfer(owner edgeproto.Principal) error {
	// An answer left from a transfer that stopped waiting is not this one's.
	select {
	case <-s.transferred:
	default:
	}
	if err := s.send(edgeproto.OwnerTransferred{Owner: owner}); err != nil {
		return err
	}
	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()
	for {
		select {
		case r := <-s.transferred:
			switch {
			case r.Owner != owner:
				continue
			case r.Error != "":
				return errors.New(r.Error)
			}
			return nil
		case <-timeout.C:
			return fmt.Errorf("no answer within %s", handshakeTimeout)
		}
	}
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
