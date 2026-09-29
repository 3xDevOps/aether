package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodMemberDeviceList, (*Server).memberDeviceList)
	registerMethod(protocol.MethodMemberDeviceApprove, (*Server).memberDeviceApprove)
	registerMethod(protocol.MethodMemberDeviceRevoke, (*Server).memberDeviceRevoke)
	registerMethod(protocol.MethodMemberInvitationCreate, (*Server).memberInvitationCreate)
	registerMethod(protocol.MethodMemberInvitationList, (*Server).memberInvitationList)
	registerMethod(protocol.MethodMemberInvitationRevoke, (*Server).memberInvitationRevoke)
	registerMethod(protocol.MethodMemberIdentityLink, (*Server).memberIdentityLink)
}

func (s *Server) rpcIdentityStore() (store.IdentityStore, *protocol.Error) {
	ids, err := s.identityStore()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	}
	return ids, nil
}

// actingOn loads the caller and allows the call when it acts on the
// caller's own member or the caller is an admin.
func (s *Server) actingOn(ctx context.Context, caller, owner domain.MemberID, method string) *protocol.Error {
	m, err := s.cfg.Store.GetMember(ctx, caller)
	if err != nil {
		return rpcError(err)
	}
	if caller != owner && m.Role != domain.RoleAdmin {
		return &protocol.Error{Code: protocol.CodeDenied,
			Message: method + ": only its own member or an admin may do this"}
	}
	return nil
}

func deviceToWire(d *domain.Device) protocol.Device {
	out := protocol.Device{
		ID: string(d.ID), MemberID: string(d.Member), Label: d.Label,
		Status: string(d.Status), Fingerprint: fingerprintOf(d.Credential),
		ApprovedBy: string(d.ApprovedBy), CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
	}
	if d.LastSeenAt != nil {
		out.LastSeenAt = d.LastSeenAt.UTC().Format(time.RFC3339)
	}
	return out
}

func invitationToWire(inv *domain.Invitation) protocol.Invitation {
	return protocol.Invitation{
		ID: string(inv.ID), Provider: inv.Provider, Login: inv.Login, Email: inv.Email,
		Role: string(inv.Role), MemberID: string(inv.Member), CreatedBy: string(inv.CreatedBy),
		CreatedAt: inv.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) memberDeviceList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	owner := member
	if caller.Role == domain.RoleAdmin {
		owner = ""
	}
	devs, err := ids.ListDevices(ctx, owner)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Device, 0, len(devs))
	for _, d := range devs {
		out = append(out, deviceToWire(d))
	}
	return protocol.MemberDeviceListResult{Devices: out}, nil
}

// memberDeviceApprove approves a device awaiting approval for its own
// member or an admin. The dispatcher already refused pending members, and
// requireApprovedCaller refuses a connection that signed in with a device
// awaiting approval, so a device never approves itself.
func (s *Server) memberDeviceApprove(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if perr := s.requireApprovedCaller(ctx, protocol.MethodMemberDeviceApprove, true); perr != nil {
		return nil, perr
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberDeviceApproveParams](params)
	if perr != nil {
		return nil, perr
	}
	dev, err := ids.GetDeviceByApprovalCode(ctx, p.Code)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("no device is waiting for approval with code %q", p.Code)}
	case errors.Is(err, store.ErrConflict):
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"approval code %q names more than one waiting device, so it approves none; approve the right one on the server with `sudo aether-server device review`", p.Code)}
	case err != nil:
		return nil, rpcError(err)
	}
	if perr := s.actingOn(ctx, member, dev.Member, protocol.MethodMemberDeviceApprove); perr != nil {
		return nil, perr
	}
	if err = ids.ApproveDevice(ctx, dev.ID, member); err != nil {
		return nil, rpcError(err)
	}
	if dev, err = ids.GetDevice(ctx, dev.ID); err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: device approved", "actor", member, "member", dev.Member, "device", dev.ID)
	return protocol.MemberDeviceResult{Device: deviceToWire(dev)}, nil
}

func (s *Server) memberDeviceRevoke(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberDeviceRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.DeviceID == "" {
		return nil, invalidParams("device_id is required")
	}
	dev, err := ids.GetDevice(ctx, domain.DeviceID(p.DeviceID))
	if err != nil {
		return nil, rpcError(err)
	}
	if perr := s.actingOn(ctx, member, dev.Member, protocol.MethodMemberDeviceRevoke); perr != nil {
		return nil, perr
	}
	if err = ids.RevokeDevice(ctx, dev.ID); err != nil {
		return nil, rpcError(err)
	}
	s.closeConns(func(id connIdentity) bool { return id.device == dev.ID })
	if dev, err = ids.GetDevice(ctx, dev.ID); err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: device revoked", "actor", member, "member", dev.Member, "device", dev.ID)
	return protocol.MemberDeviceResult{Device: deviceToWire(dev)}, nil
}

