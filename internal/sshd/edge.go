package sshd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/store"
)

// deviceIDExtension and deviceKeyExtension carry the edge device a relayed
// connection authenticated with, alongside memberIDExtension.
const (
	deviceIDExtension  = "aether-device-id"
	deviceKeyExtension = "aether-device-key"
)

// connIdentity is who an authenticated connection is. device and deviceKey
// are set on connections an edge relayed.
type connIdentity struct {
	member    domain.MemberID
	device    domain.DeviceID
	deviceKey string
}

func (s *Server) identityStore() (store.IdentityStore, error) {
	ids, ok := s.cfg.Store.(store.IdentityStore)
	if !ok {
		return nil, errors.New("sshd: the store does not hold edge identities")
	}
	return ids, nil
}

// ServeEdgeConn serves one SSH connection an edge relayed and returns when
// it ends. The caller has verified grant: its signature, server id,
// connection id and expiry.
//
// The relayed transport has its own ssh.ServerConfig and its own pre-auth
// handshake budget. It offers public key authentication only: no "none"
// method, no tailnet WhoIs, no first-key bootstrap, no invite-code user
// names. The key offered must be the grant's device key. nc's RemoteAddr
// is whatever the relay put there and is never read.
func (s *Server) ServeEdgeConn(ctx context.Context, nc net.Conn, grant edgeproto.Grant) {
	if !s.beginHandler() {
		_ = nc.Close()
		return
	}
	defer s.wg.Done()
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	s.serveConn(ctx, nc, s.edgeConfig(ctx, grant), s.edgeHandshakes, edgeConnIdentity)
}

func (s *Server) edgeConfig(ctx context.Context, grant edgeproto.Grant) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		// Runs for unsigned probes too, so it only compares keys.
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !edgeproto.DeviceKeyMatches(grant.DeviceKey, key) {
				return nil, &ssh.BannerError{
					Err:     errors.New("sshd: offered key is not the grant's device key"),
					Message: "the key offered is not the device key the edge signed this connection in with\n",
				}
			}
			return &ssh.Permissions{}, nil
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

// admitEdge maps the grant's account to a member, then admits the device.
func (s *Server) admitEdge(ctx context.Context, grant edgeproto.Grant, key ssh.PublicKey) (*ssh.Permissions, error) {
	ids, err := s.identityStore()
	if err != nil {
		return nil, err
	}
	m, err := s.edgeMember(ctx, ids, grant.Account)
	if err != nil {
		return nil, err
	}
	keyLine := edgeproto.DeviceKeyLine(key)
	label := grant.DeviceLabel
	if label == "" {
		label = ssh.FingerprintSHA256(key)
	}
	dev, err := s.edgeDevice(ctx, ids, m.ID, keyLine, label)
	if err != nil {
		return nil, err
	}
	switch dev.Status {
	case domain.DeviceApproved:
	case domain.DevicePending:
		return nil, &ssh.BannerError{Err: errors.New("sshd: device pending approval"), Message: pendingDeviceBanner(dev)}
	default:
		return nil, &ssh.BannerError{
			Err:     errors.New("sshd: device revoked"),
			Message: fmt.Sprintf("device %q was revoked on this server\n", dev.Label),
		}
	}
	if err := ids.TouchDevice(ctx, dev.ID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("sshd: edge auth: %w", err)
	}
	slog.Info("sshd: edge auth", "member", m.ID, "device", dev.ID,
		"provider", grant.Account.Provider, "subject", grant.Account.Subject, "pending", m.Pending)
	return &ssh.Permissions{Extensions: map[string]string{
		memberIDExtension:  string(m.ID),
		deviceIDExtension:  string(dev.ID),
		deviceKeyExtension: keyLine,
	}}, nil
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
	line := edgeproto.DeviceKeyLine(key)
	dev, err := ids.GetDeviceByCredential(ctx, line)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sshd: resolve device key: %w", err)
	}
	switch {
	case dev.Kind != domain.DeviceSSH:
		return nil, false, nil
	case dev.Status == domain.DevicePending:
		return nil, true, &ssh.BannerError{Err: errors.New("sshd: device pending approval"), Message: pendingDeviceBanner(dev)}
	case dev.Status != domain.DeviceApproved:
		return nil, true, &ssh.BannerError{
			Err:     errors.New("sshd: device revoked"),
			Message: fmt.Sprintf("device %q was revoked on this server\n", dev.Label),
		}
	}
	return &ssh.Permissions{Extensions: map[string]string{
		memberIDExtension:  string(dev.Member),
		deviceIDExtension:  string(dev.ID),
		deviceKeyExtension: line,
	}}, true, nil
}

