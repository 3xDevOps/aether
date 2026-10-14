package sshd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodMemberMergeStart, (*Server).memberMergeStart)
	registerMethod(protocol.MethodMemberMergePreview, (*Server).memberMergePreview)
	registerMethod(protocol.MethodMemberMergePropose, (*Server).memberMergePropose)
	registerMethod(protocol.MethodMemberMergePending, (*Server).memberMergePending)
	registerMethod(protocol.MethodMemberMergeAccept, (*Server).memberMergeAccept)
	registerMethod(protocol.MethodMemberMergeCancel, (*Server).memberMergeCancel)
	registerMethod(protocol.MethodMemberMergeList, (*Server).memberMergeList)
}

// mergeCodeTTL bounds a code until it is entered, and a proposal until it
// is accepted.
const mergeCodeTTL = 10 * time.Minute

// mergeCodeBytes is 80 bits, 16 base32 characters.
const mergeCodeBytes = 10

func (s *Server) mergeStore() (store.MergeStore, *protocol.Error) {
	ms, ok := s.cfg.Store.(store.MergeStore)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the store does not keep member merges"}
	}
	return ms, nil
}

// newMergeCode returns a code grouped for typing, XXXX-XXXX-XXXX-XXXX.
func newMergeCode() (string, error) {
	var b [mergeCodeBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("merge code: %w", err)
	}
	raw := base32.StdEncoding.EncodeToString(b[:])
	return raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16], nil
}

// mergeCodeHash is the stored form of code as a person typed it: any case,
// with or without dashes and spaces.
func mergeCodeHash(code string) (string, *protocol.Error) {
	norm := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if _, err := base32.StdEncoding.DecodeString(norm); err != nil || len(norm) != 16 {
		return "", invalidParams("code must be the 16 characters aether member merge start printed, like ABCD-EFGH-IJKL-MNOP")
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:]), nil
}

func mergeError(err error) *protocol.Error {
	var refused *store.MergeRefusal
	if errors.As(err, &refused) {
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: refused.Message}
	}
	return rpcError(err)
}

// mergeCaller checks the caller of a merge step and returns the store:
// under either policy, no step is taken from a device no person approved.
func (s *Server) mergeCaller(ctx context.Context, method string) (store.MergeStore, *protocol.Error) {
	if perr := s.requireApprovedCaller(ctx, method, true); perr != nil {
		return nil, perr
	}
	return s.mergeStore()
}

func (s *Server) memberMergeStart(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeCaller(ctx, protocol.MethodMemberMergeStart)
	if perr != nil {
		return nil, perr
	}
	code, err := newMergeCode()
	if err != nil {
		return nil, rpcError(err)
	}
	hash, perr := mergeCodeHash(code)
	if perr != nil {
		return nil, perr
	}
	now := time.Now().UTC()
	expires := now.Add(mergeCodeTTL)
	if err := ms.CreateMergeCode(ctx, member, hash, now, expires); err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: merge code minted", "member", member, "expires", expires)
	return protocol.MemberMergeStartResult{Code: code, ExpiresAt: expires.Format(time.RFC3339)}, nil
}

// memberMergePreview and the other steps that read the plan hold
// registerMu, which member.role, member.remove and every sign-in that
// creates or binds a member hold, so the plan's members cannot change
// under it.
func (s *Server) memberMergePreview(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeCaller(ctx, protocol.MethodMemberMergePreview)
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergePreviewParams](params)
	if perr != nil {
		return nil, perr
	}
	hash, perr := mergeCodeHash(p.Code)
	if perr != nil {
		return nil, perr
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	plan, err := ms.PlanMerge(ctx, hash, member, string(s.cfg.EdgeAccess), time.Now())
	if err != nil {
		return nil, mergeError(err)
	}
	return s.planToWire(plan)
}

