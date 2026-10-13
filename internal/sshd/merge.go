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
	"log/slog"
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
	registerMethod(protocol.MethodMemberMergeConfirm, (*Server).memberMergeConfirm)
	registerMethod(protocol.MethodMemberMergeList, (*Server).memberMergeList)
}

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

func (s *Server) memberMergeStart(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodMemberMergeStart
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	ms, perr := s.mergeStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergeStartParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.KeepAdmin {
		if perr := s.requireAdmin(ctx, member, method+" with keep_admin"); perr != nil {
			return nil, perr
		}
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
	if err := ms.CreateMergeCode(ctx, member, hash, p.KeepAdmin, now, expires); err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: merge code minted", "member", member, "keep_admin", p.KeepAdmin, "expires", expires)
	return protocol.MemberMergeStartResult{Code: code, ExpiresAt: expires.Format(time.RFC3339)}, nil
}

func (s *Server) memberMergePreview(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if perr := s.requireApprovedCaller(ctx, protocol.MethodMemberMergePreview, false); perr != nil {
		return nil, perr
	}
	ms, perr := s.mergeStore()
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
	plan, err := ms.PlanMerge(ctx, hash, member, time.Now())
	if err != nil {
		return nil, mergeError(err)
	}
	return s.planToWire(plan)
}

// memberMergeConfirm merges under registerMu, like member.role, so the
// last-admin check holds, and authorizationMu, so no run, swarm or
// terminal is admitted for either member while their rows move. The
// absorbed member's connections close afterwards, the caller's once it
// has the result: they reconnect as the survivor.
func (s *Server) memberMergeConfirm(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodMemberMergeConfirm
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	ms, perr := s.mergeStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberMergeConfirmParams](params)
	if perr != nil {
		return nil, perr
	}
	hash, perr := mergeCodeHash(p.Code)
	if perr != nil {
		return nil, perr
	}
	done, err := ms.MergeOfCode(ctx, hash)
	if err == nil && done.SurvivorID == member {
		return s.mergeResult(ctx, done)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, rpcError(err)
	}
	switch {
	case p.KeepSettingsOf == "" || p.PlanDigest == "":
		return nil, invalidParams("keep_settings_of and plan_digest are required; preview the merge with " + protocol.MethodMemberMergePreview)
	case !p.DiscardAck:
		return nil, invalidParams("discard_ack must be true: the other member's settings the preview lists are discarded")
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	plan, err := ms.PlanMerge(ctx, hash, member, time.Now())
	if err != nil {
		return nil, mergeError(err)
	}
	keep := domain.MemberID(p.KeepSettingsOf)
	if plan.Side(keep) == nil {
		return nil, invalidParams(fmt.Sprintf("keep_settings_of must be %s or %s, the two members this code merges",
			plan.Minted.Member.ID, plan.Confirming.Member.ID))
	}
	absorbed := plan.Other(keep).Member.ID
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
	kind, by := s.connDescription(ctx)
	confirm := store.MergeConfirm{CodeHash: hash, Caller: member, Keep: keep, Digest: p.PlanDigest, KeepAdmin: p.KeepAdmin,
		ConfirmedByKind: kind, ConfirmedBy: by, Now: time.Now().UTC(), Archive: s.archiveHome}
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
			merge, err = ms.ConfirmMerge(ctx, confirm)
			return err
		})
	}
	for _, run := range activeRuns {
		if _, err := s.cfg.Control.AdmitRevoke(string(run.ID), control.RevocationPermission, apply); err != nil {
			return nil, mergeError(err)
		}
	}
	if err := apply(); err != nil {
		return nil, mergeError(err)
	}
	closeAbsorbed := func() { s.closeMemberConns(merge.AbsorbedID) }
	if member != merge.AbsorbedID || !deferUntilResponded(ctx, closeAbsorbed) {
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
		"role", merge.RoleAfter, "confirmed_by", kind, "archive", merge.ArchivePath)
	return s.mergeResult(ctx, merge)
}

