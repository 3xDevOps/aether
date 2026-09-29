package sshd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/store"
)

// deviceIDExtension and deviceKeyExtension carry the edge device a
// connection authenticated with, alongside memberIDExtension.
const (
	deviceIDExtension  = "aether-device-id"
	deviceKeyExtension = "aether-device-key"
)

// connIdentity is who an authenticated connection is. device and deviceKey
// are set on connections that authenticated with an edge device key,
// relayed or direct.
type connIdentity struct {
	member    domain.MemberID
	device    domain.DeviceID
	deviceKey string
}

// connIdentityKey carries a connection's connIdentity in the context of its
// channels. The tailnet dashboard's in-process client (Local) carries none:
// it acts for a tailnet identity.
type connIdentityKey struct{}

func (s *Server) identityStore() (store.IdentityStore, error) {
	ids, ok := s.cfg.Store.(store.IdentityStore)
	if !ok {
		return nil, errors.New("sshd: the store does not hold edge identities")
	}
	return ids, nil
}

// ServeEdgeConn serves one SSH connection an edge relayed for a grant of
// kind ssh and returns when it ends. The caller has verified grant: its
// signature, issuer, server id, connection id, kind and expiry.
//
// The relayed transport has its own ssh.ServerConfig and its own pre-auth
// handshake budget. It offers public key authentication only: no "none"
// method, no tailnet WhoIs, no first-key bootstrap, no invite-code or
// claim user names. The key offered must be the grant's device key. nc's
// RemoteAddr is whatever the relay put there and is never read.
func (s *Server) ServeEdgeConn(ctx context.Context, nc net.Conn, grant edgeproto.Grant) {
	if grant.Kind != edgeproto.KindSSH {
		slog.Warn("sshd: relayed connection refused: grant is not for an ssh connection", "kind", grant.Kind)
		_ = nc.Close()
		return
	}
	s.serveRelayed(ctx, nc, s.edgeConfig(ctx, grant))
}

// ServeEdgeClaim serves one SSH connection an edge relayed for a grant of
// kind claim. The client presents the claim code as its SSH user name, in
// the form edgeproto.ClaimUser gives, and authenticates with the grant's
// device key. attempt spends one attempt of the server's claim code on
// code; when code is the claim code it runs claim with the admin member
// the code names, empty for none, and on claim's success destroys the
// code. Its refusals are the edgeproto claim refusals. On success the
// grant's account is an admin's identity and the device key is approved,
// under either access policy, because the code came from this machine's
// console. The connection then continues as that member's.
func (s *Server) ServeEdgeClaim(ctx context.Context, nc net.Conn, grant edgeproto.Grant, attempt func(code string, claim func(admin string) error) error) {
	if grant.Kind != edgeproto.KindClaim {
		slog.Warn("sshd: relayed claim refused: grant is not for a claim", "kind", grant.Kind)
		_ = nc.Close()
		return
	}
	s.serveRelayed(ctx, nc, s.claimConfig(ctx, grant, attempt))
}

func (s *Server) serveRelayed(ctx context.Context, nc net.Conn, cfg *ssh.ServerConfig) {
	if !s.beginHandler() {
		_ = nc.Close()
		return
	}
	defer s.wg.Done()
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	s.serveConn(ctx, nc, cfg, s.edgeHandshakes, edgeConnIdentity)
}

// grantKeyCallback accepts only the grant's device key. It runs for
// unsigned probes too, so it only compares keys.
func grantKeyCallback(grant edgeproto.Grant) func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
	return func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !edgeproto.DeviceKeyMatches(grant.DeviceKey, key) {
			return nil, &ssh.BannerError{
				Err:     errors.New("sshd: offered key is not the grant's device key"),
				Message: "the key offered is not the device key the edge signed this connection in with\n",
			}
		}
		return &ssh.Permissions{}, nil
	}
}