// newInvitation checks the account an invitation names exactly as the edge
// will read it in the directory, with role standing in for a link's
// member's role.
func newInvitation(provider, login, email string, role domain.Role, creator domain.MemberID) (*domain.Invitation, *protocol.Error) {
	inv := &domain.Invitation{
		Provider: provider, Login: strings.TrimSpace(login), Email: strings.TrimSpace(email),
		Role: role, CreatedBy: creator, ExpiresAt: time.Now().UTC().Add(edgeproto.InvitationTTL),
	}
	entry := edgeproto.DirectoryEntry{Kind: edgeproto.EntryInvitation, Provider: inv.Provider,
		Login: inv.Login, Email: inv.Email, Role: string(role), ExpiresAt: inv.ExpiresAt}
	if err := entry.Validate(); err != nil {
		return nil, invalidParams(err.Error() + `; give a GitHub login with provider "github", or an email with provider "github", "google" or none`)
	}
	return inv, nil
}

func (s *Server) memberInvitationCreate(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if perr := s.requireAdmin(ctx, member, protocol.MethodMemberInvitationCreate); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, protocol.MethodMemberInvitationCreate, false); perr != nil {
		return nil, perr
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberInvitationCreateParams](params)
	if perr != nil {
		return nil, perr
	}
	role := domain.Role(p.Role)
	if !role.Valid() {
		return nil, invalidParams(fmt.Sprintf("unknown role %q; want viewer, collaborator, or admin", p.Role))
	}
	inv, perr := newInvitation(p.Provider, p.Login, p.Email, role, member)
	if perr != nil {
		return nil, perr
	}
	return s.createInvitation(ctx, ids, inv, protocol.MethodMemberInvitationCreate)
}

// memberIdentityLink binds the edge account an admin names to the admin's
// own member. Nothing proves the caller holds that account, so a link, like
// an invitation, is an admin's to make: from any member it would bind
// someone else's account to theirs.
func (s *Server) memberIdentityLink(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if perr := s.requireAdmin(ctx, member, protocol.MethodMemberIdentityLink); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, protocol.MethodMemberIdentityLink, false); perr != nil {
		return nil, perr
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberIdentityLinkParams](params)
	if perr != nil {
		return nil, perr
	}
	inv, perr := newInvitation(p.Provider, p.Login, p.Email, domain.RoleAdmin, member)
	if perr != nil {
		return nil, perr
	}
	inv.Role, inv.Member = "", member
	return s.createInvitation(ctx, ids, inv, protocol.MethodMemberIdentityLink)
}

// createInvitation stores inv while its creator is an admin. member.role
// revokes a demoted admin's invitations under registerMu, so checking
// under it too keeps any invitation from outliving its creator's role.
// An invitation the edge directory has no room for is refused here: once
// the directory is over the edge's limit, it stops being pushed at all.
func (s *Server) createInvitation(ctx context.Context, ids store.IdentityStore, inv *domain.Invitation, method string) (any, *protocol.Error) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	if perr := s.requireAdmin(ctx, inv.CreatedBy, method); perr != nil {
		return nil, perr
	}
	entries, err := s.EdgeDirectory(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	if len(entries) >= edgeproto.MaxDirectoryEntries {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: the edge directory already holds %d members and open invitations, the most an edge accepts; revoke an open invitation first",
			method, len(entries))}
	}
	if err := ids.CreateInvitation(ctx, inv); err != nil {
		return nil, rpcError(err)
	}
	s.notifyDirectory()
	slog.Info("sshd: invitation created", "actor", inv.CreatedBy, "invitation", inv.ID,
		"provider", inv.Provider, "login", inv.Login, "email", inv.Email, "role", inv.Role, "link", inv.Member)
	return protocol.MemberInvitationResult{Invitation: invitationToWire(inv)}, nil
}

func (s *Server) memberInvitationList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	invs, err := ids.ListInvitations(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Invitation, 0, len(invs))
	for _, inv := range invs {
		if caller.Role == domain.RoleAdmin || inv.CreatedBy == member {
			out = append(out, invitationToWire(inv))
		}
	}
	return protocol.MemberInvitationListResult{Invitations: out}, nil
}

func (s *Server) memberInvitationRevoke(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberInvitationRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.InvitationID == "" {
		return nil, invalidParams("invitation_id is required")
	}
	inv, err := ids.GetInvitation(ctx, domain.InvitationID(p.InvitationID))
	if err != nil {
		return nil, rpcError(err)
	}
	if perr := s.actingOn(ctx, member, inv.CreatedBy, protocol.MethodMemberInvitationRevoke); perr != nil {
		return nil, perr
	}
	if err := ids.DeleteInvitation(ctx, inv.ID); err != nil {
		return nil, rpcError(err)
	}
	s.notifyDirectory()
	slog.Info("sshd: invitation revoked", "actor", member, "invitation", inv.ID)
	return struct{}{}, nil
}