// archiveHome moves the absorbed member's home to <MergedDir>/<merge id>/home.
func (s *Server) archiveHome(mergeID string, absorbed domain.MemberID) (string, func(), error) {
	if s.cfg.Homes == nil || s.cfg.MergedDir == "" {
		return "", nil, nil
	}
	dir := filepath.Join(s.cfg.MergedDir, mergeID)
	home := filepath.Join(dir, "home")
	moved, err := s.cfg.Homes.Archive(absorbed, home)
	if err != nil || !moved {
		return "", nil, err
	}
	return dir, func() {
		if err := s.cfg.Homes.Unarchive(absorbed, home); err != nil {
			slog.Error("sshd: merge failed and its archived home could not be put back", "member", absorbed, "path", home, "error", err)
		}
	}, nil
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
	return protocol.MemberMergeConfirmResult{Merge: mergeToWire(merge), Member: protocol.MemberFromDomain(m)}, nil
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

func (s *Server) planToWire(plan *store.MergePlan) (any, *protocol.Error) {
	homes := map[domain.MemberID]bool{}
	for _, side := range []*store.MergeSnapshot{&plan.Minted, &plan.Confirming} {
		if s.cfg.Homes == nil {
			continue
		}
		has, err := s.cfg.Homes.HasHome(side.Member.ID)
		if err != nil {
			return nil, rpcError(err)
		}
		homes[side.Member.ID] = has
	}
	a, b := plan.Minted.Member, plan.Confirming.Member
	role := store.MergedRole(a.Role, b.Role, false)
	out := protocol.MemberMergePlan{
		Minted:     snapshotToWire(plan.Minted, homes[a.ID]),
		Confirming: snapshotToWire(plan.Confirming, homes[b.ID]),
		Role:       string(role),
		SignIns:    []string{},
		PlanDigest: plan.Digest,
	}
	if keep := store.MergedRole(a.Role, b.Role, true); keep != role {
		out.RoleWithKeepAdmin = string(keep)
		out.KeepAdminRequired = plan.LeavesNoAdmin(role)
		out.KeepAdminConsented = plan.KeepAdminConsented()
	}
	for _, side := range []store.MergeSnapshot{plan.Minted, plan.Confirming} {
		m := side.Member
		if m.TailnetLogin != "" {
			out.SignIns = append(out.SignIns, "tailnet login "+m.TailnetLogin)
		}
		if m.PublicKey != "" {
			out.SignIns = append(out.SignIns, "SSH key "+fingerprintOf(m.PublicKey))
		}
		for _, id := range side.Identities {
			out.SignIns = append(out.SignIns, fmt.Sprintf("%s account %s at the edge", id.Provider, identityName(id)))
		}
	}
	for _, keep := range []store.MergeSnapshot{plan.Minted, plan.Confirming} {
		other := plan.Other(keep.Member.ID)
		out.Choices = append(out.Choices, protocol.MergeChoice{KeepSettingsOf: string(keep.Member.ID),
			AbsorbedID: string(other.Member.ID), Discarded: settingsToWire(other.Settings, homes[other.Member.ID])})
	}
	return out, nil
}

func identityName(id *domain.Identity) string {
	switch {
	case id.Login != "":
		return id.Login
	case id.Email != "":
		return id.Email
	}
	return id.Subject
}

func snapshotToWire(snap store.MergeSnapshot, home bool) protocol.MergeMember {
	m := snap.Member
	out := protocol.MergeMember{ID: string(m.ID), DisplayName: m.DisplayName, Role: string(m.Role), TailnetLogin: m.TailnetLogin,
		Identities: []protocol.Identity{}, Devices: []protocol.Device{}, Runs: snap.Runs, Schedules: snap.Schedules,
		Settings: settingsToWire(snap.Settings, home)}
	if m.PublicKey != "" {
		out.PublicKeyFingerprint = fingerprintOf(m.PublicKey)
	}
	for _, id := range snap.Identities {
		out.Identities = append(out.Identities, protocol.Identity{Provider: id.Provider, Subject: id.Subject,
			Login: id.Login, Email: id.Email, CreatedAt: id.CreatedAt.UTC().Format(time.RFC3339)})
	}
	for _, d := range snap.Devices {
		out.Devices = append(out.Devices, deviceToWire(d))
	}
	return out
}

func settingsToWire(s store.MergeSettings, home bool) protocol.MergeSettings {
	out := protocol.MergeSettings{DisplayName: s.DisplayName, Color: s.Color, GitName: s.GitName, GitEmail: s.GitEmail,
		Image: s.Image, HarnessDefinitions: []protocol.MergeHarnessDefinition{}, ProfileHeads: []protocol.MergeProfileHead{},
		AccountSharedWith: []string{}, MirrorWorkspaces: []string{}, Home: home}
	for _, d := range s.HarnessDefinitions {
		out.HarnessDefinitions = append(out.HarnessDefinitions, protocol.MergeHarnessDefinition{Name: d.Name, Definition: d.Definition})
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
	return protocol.MemberMerge{ID: m.ID, SurvivorID: string(m.SurvivorID), AbsorbedID: string(m.AbsorbedID),
		ConfirmedByKind: m.ConfirmedByKind, ConfirmedBy: m.ConfirmedBy, SurvivorRoleBefore: string(m.SurvivorRoleBefore),
		AbsorbedRoleBefore: string(m.AbsorbedRoleBefore), RoleAfter: string(m.RoleAfter),
		Discarded: settingsToWire(m.Discarded, m.ArchivePath != ""), Absorbed: snapshotToWire(m.Absorbed, m.ArchivePath != ""),
		ArchivePath: m.ArchivePath, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339)}
}