// edgeConfig authenticates a relayed connection. Its SSH user name is
// <provider>:<subject>, the account the client signed in as, under the
// client's own signature: a grant naming any other account is refused
// before anything is recorded, so an edge cannot register or bind a
// device key it relays to an account of its choosing.
func (s *Server) edgeConfig(ctx context.Context, grant edgeproto.Grant) *ssh.ServerConfig {
	keyOK := grantKeyCallback(grant)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if want := edgeproto.AccountUser(grant.Account); conn.User() != want {
				return nil, &ssh.BannerError{
					Err: fmt.Errorf("sshd: relayed connection as %q with a grant for %s", conn.User(), want),
					Message: fmt.Sprintf("this device connects as %q, but the edge signed the connection in as %s (%s); nothing was recorded\n",
						conn.User(), accountName(grant.Account), want),
				}
			}
			return keyOK(conn, key)
		},
		// Runs once the client has signed with the key, so store writes
		// here cannot be triggered by a key nobody holds, and a refusal
		// still reaches the client as a banner.
		VerifiedPublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey, _ *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			authCtx, cancel := context.WithTimeout(ctx, authTimeout)
			defer cancel()
			return s.admitEdge(authCtx, grant, key)
		},
	}
	cfg.AddHostKey(s.hostKey)
	return cfg
}

// claimConfig authenticates a claim connection. The SSH user name holds
// the claim code, so neither callback logs it or puts it in an error. It
// also names the account the client is signed in as, under the client's
// own signature: a grant naming any other account is refused before an
// attempt is spent, so an edge cannot make the claim for an account of its
// choosing.
func (s *Server) claimConfig(ctx context.Context, grant edgeproto.Grant, attempt func(string, func(string) error) error) *ssh.ServerConfig {
	keyOK := grantKeyCallback(grant)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			_, owner, ok := edgeproto.ParseClaimUser(conn.User())
			if !ok {
				return nil, &ssh.BannerError{
					Err:     errors.New("sshd: claim connection without a claim user name"),
					Message: "this connection claims the server: its SSH user name must carry the claim code and the claiming account\n",
				}
			}
			if owner != edgeproto.AccountPrincipal(grant.Account) {
				return nil, &ssh.BannerError{
					Err: fmt.Errorf("sshd: claim for %s %s relayed with a grant for %s", owner.Provider, owner.Subject, accountName(grant.Account)),
					Message: fmt.Sprintf("this device claims as %s account %s, but the edge signed the connection in as %s; the claim was not attempted\n",
						owner.Provider, owner.Subject, accountName(grant.Account)),
				}
			}
			return keyOK(conn, key)
		},
		// Runs once the client has signed with the device key: an attempt
		// is spent only by the holder of that key.
		VerifiedPublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey, _ *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			code, _, _ := edgeproto.ParseClaimUser(conn.User())
			authCtx, cancel := context.WithTimeout(ctx, authTimeout)
			defer cancel()
			var perms *ssh.Permissions
			err := attempt(code, func(admin string) error {
				var cerr error
				perms, cerr = s.claimServer(authCtx, grant, key, domain.MemberID(admin))
				return cerr
			})
			if err != nil {
				return nil, claimRefusal(err)
			}
			return perms, nil
		},
	}
	cfg.AddHostKey(s.hostKey)
	return cfg
}

func claimRefusal(err error) error {
	var banner *ssh.BannerError
	if errors.As(err, &banner) {
		return err
	}
	var refusal edgeproto.Refusal
	if errors.As(err, &refusal) {
		return &ssh.BannerError{Err: fmt.Errorf("sshd: claim refused: %w", err), Message: string(refusal) + "\n"}
	}
	return &ssh.BannerError{Err: fmt.Errorf("sshd: claim: %w", err), Message: "the claim failed on the server: " + err.Error() + "\n"}
}

func edgeConnIdentity(_ context.Context, sconn *ssh.ServerConn) (connIdentity, error) {
	ext := sconn.Permissions.Extensions
	id := connIdentity{
		member:    domain.MemberID(ext[memberIDExtension]),
		device:    domain.DeviceID(ext[deviceIDExtension]),
		deviceKey: ext[deviceKeyExtension],
	}
	if id.member == "" || id.device == "" {
		return connIdentity{}, errors.New("sshd: relayed connection authenticated without a member and device")
	}
	return id, nil
}