func pendingDeviceBanner(dev *domain.Device) string {
	return fmt.Sprintf("device %q is waiting for approval. From a device this account already uses, or as an admin, run:\n"+
		"  aether device approve %s\nor on the server:\n  sudo aether-server device approve %s\n",
		dev.Label, dev.ApprovalCode, dev.ApprovalCode)
}

// edgeMember returns the member bound to account. An unbound account
// matching an open invitation becomes that invitation's member.
func (s *Server) edgeMember(ctx context.Context, ids store.IdentityStore, account edgeproto.Account) (*domain.Member, error) {
	m, err := ids.GetMemberByIdentity(ctx, account.Provider, account.Subject)
	if !errors.Is(err, store.ErrNotFound) {
		return m, err
	}
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
	m, err = s.acceptInvitation(ctx, ids, invs[0], account)
	if errors.Is(err, store.ErrNotFound) {
		// Another account consumed the invitation first.
		return nil, notMember
	}
	return m, err
}

// acceptInvitation binds account through inv, creating the member unless
// inv links an existing one.
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
// for member on first contact.
func (s *Server) edgeDevice(ctx context.Context, ids store.IdentityStore, member domain.MemberID, keyLine, label string) (*domain.Device, error) {
	dev, err := ids.GetDeviceByCredential(ctx, keyLine)
	if errors.Is(err, store.ErrNotFound) {
		dev = &domain.Device{Member: member, Kind: domain.DeviceSSH, Credential: keyLine, Label: label}
		err = ids.RegisterDevice(ctx, dev, !s.cfg.EdgeDeviceAutoApprove)
		if errors.Is(err, store.ErrConflict) {
			// A concurrent first contact of the same device registered it.
			dev, err = ids.GetDeviceByCredential(ctx, keyLine)
		} else if err == nil {
			slog.Info("sshd: edge device registered", "member", member, "device", dev.ID,
				"status", dev.Status, "key", fingerprintOf(keyLine))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sshd: edge device: %w", err)
	}
	if dev.Member != member || dev.Kind != domain.DeviceSSH {
		return nil, &ssh.BannerError{
			Err:     errors.New("sshd: device key registered to another member"),
			Message: "this device key is registered to another member of this server\n",
		}
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

// ClaimByEdge makes account the admin of this server after the edge agent
// has checked a claim code. On a server with no member it creates that
// admin. On a server with members, the account must already be an admin's
// identity (a repeated claim), or match an admin's open link from
// member.identity.link, which it then consumes: an admin who joined by key
// or tailnet links the edge account first, then claims. Anything else is
// refused with edgeproto.RefusalClaimed.
func (s *Server) ClaimByEdge(ctx context.Context, account edgeproto.Account) (domain.Member, error) {
	if err := account.Validate(); err != nil {
		return domain.Member{}, fmt.Errorf("sshd: claim: %w", err)
	}
	ids, err := s.identityStore()
	if err != nil {
		return domain.Member{}, err
	}
	s.registerMu.Lock()
	m, err := s.claim(ctx, ids, account)
	s.registerMu.Unlock()
	if err != nil {
		return domain.Member{}, err
	}
	if m.Role != domain.RoleAdmin {
		return domain.Member{}, edgeproto.RefusalClaimed
	}
	s.notifyDirectory()
	slog.Info("sshd: claimed through the edge", "member", m.ID,
		"provider", account.Provider, "subject", account.Subject)
	return *m, nil
}

// claim runs under registerMu, which member.role and member.remove also
// hold, so the admin checked here stays an admin until the claim is done.
func (s *Server) claim(ctx context.Context, ids store.IdentityStore, account edgeproto.Account) (*domain.Member, error) {
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
	var entries []edgeproto.DirectoryEntry
	for _, id := range identities {
		// A member removed between the two reads has no role and is left out.
		if role, ok := roles[id.Member]; ok {
			entries = append(entries, edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember,
				Provider: id.Provider, Subject: id.Subject, Role: role})
		}
	}
	now := time.Now()
	for _, inv := range invs {
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

// CloseEdgeDevice closes every live connection authenticated with
// deviceKey, an authorized_keys line. The edge agent calls it when the edge
// reports that device's token revoked; the device itself stays approved on
// this server.
func (s *Server) CloseEdgeDevice(deviceKey string) {
	key, err := edgeproto.ParseDeviceKey(deviceKey)
	if err != nil {
		slog.Warn("sshd: close edge device: unparsable key", "error", err)
		return
	}
	line := edgeproto.DeviceKeyLine(key)
	s.closeConns(func(id connIdentity) bool { return id.deviceKey == line })
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

// checkConnIdentity re-reads that the member exists and, on a relayed
// connection, that its device is still approved.
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
	if dev.Status != domain.DeviceApproved {
		return fmt.Errorf("sshd: device %s is %s", id.device, dev.Status)
	}
	return nil
}
