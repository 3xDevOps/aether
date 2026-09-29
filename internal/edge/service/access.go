package edge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// Client is the device and account behind a device token.
type Client struct {
	Device  edgeproto.Device
	Account edgeproto.AccountInfo
}

// authenticate checks the request's "Authorization: Bearer <device token>".
// It fails with edgeproto.RefusalTokenRequired when there is no token and
// edgeproto.RefusalTokenRevoked when the token is not a live device token.
func (s *Service) authenticate(r *http.Request) (Client, int64, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Client{}, 0, edgeproto.RefusalTokenRequired
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return Client{}, 0, edgeproto.RefusalTokenRevoked
	}
	return s.deviceByToken(r.Context(), token)
}

// deviceByToken returns the device and account a device token belongs to,
// or edgeproto.RefusalTokenRevoked for a token that is not a live one.
func (s *Service) deviceByToken(ctx context.Context, token string) (Client, int64, error) {
	if !edgeproto.ValidToken(token) {
		return Client{}, 0, edgeproto.RefusalTokenRevoked
	}
	d, err := s.store.UseDevice(ctx, edgeproto.HashToken(token), s.now())
	if errors.Is(err, edgestore.ErrNotFound) {
		return Client{}, 0, edgeproto.RefusalTokenRevoked
	}
	if err != nil {
		return Client{}, 0, err
	}
	return Client{
		Device:  edgeproto.Device{ID: d.ID, Label: d.Label, Key: d.Key},
		Account: d.Account,
	}, d.AccountID, nil
}

// Admit reports the role account a has on the claimed server serverID: a
// member entry's role, the admin role for the owner, or a live
// invitation's role, in that order. It fails with
// edgeproto.RefusalUnknownServer, edgeproto.RefusalIdentityStale when only
// an invitation could admit a and its identity is not current, or
// edgeproto.RefusalNotMember.
func (s *Service) Admit(ctx context.Context, serverID string, a edgeproto.Account) (string, error) {
	acc, err := s.store.ServerAccess(ctx, serverID, a)
	if errors.Is(err, edgestore.ErrNotFound) {
		return "", edgeproto.RefusalUnknownServer
	}
	if err != nil {
		return "", err
	}
	now := s.now()
	role, ok := roleOf(acc, a, now)
	if ok {
		return role, nil
	}
	confirmed := a
	confirmed.IdentityAt = now
	if _, ok := roleOf(acc, confirmed, now); ok {
		return "", edgeproto.RefusalIdentityStale
	}
	return "", edgeproto.RefusalNotMember
}

func roleOf(acc edgestore.Access, a edgeproto.Account, now time.Time) (string, bool) {
	for _, e := range acc.Entries {
		if e.Kind == edgeproto.EntryMember && e.Matches(a, now) {
			return e.Role, true
		}
	}
	if acc.Owner {
		return string(domain.RoleAdmin), true
	}
	for _, e := range acc.Entries {
		if e.Kind == edgeproto.EntryInvitation && e.Matches(a, now) {
			return e.Role, true
		}
	}
	return "", false
}

// AdmitClaim admits account, connecting from addr, to open a claim
// connection to serverID: it fails with edgeproto.RefusalTooMany past the
// limit per address, per account or per server, and
// edgeproto.RefusalClaimed when serverID has an owner. The relay admits
// the connection only to a connected server; the server checks the claim
// code inside SSH.
func (s *Service) AdmitClaim(ctx context.Context, serverID string, account edgeproto.Account, addr netip.Addr) error {
	if !s.claimLimit.allow(edgeproto.RateLimitKeys(addr)...) ||
		!s.claimAccountLimit.allow(edgeproto.AccountPrincipal(account)) ||
		!s.claimServerLimit.allow(serverID) {
		return edgeproto.RefusalTooMany
	}
	owned, err := s.store.ServerOwned(ctx, serverID)
	if err != nil {
		return err
	}
	if owned {
		return edgeproto.RefusalClaimed
	}
	return nil
}

// ServerConnected records the name and access policy an enrolled server
// announced and returns its state for edgeproto.Ready, or
// edgeproto.RefusalServerBlocked for a server id the operator blocked.
func (s *Service) ServerConnected(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy) (string, error) {
	blocked, err := s.store.ServerBlocked(ctx, serverID)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", edgeproto.RefusalServerBlocked
	}
	claimed, err := s.store.EnrollServer(ctx, serverID, name, policy)
	if err != nil {
		return "", err
	}
	if !claimed {
		return edgeproto.StateUnclaimed, nil
	}
	return edgeproto.StateClaimed, nil
}