func devicePermissions(member domain.MemberID, dev *domain.Device) *ssh.Permissions {
	return &ssh.Permissions{Extensions: map[string]string{
		memberIDExtension:  string(member),
		deviceIDExtension:  string(dev.ID),
		deviceKeyExtension: dev.Credential,
	}}
}

// admitEdge maps the grant's account to a member, then admits the device
// under the server's access policy.
func (s *Server) admitEdge(ctx context.Context, grant edgeproto.Grant, key ssh.PublicKey) (*ssh.Permissions, error) {
	ids, err := s.identityStore()
	if err != nil {
		return nil, err
	}
	keyLine, label := edgeproto.DeviceKeyLine(key), deviceLabel(grant, key)
	m, err := ids.GetMemberByIdentity(ctx, grant.Account.Provider, grant.Account.Subject)
	if errors.Is(err, store.ErrNotFound) {
		m, err = s.edgeInvitee(ctx, ids, grant.Account, keyLine, label)
	}
	if err != nil {
		return nil, err
	}
	dev, err := s.edgeDevice(ctx, ids, m.ID, grant.Account, keyLine, label)
	if err != nil {
		return nil, err
	}
	if err := s.deviceRefusal(dev, true); err != nil {
		return nil, err
	}
	if err := ids.TouchDevice(ctx, dev.ID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("sshd: edge auth: %w", err)
	}
	slog.Info("sshd: edge auth", "member", m.ID, "device", dev.ID, "status", dev.Status,
		"provider", grant.Account.Provider, "subject", grant.Account.Subject, "pending", m.Pending)
	return devicePermissions(m.ID, dev), nil
}

func deviceLabel(grant edgeproto.Grant, key ssh.PublicKey) string {
	if grant.DeviceLabel != "" {
		return grant.DeviceLabel
	}
	return ssh.FingerprintSHA256(key)
}

// deviceAdmitted reports whether a connection may use dev: an approved
// device on any path; one awaiting approval only on a relayed connection
// under account access, where signing in admits it. The direct path
// accepts approved device keys only, under either policy: it never checks
// that the edge still vouches for the account.
func (s *Server) deviceAdmitted(dev *domain.Device, relayed bool) bool {
	return dev.Status == domain.DeviceApproved ||
		(relayed && dev.Status.AwaitsApproval() && s.cfg.EdgeAccess == edgeproto.PolicyAccount)
}

func (s *Server) deviceRefusal(dev *domain.Device, relayed bool) error {
	switch {
	case s.deviceAdmitted(dev, relayed):
		return nil
	case dev.Status == domain.DeviceRevoked:
		return &ssh.BannerError{
			Err:     errors.New("sshd: device revoked"),
			Message: fmt.Sprintf("device %q was revoked on this server\n", dev.Label),
		}
	}
	return &ssh.BannerError{Err: fmt.Errorf("sshd: device %s", dev.Status), Message: approvalBanner(dev, relayed)}
}

// approvalBanner shows a device awaiting approval its code and the
// account it signed in as, which decides the member approving admits it
// as. The code is shown here, inside SSH, and in no list.
func approvalBanner(dev *domain.Device, relayed bool) string {
	state := "is waiting for approval"
	switch {
	case dev.Status == domain.DeviceRegistered && relayed:
		state = "was admitted by signing in alone and is waiting for approval: this server now admits approved devices only"
	case dev.Status == domain.DeviceRegistered:
		state = "is not approved, and a direct connection accepts approved devices only"
	}
	who := "from an approved device, SSH key or tailnet connection of this account, or as an admin"
	if dev.Invitation != "" {
		who = "as an admin, or as the member an account link names; approving it accepts the invitation"
	}
	account := accountName(edgeproto.Account{Provider: dev.Provider, Subject: dev.Subject, Login: dev.Login, Email: dev.Email})
	return fmt.Sprintf("device %q, signed in as %s, %s. Approve it %s:\n"+
		"  aether device approve %s\nor on the server:\n  sudo aether-server device approve %s\n",
		dev.Label, account, state, who, dev.ApprovalCode, dev.ApprovalCode)
}