func (s *Server) memberMergePropose(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeCaller(ctx, protocol.MethodMemberMergePropose)
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergeProposeParams](params)
	if perr != nil {
		return nil, perr
	}
	hash, perr := mergeCodeHash(p.Code)
	if perr != nil {
		return nil, perr
	}
	switch {
	case p.KeepSettingsOf == "" || p.PlanDigest == "":
		return nil, invalidParams("keep_settings_of and plan_digest are required; preview the merge with " + protocol.MethodMemberMergePreview)
	case !p.DiscardAck:
		return nil, invalidParams("discard_ack must be true: the other member's settings the preview lists are discarded")
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	now := time.Now().UTC()
	pm, err := ms.ProposeMerge(ctx, store.MergeProposal{CodeHash: hash, Proposer: member, Keep: domain.MemberID(p.KeepSettingsOf),
		Digest: p.PlanDigest, KeepAdmin: p.KeepAdmin, Policy: string(s.cfg.EdgeAccess), Now: now, Expires: now.Add(mergeCodeTTL)})
	if err != nil {
		return nil, mergeError(err)
	}
	slog.Info("sshd: merge proposed", "proposal", pm.ProposalID, "minted_by", pm.Minted, "proposer", member,
		"keep", pm.Keep, "keep_admin", p.KeepAdmin)
	return s.proposalToWire(pm)
}

func (s *Server) memberMergePending(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeCaller(ctx, protocol.MethodMemberMergePending)
	if perr != nil {
		return nil, perr
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	pending, err := ms.PendingMerges(ctx, member, string(s.cfg.EdgeAccess), time.Now())
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.MemberMergePendingResult{Proposals: []protocol.MemberMergeProposal{}}
	for _, pm := range pending {
		w, perr := s.proposalToWire(pm)
		if perr != nil {
			return nil, perr
		}
		out.Proposals = append(out.Proposals, w)
	}
	return out, nil
}

// memberMergeAccept merges under registerMu, so the last-admin check
// holds, and authorizationMu, so no run, swarm or terminal is admitted for
// either member while their rows move. The absorbed member's connections
// close afterwards, the caller's once it has the result: they reconnect
// as the survivor.
func (s *Server) memberMergeAccept(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodMemberMergeAccept
	ms, perr := s.mergeCaller(ctx, method)
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergeAcceptParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.ProposalID == "" || p.PlanDigest == "" {
		return nil, invalidParams("proposal_id and plan_digest are required; read them with " + protocol.MethodMemberMergePending)
	}
	if done, err := ms.MergeOfProposal(ctx, p.ProposalID); err == nil && done.SurvivorID == member {
		return s.mergeResult(ctx, done)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, rpcError(err)
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	policy := string(s.cfg.EdgeAccess)
	pending, err := ms.PendingMerges(ctx, member, policy, time.Now())
	if err != nil {
		return nil, rpcError(err)
	}
	var absorbed, other domain.MemberID
	for _, pm := range pending {
		if pm.ProposalID == p.ProposalID {
			absorbed, other = pm.Minted, pm.Proposer
			if pm.Keep == pm.Minted {
				absorbed, other = pm.Proposer, pm.Minted
			}
		}
	}
	accept := store.MergeAccept{ProposalID: p.ProposalID, Caller: member, Digest: p.PlanDigest, KeepAdmin: p.KeepAdmin,
		Policy: policy, Now: time.Now().UTC(), Archive: s.archiveHome}
	accept.ConfirmedByKind, accept.ConfirmedBy = s.connDescription(ctx)
	if edge := s.currentEdgeOwner(); edge != nil {
		owner, oerr := edge.Owner()
		if oerr != nil {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": read the server's owner at its edge: " + oerr.Error()}
		}
		if owner != nil {
			accept.OwnerProvider, accept.OwnerSubject = owner.Provider, owner.Subject
		}
	}
	if absorbed == "" {
		if _, err = ms.AcceptMerge(ctx, accept); err == nil {
			err = fmt.Errorf("%s: proposal %s is not open for member %s", method, p.ProposalID, member)
		}
		return nil, mergeError(err)
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	devs, err := ids.ListDevices(ctx, absorbed)
	if err != nil {
		return nil, rpcError(err)
	}
	var activeRuns []*domain.Run
	if s.cfg.Control != nil {
		if activeRuns, err = s.cfg.Store.ListActiveRuns(ctx); err != nil {
			return nil, rpcError(err)
		}
	}
	var merge *store.MemberMerge
	apply := func() error {
		if merge != nil {
			return nil
		}
		return s.cfg.Runs.WithStoppedTerminal(ctx, absorbed, func() error {
			if s.cfg.Homes != nil {
				unlock := s.cfg.Homes.LockCaches(absorbed)
				defer unlock()
				if err := s.cfg.Homes.EnsureCacheMetadata(absorbed, memberhome.CachePoolTerminal); err != nil {
					return fmt.Errorf("%s: keep the absorbed member's caches discoverable for cleanup: %w", method, err)
				}
			}
			var err error
			merge, err = ms.AcceptMerge(ctx, accept)
			return err
		})
	}
	// Only a run either member controls has a lease the merge changes:
	// the absorbed member's ends, and the survivor's role may drop.
	for _, run := range activeRuns {
		if holder, ok := s.cfg.Control.Status(string(run.ID)); !ok || (holder.MemberID != absorbed && holder.MemberID != other) {
			continue
		}
		if _, err := s.cfg.Control.AdmitRevoke(string(run.ID), control.RevocationPermission, apply); err != nil {
			return nil, mergeError(err)
		}
	}
	if err := apply(); err != nil {
		return nil, mergeError(err)
	}
	s.afterMerge(ctx, member, merge, devs)
	return s.mergeResult(ctx, merge)
}

// afterMerge closes the absorbed member's connections, the caller's once
// it has the result, and publishes the merge and the devices that moved.
// A demoted admin's invite codes need nothing here: joining checks the
// issuer's role.
func (s *Server) afterMerge(ctx context.Context, caller domain.MemberID, merge *store.MemberMerge, devs []*domain.Device) {
	closeAbsorbed := func() { s.closeMemberConns(merge.AbsorbedID) }
	if caller != merge.AbsorbedID || !deferUntilResponded(ctx, closeAbsorbed) {
		closeAbsorbed()
	}
	s.notifyDirectory()
	ev := events.Event{WorkspaceID: events.ServerScope, ActorID: merge.SurvivorID,
		Payload: events.MemberMergedPayload{MergeID: merge.ID, SurvivorID: merge.SurvivorID, AbsorbedID: merge.AbsorbedID}}
	if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
		slog.Warn("sshd: publish member.merged", "merge", merge.ID, "error", err)
	}
	for _, d := range devs {
		s.publishDevice(ctx, merge.SurvivorID, d.ID, d.Status)
	}
	slog.Info("sshd: members merged", "merge", merge.ID, "survivor", merge.SurvivorID, "absorbed", merge.AbsorbedID,
		"role", merge.RoleAfter, "confirmed_by", merge.ConfirmedByKind, "archive", merge.ArchivePath)
}

func (s *Server) memberMergeCancel(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeCaller(ctx, protocol.MethodMemberMergeCancel)
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergeCancelParams](params)
	if perr != nil {
		return nil, perr
	}
	n, err := ms.CancelMerge(ctx, member, p.ProposalID)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemberMergeCancelResult{Cancelled: n}, nil
}

