package edge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// webCodeTTL bounds how long a browser has to carry a web sign-in code to
// its server.
const webCodeTTL = 2 * time.Minute

// claimTimeout bounds the wait for a server's answer to a claim.
const claimTimeout = 10 * time.Second

// Client is the device and account behind a device token.
type Client struct {
	Device  edgeproto.Device
	Account edgeproto.Account
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
// edgeproto.RefusalUnknownServer or edgeproto.RefusalNotMember.
func (s *Service) Admit(ctx context.Context, serverID string, a edgeproto.Account) (string, error) {
	acc, err := s.store.ServerAccess(ctx, serverID, a)
	if errors.Is(err, edgestore.ErrNotFound) {
		return "", edgeproto.RefusalUnknownServer
	}
	if err != nil {
		return "", err
	}
	role, ok := roleOf(acc, a, s.now())
	if !ok {
		return "", edgeproto.RefusalNotMember
	}
	return role, nil
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

// ServerConnected records the name an enrolled server announced and
// returns its state for edgeproto.Ready.
func (s *Service) ServerConnected(ctx context.Context, serverID, name string) (string, error) {
	err := s.store.RenameServer(ctx, serverID, name)
	if errors.Is(err, edgestore.ErrNotFound) {
		return edgeproto.StateUnclaimed, nil
	}
	if err != nil {
		return "", err
	}
	return edgeproto.StateClaimed, nil
}

// RecordClaim records owner as the owner of serverID after the server
// accepted owner's claim code.
func (s *Service) RecordClaim(ctx context.Context, serverID, name string, owner edgeproto.Account) error {
	if !edgeproto.ValidServerID(serverID) {
		return fmt.Errorf("edge: record claim: invalid server id %q", serverID)
	}
	if err := owner.Validate(); err != nil {
		return fmt.Errorf("edge: record claim: %w", err)
	}
	now := s.now()
	ownerID, err := s.store.EnsureAccount(ctx, owner, now)
	if err != nil {
		return err
	}
	return s.store.ClaimServer(ctx, serverID, name, ownerID, now)
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

// RedeemWebCode answers a server's edgeproto.WebRedeem. serverID is the
// server whose control channel carried it. The code is used up by the
// attempt whatever its outcome.
func (s *Service) RedeemWebCode(ctx context.Context, serverID string, r edgeproto.WebRedeem) edgeproto.WebRedeemResult {
	grant, err := s.redeemWebCode(ctx, serverID, r)
	if err != nil {
		return edgeproto.WebRedeemResult{ID: r.ID, Error: err.Error()}
	}
	return edgeproto.WebRedeemResult{ID: r.ID, Grant: grant}
}

func (s *Service) redeemWebCode(ctx context.Context, serverID string, r edgeproto.WebRedeem) (string, error) {
	if !edgeproto.ValidConnID(r.ID) || !edgeproto.ValidToken(r.Code) || !edgeproto.ValidToken(r.Verifier) {
		return "", errors.New("web sign-in code or verifier is malformed")
	}
	c, err := s.store.TakeWebCode(ctx, edgeproto.HashToken(r.Code))
	if errors.Is(err, edgestore.ErrNotFound) {
		return "", errors.New("web sign-in code is unknown or already used")
	}
	if err != nil {
		return "", err
	}
	switch {
	case c.ServerID != serverID:
		return "", errors.New("web sign-in code was issued for another server")
	case !s.now().Before(c.ExpiresAt):
		return "", errors.New("web sign-in code expired")
	case !edgeproto.VerifyPKCE(c.Challenge, r.Verifier):
		return "", errors.New("web sign-in verifier does not match the challenge")
	}
	if _, err := s.Admit(ctx, serverID, c.Account); err != nil {
		return "", err
	}
	return s.IssueGrant(edgeproto.Grant{
		ServerID:    serverID,
		ConnID:      r.ID,
		Kind:        edgeproto.KindWeb,
		Account:     c.Account,
		DeviceID:    c.SessionID,
		DeviceLabel: "browser",
	})
}

// claim forwards a claim code, as a person typed it, to the connected
// server it names, for the account on device d. d.Key is empty for a
// browser.
func (s *Service) claim(ctx context.Context, code string, a edgeproto.Account, d edgeproto.Device) (edgeproto.ClaimResponse, error) {
	if _, _, err := edgeproto.ParseClaimCode(code); err != nil {
		return edgeproto.ClaimResponse{}, pageErr(http.StatusBadRequest, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, claimTimeout)
	defer cancel()
	id, name, err := s.link.Claim(ctx, code, a, d)
	var refusal edgeproto.Refusal
	switch {
	case errors.As(err, &refusal):
		return edgeproto.ClaimResponse{}, refusal
	case err != nil:
		return edgeproto.ClaimResponse{}, pageErr(http.StatusGatewayTimeout, "claim: %v", err)
	}
	if err := s.RecordClaim(ctx, id, name, a); err != nil {
		return edgeproto.ClaimResponse{}, err
	}
	return edgeproto.ClaimResponse{ServerID: id, Name: name}, nil
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
		out = append(out, serverRow{
			ServerInfo: edgeproto.ServerInfo{
				ID: acc.ServerID, Name: acc.Name, Role: role, Online: s.link.Online(acc.ServerID),
			},
			Owner: acc.Owner,
		})
	}
	return out, nil
}

type serverRow struct {
	edgeproto.ServerInfo
	Owner bool
}