// directDevice admits an edge device key on a direct or tailnet
// connection. A device this server approved authenticates its member on
// every path, so a link with both an address and an edge reaches the
// server either way. It only reads: it also runs for unsigned probes.
func (s *Server) directDevice(ctx context.Context, key ssh.PublicKey) (*ssh.Permissions, bool, error) {
	ids, ok := s.cfg.Store.(store.IdentityStore)
	if !ok || key.Type() != ssh.KeyAlgoED25519 {
		return nil, false, nil
	}
	dev, err := ids.GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(key))
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sshd: resolve device key: %w", err)
	}
	if err := s.deviceRefusal(dev, false); err != nil {
		return nil, true, err
	}
	return devicePermissions(dev.Member, dev), true, nil
}

// edgeInvitee handles an account no member holds. One matching an open
// invitation joins under account access: it becomes that invitation's
// member. Under approved-devices the connecting device waits on the
// invitation instead, and nothing else changes until a person approves
// it: an edge that signs a grant for an invited account must not create a
// member, bind an account or use up the invitation.
func (s *Server) edgeInvitee(ctx context.Context, ids store.IdentityStore, account edgeproto.Account, keyLine, label string) (*domain.Member, error) {
	notMember := &ssh.BannerError{
		Err:     fmt.Errorf("sshd: %s: %w", accountName(account), edgeproto.RefusalNotMember),
		Message: accountName(account) + " is not a member of this server\n",
	}
	invs, err := matchingInvitations(ctx, ids, account)
	if err != nil {
		return nil, err
	}
	if len(invs) == 0 {
		return nil, notMember
	}
	if s.cfg.EdgeAccess != edgeproto.PolicyAccount {
		return nil, s.invitationDevice(ctx, ids, invs[0], account, keyLine, label)
	}
	m, err := s.acceptInvitation(ctx, ids, invs[0], account)
	if errors.Is(err, store.ErrNotFound) {
		// Another account consumed the invitation first.
		return nil, notMember
	}
	return m, err
}

// invitationDevice records the device with keyLine as waiting on inv for
// account, when it is new, and returns the device's refusal: the code
// that approves it, or its revocation.
func (s *Server) invitationDevice(ctx context.Context, ids store.IdentityStore, inv *domain.Invitation, account edgeproto.Account, keyLine, label string) error {
	dev := &domain.Device{Invitation: inv.ID, Provider: account.Provider, Subject: account.Subject,
		Email: account.Email, Login: account.Login, Name: account.Name,
		Credential: keyLine, Label: label, Status: domain.DevicePending}
	err := ids.RegisterInvitationDevice(ctx, dev, time.Now())
	switch {
	case errors.Is(err, store.ErrLimit):
		return tooManyWaiting(err, account)
	case errors.Is(err, store.ErrConflict):
		dev, err = ids.GetDeviceByCredential(ctx, keyLine)
	case err == nil:
		slog.Info("sshd: edge device waiting on an invitation", "invitation", inv.ID, "device", dev.ID,
			"provider", account.Provider, "subject", account.Subject, "key", fingerprintOf(keyLine))
	}
	if err != nil {
		return fmt.Errorf("sshd: invitation device: %w", err)
	}
	if dev.Member != "" || dev.Provider != account.Provider || dev.Subject != account.Subject {
		return errKeyOfAnotherAccount
	}
	return s.deviceRefusal(dev, true)
}

// tooManyWaiting refuses a new device of account once
// store.MaxWaitingDevices wait for approval.
func tooManyWaiting(err error, account edgeproto.Account) error {
	return &ssh.BannerError{
		Err: fmt.Errorf("sshd: %s: %w", accountName(account), err),
		Message: fmt.Sprintf("%d devices of %s are waiting for approval on this server already, so no new one is recorded. "+
			"An admin approves or revokes them with:\n  aether device list\nor on the server:\n  sudo aether-server device review\n",
			store.MaxWaitingDevices, accountName(account)),
	}
}

var errKeyOfAnotherAccount = &ssh.BannerError{
	Err:     errors.New("sshd: device key registered to another account"),
	Message: "this device key is registered to another account on this server\n",
}