// archiveHome moves the absorbed member's home to <MergedDir>/<merge
// id>/home, beside a file naming the member so RecoverMergeArchives can
// put it back if the server stops before the merge commits.
func (s *Server) archiveHome(mergeID string, absorbed domain.MemberID) (string, func(), error) {
	if s.cfg.Homes == nil || s.cfg.MergedDir == "" {
		return "", nil, nil
	}
	dir := filepath.Join(s.cfg.MergedDir, mergeID)
	marker, home := filepath.Join(dir, mergeMemberFile), filepath.Join(dir, "home")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("sshd: merge archive: %w", err)
	}
	clear := func() { _ = os.Remove(marker); _ = os.Remove(dir) }
	if err := os.WriteFile(marker, []byte(absorbed), 0o600); err != nil {
		clear()
		return "", nil, fmt.Errorf("sshd: merge archive: %w", err)
	}
	moved, err := s.cfg.Homes.Archive(absorbed, home)
	if err != nil || !moved {
		clear()
		return "", nil, err
	}
	return dir, func() {
		if err := s.cfg.Homes.Unarchive(absorbed, home); err != nil {
			slog.Error("sshd: merge failed and its archived home could not be put back; aether-server restores it at its next start",
				"member", absorbed, "path", home, "error", err)
			return
		}
		clear()
	}, nil
}

// mergeMemberFile names the absorbed member in a merge's archive.
const mergeMemberFile = "member"