// Refusals of an ownership report, which the relay sends the server that
// made it.
const (
	refusalNoOwnerAccount edgeproto.Refusal = "the owner has no account at this edge: it never signed in here, or its account was deleted"
	refusalNeverClaimed   edgeproto.Refusal = "this server was never claimed at this edge"
)

// ownerRefusal maps the store's refusals of an owner to the refusal the
// server receives.
func ownerRefusal(err error) error {
	switch {
	case errors.Is(err, edgestore.ErrServerBlocked):
		return edgeproto.RefusalServerBlocked
	case errors.Is(err, edgestore.ErrAccountBlocked):
		return edgeproto.RefusalAccountBlocked
	case errors.Is(err, edgestore.ErrNoAccount):
		return refusalNoOwnerAccount
	case errors.Is(err, edgestore.ErrHasOwner):
		return edgeproto.RefusalClaimed
	case errors.Is(err, edgestore.ErrNotFound):
		return refusalNeverClaimed
	}
	return err
}

// RecordClaim records owner as the owner of serverID after the server
// reported that owner's claim connection presented its claim code; name
// and policy are from its hello. It records nothing, and fails with an
// edgeproto.Refusal, when the server or the account is blocked, the
// account no longer exists, or the server has an owner.
func (s *Service) RecordClaim(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy, owner edgeproto.Account) error {
	if !edgeproto.ValidServerID(serverID) {
		return fmt.Errorf("edge: record claim: invalid server id %q", serverID)
	}
	return ownerRefusal(s.store.RecordClaim(ctx, serverID, name, policy, edgeproto.AccountPrincipal(owner), s.now()))
}

// TransferOwner records owner as the owner of the claimed server
// serverID, as the server reported. It fails with an edgeproto.Refusal
// when owner has no account at this edge or is blocked.
func (s *Service) TransferOwner(ctx context.Context, serverID string, owner edgeproto.Principal) error {
	return ownerRefusal(s.store.TransferOwner(ctx, serverID, owner))
}

// DropOwner records that the claimed server serverID has no owner, as it
// reported.
func (s *Service) DropOwner(ctx context.Context, serverID string) error {
	return ownerRefusal(s.store.DropOwner(ctx, serverID))
}

// RemoveServer forgets serverID and its directory, as when its operator
// sends edgeproto.Unenroll. Forgetting an unknown server succeeds.
func (s *Service) RemoveServer(ctx context.Context, serverID string) error {
	if err := s.store.DeleteServer(ctx, serverID); err != nil && !errors.Is(err, edgestore.ErrNotFound) {
		return err
	}
	return nil
}

// ReplaceDirectory replaces the directory of the claimed server serverID.
func (s *Service) ReplaceDirectory(ctx context.Context, serverID string, d edgeproto.Directory) error {
	if len(d.Entries) > edgeproto.MaxDirectoryEntries {
		return fmt.Errorf("edge: directory has %d entries, limit %d", len(d.Entries), edgeproto.MaxDirectoryEntries)
	}
	for i, e := range d.Entries {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("edge: directory entry %d: %w", i, err)
		}
	}
	err := s.store.ReplaceDirectory(ctx, serverID, d.Entries)
	if errors.Is(err, edgestore.ErrNotFound) {
		return fmt.Errorf("edge: server %s is not claimed; its directory is ignored", serverID)
	}
	return err
}

// revokeDevice deletes one of the account's devices and has the relay
// close its connections.
func (s *Service) revokeDevice(ctx context.Context, accountID int64, deviceID string) error {
	err := s.store.DeleteDevice(ctx, accountID, deviceID)
	if errors.Is(err, edgestore.ErrNotFound) {
		return pageErr(http.StatusNotFound, "device %s is not one of your devices", deviceID)
	}
	if err != nil {
		return err
	}
	s.link.RevokeDevice(deviceID)
	return nil
}

// servers lists the servers account a can reach, with its role on each.
// Member is set where a's directory entry is a membership, not an
// invitation.
func (s *Service) servers(ctx context.Context, a edgeproto.Account) ([]serverRow, error) {
	all, err := s.store.AccountAccess(ctx, a)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []serverRow
	for _, acc := range all {
		role, ok := roleOf(acc, a, now)
		if !ok {
			continue
		}
		row := serverRow{
			ServerInfo: edgeproto.ServerInfo{
				ID: acc.ServerID, Name: acc.Name, Role: role, Online: s.link.Online(acc.ServerID),
				AccessPolicy: acc.AccessPolicy, Kind: acc.Kind,
			},
			Owner: acc.Owner,
		}
		for _, e := range acc.Entries {
			if e.Kind == edgeproto.EntryMember && e.Matches(a, now) {
				row.Member = true
			}
		}
		out = append(out, row)
	}
	return out, nil
}

type serverRow struct {
	edgeproto.ServerInfo
	Owner  bool
	Member bool
}