// acceptInvitation binds account through inv, creating the member unless
// inv links an existing one. It creates no device: the device connecting
// is registered by edgeDevice under the server's policy.
func (s *Server) acceptInvitation(ctx context.Context, ids store.IdentityStore, inv *domain.Invitation, account edgeproto.Account) (*domain.Member, error) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("sshd: accept invitation: %w", err)
	}
	fresh := &domain.Member{DisplayName: displayNameOf(account), Color: attribution.NextColor(memberColorsOf(members))}
	m, err := ids.AcceptInvitation(ctx, inv.ID, identityOf(account), fresh, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sshd: accept invitation %s: %w", inv.ID, err)
	}
	s.notifyDirectory()
	slog.Info("sshd: edge invitation accepted", "invitation", inv.ID, "member", m.ID, "role", m.Role,
		"provider", account.Provider, "subject", account.Subject)
	return m, nil
}

// matchingInvitations returns the open invitations account matches now,
// oldest first, matched exactly as the edge matches directory entries.
func matchingInvitations(ctx context.Context, ids store.IdentityStore, account edgeproto.Account) ([]*domain.Invitation, error) {
	invs, err := ids.ListInvitations(ctx)
	if err != nil {
		return nil, fmt.Errorf("sshd: match invitation: %w", err)
	}
	now := time.Now()
	var matched []*domain.Invitation
	for _, inv := range invs {
		entry := edgeproto.DirectoryEntry{Kind: edgeproto.EntryInvitation, Provider: inv.Provider,
			Login: inv.Login, Email: inv.Email, ExpiresAt: inv.ExpiresAt}
		if entry.Matches(account, now) {
			matched = append(matched, inv)
		}
	}
	return matched, nil
}

// edgeDevice returns the device registered with keyLine, registering it
// for account's identity of member on first contact: registered under
// account access, pending under approved-devices. A new or rotated key is
// a new device. A key registered through another identity is refused,
// even another identity of the same member, and so is a new key once
// store.MaxWaitingDevices of the account's devices are pending.
func (s *Server) edgeDevice(ctx context.Context, ids store.IdentityStore, member domain.MemberID, account edgeproto.Account, keyLine, label string) (*domain.Device, error) {
	dev, err := ids.GetDeviceByCredential(ctx, keyLine)
	if errors.Is(err, store.ErrNotFound) {
		status := domain.DevicePending
		if s.cfg.EdgeAccess == edgeproto.PolicyAccount {
			status = domain.DeviceRegistered
		}
		dev = &domain.Device{Member: member, Provider: account.Provider, Subject: account.Subject,
			Email: account.Email, Login: account.Login, Name: account.Name,
			Credential: keyLine, Label: label, Status: status}
		err = ids.RegisterDevice(ctx, dev)
		switch {
		case errors.Is(err, store.ErrLimit):
			return nil, tooManyWaiting(err, account)
		case errors.Is(err, store.ErrConflict):
			// A concurrent first contact of the same device registered it.
			dev, err = ids.GetDeviceByCredential(ctx, keyLine)
		case err == nil:
			slog.Info("sshd: edge device registered", "member", member, "device", dev.ID,
				"status", dev.Status, "key", fingerprintOf(keyLine))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sshd: edge device: %w", err)
	}
	if dev.Member != member || dev.Provider != account.Provider || dev.Subject != account.Subject {
		return nil, errKeyOfAnotherAccount
	}
	return dev, nil
}

func identityOf(a edgeproto.Account) *domain.Identity {
	return &domain.Identity{Provider: a.Provider, Subject: a.Subject, Email: a.Email, Login: a.Login}
}

// accountName names an account the way its owner knows it.
func accountName(a edgeproto.Account) string {
	switch {
	case a.Login != "":
		return a.Provider + " account " + a.Login
	case a.Email != "":
		return a.Provider + " account " + a.Email
	}
	return a.Provider + " account " + a.Subject
}

func displayNameOf(a edgeproto.Account) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Login != "":
		return a.Login
	case a.Email != "":
		return displayNameFromLogin(a.Email)
	}
	return a.Provider + " " + a.Subject
}