// RecoverMergeArchives puts back the home of each merge that moved it and
// never committed: the server stopped in between. Call it before Serve.
func (s *Server) RecoverMergeArchives(ctx context.Context) error {
	if s.cfg.Homes == nil || s.cfg.MergedDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.cfg.MergedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sshd: recover merge archives: %w", err)
	}
	ms, ok := s.cfg.Store.(store.MergeStore)
	if !ok {
		return nil
	}
	merges, err := ms.ListMerges(ctx, "")
	if err != nil {
		return fmt.Errorf("sshd: recover merge archives: %w", err)
	}
	recorded := make(map[string]bool, len(merges))
	for _, m := range merges {
		recorded[m.ID] = true
	}
	for _, e := range entries {
		if recorded[e.Name()] {
			continue
		}
		dir := filepath.Join(s.cfg.MergedDir, e.Name())
		member, err := os.ReadFile(filepath.Join(dir, mergeMemberFile))
		if err != nil {
			return fmt.Errorf("sshd: recover merge archive %s: %w", dir, err)
		}
		home := filepath.Join(dir, "home")
		if _, err := os.Lstat(home); err == nil {
			if err := s.cfg.Homes.Unarchive(domain.MemberID(member), home); err != nil {
				return fmt.Errorf("sshd: recover merge archive %s: %w", dir, err)
			}
			slog.Warn("sshd: put back the home of a merge that never committed", "member", string(member), "archive", dir)
		}
		if err := os.Remove(filepath.Join(dir, mergeMemberFile)); err != nil {
			return fmt.Errorf("sshd: recover merge archive %s: %w", dir, err)
		}
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("sshd: recover merge archive %s: %w", dir, err)
		}
	}
	return nil
}

// connDescription names the connection a call came in on for the merge
// audit record.
func (s *Server) connDescription(ctx context.Context) (kind, description string) {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	switch {
	case !ok:
		return "dashboard", "the tailnet dashboard"
	case id.device != "":
		if ids, err := s.identityStore(); err == nil {
			if dev, err := ids.GetDevice(ctx, id.device); err == nil {
				return "edge-device", fmt.Sprintf("edge device %q (%s), %s account %s", dev.Label, dev.ID, dev.Provider, dev.Login)
			}
		}
		return "edge-device", "edge device " + string(id.device)
	}
	return "ssh", "SSH connection of member " + string(id.member)
}

func (s *Server) mergeResult(ctx context.Context, merge *store.MemberMerge) (any, *protocol.Error) {
	m, err := s.cfg.Store.GetMember(ctx, merge.SurvivorID)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemberMergeAcceptResult{Merge: mergeToWire(merge), Member: protocol.MemberFromDomain(m)}, nil
}

func (s *Server) memberMergeList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	ms, perr := s.mergeStore()
	if perr != nil {
		return nil, perr
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	survivor := member
	if caller.Role == domain.RoleAdmin {
		survivor = ""
	}
	merges, err := ms.ListMerges(ctx, survivor)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.MemberMergeListResult{Merges: []protocol.MemberMerge{}}
	for _, m := range merges {
		out.Merges = append(out.Merges, mergeToWire(m))
	}
	return out, nil
}

func (s *Server) proposalToWire(pm *store.PendingMerge) (protocol.MemberMergeProposal, *protocol.Error) {
	out := protocol.MemberMergeProposal{ID: pm.ProposalID, MintedID: string(pm.Minted), ProposerID: string(pm.Proposer),
		KeepSettingsOf: string(pm.Keep), ProposerKeepAdmin: pm.ProposerKeepAdmin, PlanDigest: pm.Digest,
		ExpiresAt: pm.ExpiresAt.UTC().Format(time.RFC3339), Refusal: pm.Refusal}
	if pm.Plan != nil {
		plan, perr := s.planToWire(pm.Plan)
		if perr != nil {
			return out, perr
		}
		out.Plan = &plan
	}
	return out, nil
}