// claimServer makes the grant's account this server's admin after the
// claim code matched, and approves key as that member's device. On a
// server with no member it creates that admin. On a server with members,
// the account must already be an admin's identity (a repeated claim), or
// match an admin's open link from member.identity.link, which it then
// consumes: an admin who joined by key or tailnet links the account
// first, then claims. A code the machine's administrator issued for an
// existing admin binds the account to that admin instead (recoverAdmin).
// Anything else is refused with edgeproto.RefusalClaimed. It runs under
// registerMu, which member.role and member.remove also hold, so the admin
// checked here stays an admin until the claim is done.
func (s *Server) claimServer(ctx context.Context, grant edgeproto.Grant, key ssh.PublicKey, admin domain.MemberID) (*ssh.Permissions, error) {
	ids, err := s.identityStore()
	if err != nil {
		return nil, err
	}
	account := grant.Account
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	var m *domain.Member
	if admin != "" {
		m, err = s.recoverAdmin(ctx, ids, admin, account)
	} else {
		m, err = s.claimMember(ctx, ids, account)
	}
	if err != nil {
		return nil, err
	}
	if m.Role != domain.RoleAdmin {
		return nil, edgeproto.RefusalClaimed
	}
	dev, err := s.claimDevice(ctx, ids, m.ID, account, edgeproto.DeviceKeyLine(key), deviceLabel(grant, key))
	if err != nil {
		return nil, err
	}
	s.notifyDirectory()
	slog.Info("sshd: claimed through the edge", "member", m.ID, "device", dev.ID,
		"provider", account.Provider, "subject", account.Subject, "console_recovery", admin != "")
	return devicePermissions(m.ID, dev), nil
}

// recoverAdmin binds account to admin, the existing admin member a claim
// code from `aether-server edge claim-code --admin` names: the machine's
// administrator restores an admin who can no longer reach the server. It
// creates no member and changes no role. An account bound to another
// member is refused.
func (s *Server) recoverAdmin(ctx context.Context, ids store.IdentityStore, admin domain.MemberID, account edgeproto.Account) (*domain.Member, error) {
	m, err := s.cfg.Store.GetMember(ctx, admin)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &ssh.BannerError{Err: fmt.Errorf("sshd: claim for member %s: %w", admin, err),
			Message: fmt.Sprintf("the claim code names member %s, which no longer exists; get a new code on the server\n", admin)}
	}
	if err != nil {
		return nil, fmt.Errorf("sshd: claim: %w", err)
	}
	if m.Role != domain.RoleAdmin {
		return nil, &ssh.BannerError{Err: fmt.Errorf("sshd: claim for member %s, who is %s", admin, m.Role),
			Message: fmt.Sprintf("the claim code names member %s, who is %s, not an admin; the claim was refused\n", admin, m.Role)}
	}
	bound, err := ids.GetMemberByIdentity(ctx, account.Provider, account.Subject)
	switch {
	case err == nil && bound.ID == m.ID:
		return m, nil
	case err == nil:
		return nil, &ssh.BannerError{Err: fmt.Errorf("sshd: claim for member %s by an account of member %s", admin, bound.ID),
			Message: fmt.Sprintf("%s belongs to member %s on this server, not to %s; the claim was refused\n", accountName(account), bound.ID, admin)}
	case !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("sshd: claim: %w", err)
	}
	identity := identityOf(account)
	identity.Member = m.ID
	if err := ids.BindIdentity(ctx, identity); err != nil {
		return nil, fmt.Errorf("sshd: claim: %w", err)
	}
	slog.Warn("sshd: console recovery bound an edge account to an admin", "member", m.ID,
		"provider", account.Provider, "subject", account.Subject, "login", account.Login, "email", account.Email)
	return m, nil
}