// planToWire is the plan as the two sides see it: what the decision needs,
// with no email address, device key or agent definition.
func (s *Server) planToWire(plan *store.MergePlan) (protocol.MemberMergePlan, *protocol.Error) {
	homes := map[domain.MemberID]bool{}
	for _, side := range []*store.MergeSnapshot{&plan.Minted, &plan.Proposer} {
		if s.cfg.Homes == nil {
			continue
		}
		has, err := s.cfg.Homes.HasHome(side.Member.ID)
		if err != nil {
			return protocol.MemberMergePlan{}, rpcError(err)
		}
		homes[side.Member.ID] = has
	}
	a, b := plan.Minted.Member, plan.Proposer.Member
	role := plan.Role(false, false)
	out := protocol.MemberMergePlan{
		Minted:     snapshotToWire(plan.Minted, homes[a.ID], false),
		Proposer:   snapshotToWire(plan.Proposer, homes[b.ID], false),
		Role:       string(role),
		SignIns:    []string{},
		PlanDigest: plan.Digest,
	}
	if keep := plan.Role(true, true); keep != role {
		out.RoleWithKeepAdmin = string(keep)
		out.KeepAdminRequired = plan.LeavesNoAdmin(role)
	}
	for _, side := range []store.MergeSnapshot{plan.Minted, plan.Proposer} {
		m := side.Member
		if m.TailnetLogin != "" {
			out.SignIns = append(out.SignIns, "tailnet login "+m.TailnetLogin)
		}
		if m.PublicKey != "" {
			out.SignIns = append(out.SignIns, "SSH key "+fingerprintOf(m.PublicKey))
		}
		for _, id := range side.Identities {
			out.SignIns = append(out.SignIns, fmt.Sprintf("%s account %s at the edge", id.Provider, id.Login))
		}
	}
	for _, keep := range []store.MergeSnapshot{plan.Minted, plan.Proposer} {
		other := plan.Other(keep.Member.ID)
		out.Choices = append(out.Choices, protocol.MergeChoice{KeepSettingsOf: string(keep.Member.ID),
			AbsorbedID: string(other.Member.ID), Discarded: settingsToWire(other.Settings, homes[other.Member.ID], false)})
	}
	return out, nil
}

// snapshotToWire converts one member of a plan, or with full set of an
// audit record, which also keeps git email addresses and agent
// definitions.
func snapshotToWire(snap store.MergeSnapshot, home, full bool) protocol.MergeMember {
	m := snap.Member
	out := protocol.MergeMember{ID: string(m.ID), DisplayName: m.DisplayName, Role: string(m.Role), TailnetLogin: m.TailnetLogin,
		Identities: []protocol.MergeIdentity{}, Devices: []protocol.MergeDevice{}, Runs: snap.Runs, Schedules: snap.Schedules,
		Settings: settingsToWire(snap.Settings, home, full)}
	if m.PublicKey != "" {
		out.PublicKeyFingerprint = fingerprintOf(m.PublicKey)
	}
	for _, id := range snap.Identities {
		out.Identities = append(out.Identities, protocol.MergeIdentity{Provider: id.Provider, Login: id.Login})
	}
	for _, d := range snap.Devices {
		out.Devices = append(out.Devices, protocol.MergeDevice{ID: string(d.ID), Label: d.Label, Status: string(d.Status)})
	}
	return out
}

func settingsToWire(s store.MergeSettings, home, full bool) protocol.MergeSettings {
	out := protocol.MergeSettings{DisplayName: s.DisplayName, Color: s.Color, GitName: s.GitName,
		Image: s.Image, HarnessDefinitions: []protocol.MergeHarnessDefinition{}, ProfileHeads: []protocol.MergeProfileHead{},
		AccountSharedWith: []string{}, MirrorWorkspaces: []string{}, Home: home}
	if full {
		out.GitEmail = s.GitEmail
	}
	for _, d := range s.HarnessDefinitions {
		def := protocol.MergeHarnessDefinition{Name: d.Name}
		if full {
			def.Definition = d.Definition
		}
		out.HarnessDefinitions = append(out.HarnessDefinitions, def)
	}
	for _, h := range s.ProfileHeads {
		out.ProfileHeads = append(out.ProfileHeads, protocol.MergeProfileHead{Harness: h.Harness, SnapshotID: string(h.SnapshotID)})
	}
	for _, g := range s.AccountSharedWith {
		out.AccountSharedWith = append(out.AccountSharedWith, string(g))
	}
	for _, w := range s.MirrorWorkspaces {
		out.MirrorWorkspaces = append(out.MirrorWorkspaces, string(w))
	}
	return out
}

func mergeToWire(m *store.MemberMerge) protocol.MemberMerge {
	home := m.ArchivePath != ""
	return protocol.MemberMerge{ID: m.ID, SurvivorID: string(m.SurvivorID), AbsorbedID: string(m.AbsorbedID),
		ConfirmedByKind: m.ConfirmedByKind, ConfirmedBy: m.ConfirmedBy, SurvivorRoleBefore: string(m.SurvivorRoleBefore),
		AbsorbedRoleBefore: string(m.AbsorbedRoleBefore), RoleAfter: string(m.RoleAfter),
		Discarded: settingsToWire(m.Discarded, home, true), Absorbed: snapshotToWire(m.Absorbed, home, true),
		ArchivePath: m.ArchivePath, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339)}
}