func (s *Server) claimMember(ctx context.Context, ids store.IdentityStore, account edgeproto.Account) (*domain.Member, error) {
	m, err := ids.GetMemberByIdentity(ctx, account.Provider, account.Subject)
	if !errors.Is(err, store.ErrNotFound) {
		return m, err
	}
	owner := &domain.Member{DisplayName: displayNameOf(account), Color: attribution.NextColor(nil), Role: domain.RoleAdmin}
	err = ids.ClaimMember(ctx, owner, identityOf(account))
	if !errors.Is(err, store.ErrConflict) {
		return owner, err
	}
	invs, err := matchingInvitations(ctx, ids, account)
	if err != nil {
		return nil, err
	}
	for _, inv := range invs {
		if inv.Member == "" {
			continue
		}
		linked, err := s.cfg.Store.GetMember(ctx, inv.Member)
		if err != nil {
			return nil, fmt.Errorf("sshd: claim: linked member: %w", err)
		}
		if linked.Role != domain.RoleAdmin {
			continue
		}
		m, err = ids.AcceptInvitation(ctx, inv.ID, identityOf(account), owner, time.Now())
		if err != nil {
			return nil, fmt.Errorf("sshd: claim: %w", err)
		}
		return m, nil
	}
	return nil, edgeproto.RefusalClaimed
}

// claimDevice approves the claiming device key for member through
// account's identity, registering it when new. A key registered through
// another identity, or revoked, is refused.
func (s *Server) claimDevice(ctx context.Context, ids store.IdentityStore, member domain.MemberID, account edgeproto.Account, keyLine, label string) (*domain.Device, error) {
	dev, err := ids.GetDeviceByCredential(ctx, keyLine)
	if errors.Is(err, store.ErrNotFound) {
		dev = &domain.Device{Member: member, Provider: account.Provider, Subject: account.Subject,
			Email: account.Email, Login: account.Login, Name: account.Name,
			Credential: keyLine, Label: label, Status: domain.DeviceApproved}
		if err = ids.RegisterDevice(ctx, dev); err != nil {
			return nil, fmt.Errorf("sshd: claim: register device: %w", err)
		}
		return dev, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sshd: claim: %w", err)
	}
	if dev.Member != member || dev.Provider != account.Provider || dev.Subject != account.Subject {
		return nil, errKeyOfAnotherAccount
	}
	switch dev.Status {
	case domain.DeviceRevoked:
		return nil, &ssh.BannerError{
			Err:     errors.New("sshd: claim: device revoked"),
			Message: fmt.Sprintf("device %q was revoked on this server; claim from another device\n", dev.Label),
		}
	case domain.DeviceRegistered, domain.DevicePending:
		if err := ids.ApproveDevice(ctx, dev.ID, ""); err != nil {
			return nil, fmt.Errorf("sshd: claim: approve device: %w", err)
		}
		return ids.GetDevice(ctx, dev.ID)
	}
	return dev, nil
}

// EdgeAccountDeleted handles the edge's notice that the account provider,
// subject was deleted there: it removes that identity and the devices
// registered through it, and closes their connections. The notice is the
// edge's assertion, so it may only take access away: it removes no
// member, changes no role, deletes no data, and leaves the member's SSH
// keys, tailnet identity and other identities working.
func (s *Server) EdgeAccountDeleted(ctx context.Context, provider, subject string) error {
	ids, err := s.identityStore()
	if err != nil {
		return err
	}
	member, devices, err := ids.RemoveIdentity(ctx, provider, subject)
	if errors.Is(err, store.ErrNotFound) {
		slog.Info("sshd: edge account deleted; no member has that identity", "provider", provider, "subject", subject)
		return nil
	}
	if err != nil {
		return fmt.Errorf("sshd: edge account deleted: %w", err)
	}
	s.closeConns(func(id connIdentity) bool { return slices.Contains(devices, id.device) })
	s.notifyDirectory()
	slog.Info("sshd: edge account deleted; removed its identity and edge devices",
		"member", member, "provider", provider, "subject", subject, "devices", len(devices))
	return nil
}

// EdgeDirectory is the directory the edge agent pushes: every member bound
// to an edge identity, and every open, unexpired invitation.
func (s *Server) EdgeDirectory(ctx context.Context) ([]edgeproto.DirectoryEntry, error) {
	ids, err := s.identityStore()
	if err != nil {
		return nil, err
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("sshd: edge directory: %w", err)
	}
	roles := make(map[domain.MemberID]string, len(members))
	for _, m := range members {
		roles[m.ID] = string(m.Role)
	}
	identities, err := ids.ListIdentities(ctx)
	if err != nil {
		return nil, fmt.Errorf("sshd: edge directory: %w", err)
	}
	invs, err := ids.ListInvitations(ctx)
	if err != nil {
		return nil, fmt.Errorf("sshd: edge directory: %w", err)
	}
	// An identity or invitation v0.5.2-alpha.3 stored for Google matches no
	// account and is left out; the listings show it until it is removed.
	var entries []edgeproto.DirectoryEntry
	for _, id := range identities {
		if id.Provider != edgeproto.ProviderGitHub {
			continue
		}
		// A member removed between the two reads has no role and is left out.
		if role, ok := roles[id.Member]; ok {
			entries = append(entries, edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember,
				Provider: id.Provider, Subject: id.Subject, Role: role})
		}
	}
	now := time.Now()
	for _, inv := range invs {
		if inv.Provider != "" && inv.Provider != edgeproto.ProviderGitHub {
			continue
		}
		role, ok := string(inv.Role), true
		if inv.Member != "" {
			role, ok = roles[inv.Member]
		}
		if ok && now.Before(inv.ExpiresAt) {
			entries = append(entries, edgeproto.DirectoryEntry{Kind: edgeproto.EntryInvitation,
				Provider: inv.Provider, Login: inv.Login, Email: inv.Email, Role: role, ExpiresAt: inv.ExpiresAt.UTC()})
		}
	}
	if len(entries) > edgeproto.MaxDirectoryEntries {
		return nil, fmt.Errorf("sshd: edge directory has %d entries, more than the %d an edge accepts",
			len(entries), edgeproto.MaxDirectoryEntries)
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("sshd: edge directory: %w", err)
		}
	}
	return entries, nil
}

// EdgeDirectoryChanged signals after any change that can alter
// EdgeDirectory: members, their roles and identities, and invitations.
// Signals coalesce; the channel has one receiver, the edge agent, which
// re-reads the whole directory on each.
func (s *Server) EdgeDirectoryChanged() <-chan struct{} {
	return s.directoryChanged
}

func (s *Server) notifyDirectory() {
	select {
	case s.directoryChanged <- struct{}{}:
	default:
	}
}

func (s *Server) bindConn(c net.Conn, id connIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, tracked := s.conns[c]; tracked {
		s.connIdentities[c] = id
	}
}

// closeConns closes every connection whose identity matches. Closing the
// transport ends its channels and cancels their contexts.
func (s *Server) closeConns(match func(connIdentity) bool) {
	var doomed []net.Conn
	s.mu.Lock()
	for c, id := range s.connIdentities {
		if match(id) {
			doomed = append(doomed, c)
		}
	}
	s.mu.Unlock()
	for _, c := range doomed {
		_ = c.Close()
	}
}

func (s *Server) closeMemberConns(member domain.MemberID) {
	s.closeConns(func(id connIdentity) bool { return id.member == member })
}

// checkConnIdentity re-reads that the member exists and, on a connection
// that authenticated with an edge device, that the device still admits
// it. Only relayed connections ever hold a device awaiting approval, so
// it is admitted as a relayed one.
func (s *Server) checkConnIdentity(ctx context.Context, id connIdentity) error {
	if _, err := s.memberFor(ctx, id.member); err != nil {
		return err
	}
	if id.device == "" {
		return nil
	}
	ids, err := s.identityStore()
	if err != nil {
		return err
	}
	dev, err := ids.GetDevice(ctx, id.device)
	if err != nil {
		return fmt.Errorf("sshd: device %s: %w", id.device, err)
	}
	if !s.deviceAdmitted(dev, true) {
		return fmt.Errorf("sshd: device %s is %s", id.device, dev.Status)
	}
	return nil
}

// watchDevice closes a connection that authenticated with an edge device
// once the device stops admitting it: revoked or removed by another
// process, such as `aether-server device review`, which cannot reach this
// server's connections. A revocation through this server closes them at
// once instead.
func (s *Server) watchDevice(ctx context.Context, id connIdentity, abort func()) {
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.checkConnIdentity(ctx, id); err != nil && ctx.Err() == nil {
				slog.Info("sshd: device no longer admitted; closing connection", "member", id.member, "device", id.device, "error", err)
				abort()
				return
			}
		}
	}
}
