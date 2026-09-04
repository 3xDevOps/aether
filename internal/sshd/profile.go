package sshd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodProfilePush, (*Server).profilePush)
	registerMethod(protocol.MethodProfileStatus, (*Server).profileStatus)
	registerMethod(protocol.MethodProfileRollback, (*Server).profileRollback)
}

// ProfileService is the sshd seam for agent-profile snapshots. It
// deliberately omits PinRun: these handlers never select or mutate a run pin.
type ProfileService interface {
	Put(ctx context.Context, member, harness string, files []profile.File) (domain.ProfileSnapshot, error)
	Get(ctx context.Context, id domain.ProfileSnapshotID) (domain.ProfileSnapshot, []profile.File, error)
	Latest(ctx context.Context, member, harness string) (domain.ProfileSnapshot, error)
	List(ctx context.Context, member, harness string) ([]domain.ProfileSnapshot, error)
	Rollback(ctx context.Context, member, harness string, id domain.ProfileSnapshotID) error
	Materialize(ctx context.Context, id domain.ProfileSnapshotID, destDir string) error
}

func (s *Server) profilePush(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Profiles == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "profile service not configured"}
	}
	p, perr := decodeParams[protocol.ProfilePushParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" {
		return nil, invalidParams("harness is required")
	}
	if len(p.AllowSecret) > 0 && p.WorkspaceID == "" {
		return nil, invalidParams("--allow-secret requires workspace_id")
	}
	if p.WorkspaceID != "" {
		if _, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID)); err != nil {
			return nil, profileError(err)
		}
	}
	files, err := assemblePushFiles(ctx, s.cfg.Profiles, string(member), p)
	if err != nil {
		return nil, profileError(err)
	}
	allow := map[string]bool{}
	for _, a := range p.AllowSecret {
		if a != "" {
			allow[strings.ReplaceAll(a, "\\", "/")] = true
			allow[a] = true
		}
	}
	if err = profile.ScanFiles(files, allow); err != nil {
		return nil, profileError(err)
	}
	snap, err := s.cfg.Profiles.Put(ctx, string(member), p.Harness, files)
	if err != nil {
		return nil, profileError(err)
	}

	if err := s.materializeProfile(ctx, member, p.Harness, snap); err != nil {
		return nil, profileError(err)
	}
	if p.WorkspaceID != "" && len(p.AllowSecret) > 0 {
		msg := "profile.push --allow-secret: " + strings.Join(p.AllowSecret, ", ")
		if _, pubErr := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: domain.WorkspaceID(p.WorkspaceID),
			ActorID:     member,
			Payload: events.TimelinePayload{
				Kind:    events.TimelineNote,
				Message: msg,
			},
		}); pubErr != nil {
			return nil, rpcError(pubErr)
		}
	}
	return protocol.ProfilePushResult{Snapshot: protocol.ProfileSnapshotFromDomain(snap)}, nil
}

func (s *Server) profileStatus(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Profiles == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "profile service not configured"}
	}
	p, perr := decodeParams[protocol.ProfileStatusParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" {
		return nil, invalidParams("harness is required")
	}
	listed, err := s.cfg.Profiles.List(ctx, string(member), p.Harness)
	if err != nil {
		return nil, profileError(err)
	}
	out := protocol.ProfileStatusResult{
		Snapshots: make([]protocol.ProfileSnapshot, 0, len(listed)),
	}
	for _, snap := range listed {
		out.Snapshots = append(out.Snapshots, protocol.ProfileSnapshotFromDomain(snap))
	}
	head, err := s.cfg.Profiles.Latest(ctx, string(member), p.Harness)
	if err != nil {
		if errors.Is(err, profile.ErrNotFound) || errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		return nil, profileError(err)
	}
	wire := protocol.ProfileSnapshotFromDomain(head)
	out.Snapshot = &wire
	_, files, err := s.cfg.Profiles.Get(ctx, head.ID)
	if err != nil {
		return nil, profileError(err)
	}
	out.Files = make([]protocol.ProfileFileMeta, 0, len(files))
	for _, f := range files {
		out.Files = append(out.Files, protocol.ProfileFileMeta{
			Path:   f.Path,
			Mode:   f.Mode,
			Digest: blobDigest(f.Content),
		})
	}
	return out, nil
}

func (s *Server) profileRollback(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Profiles == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "profile service not configured"}
	}
	p, perr := decodeParams[protocol.ProfileRollbackParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" || p.SnapshotID == "" {
		return nil, invalidParams("harness and snapshot_id are required")
	}
	if err := s.cfg.Profiles.Rollback(ctx, string(member), p.Harness, domain.ProfileSnapshotID(p.SnapshotID)); err != nil {
		return nil, profileError(err)
	}
	head, err := s.cfg.Profiles.Latest(ctx, string(member), p.Harness)
	if err != nil {
		return nil, profileError(err)
	}
	if err := s.materializeProfile(ctx, member, p.Harness, head); err != nil {
		return nil, profileError(err)
	}
	return protocol.ProfileRollbackResult{Snapshot: protocol.ProfileSnapshotFromDomain(head)}, nil
}

func (s *Server) materializeProfile(ctx context.Context, member domain.MemberID, harnessName string, snap domain.ProfileSnapshot) error {
	if s.cfg.Homes == nil {
		return nil
	}
	profileDef, shipped := harness.Lookup(harnessName)
	if !shipped {
		row, err := s.cfg.Store.GetHarnessDefinition(ctx, member, harnessName)
		if err != nil {
			return fmt.Errorf("resolve harness %q: %w", harnessName, err)
		}
		var definition harness.Definition
		if err := json.Unmarshal(row.Definition, &definition); err != nil {
			return fmt.Errorf("decode harness %q definition: %w", harnessName, err)
		}
		profileDef = definition.Profile()
	}
	if profileDef.LocalRoot == "" {
		return nil
	}
	homePath, err := s.cfg.Homes.Path(member)
	if err != nil {
		return fmt.Errorf("resolve home for member %q: %w", member, err)
	}
	destDir := filepath.Join(homePath, filepath.FromSlash(harness.HomeRelative(profileDef.LocalRoot)))
	if err := s.cfg.Profiles.Materialize(ctx, snap.ID, destDir); err != nil {
		return fmt.Errorf("materialize profile %s into %s: %w", snap.ID, destDir, err)
	}
	return nil

}
func assemblePushFiles(ctx context.Context, svc ProfileService, member string, p protocol.ProfilePushParams) ([]profile.File, error) {
	if len(p.Files) > 0 {
		out := make([]profile.File, 0, len(p.Files))
		for _, f := range p.Files {
			out = append(out, profile.File{Path: f.Path, Mode: f.Mode, Content: f.Content})
		}
		return out, nil
	}
	blobs := make(map[string][]byte, len(p.Blobs))
	for _, b := range p.Blobs {
		sum := blobDigest(b.Content)
		if b.Digest != "" && b.Digest != sum {
			return nil, &invalidErr{msg: "blob digest mismatch for " + b.Digest}
		}
		blobs[sum] = append([]byte(nil), b.Content...)
	}
	latest, err := svc.Latest(ctx, member, p.Harness)
	if err != nil && !errors.Is(err, profile.ErrNotFound) && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		_, files, gerr := svc.Get(ctx, latest.ID)
		if gerr != nil {
			return nil, gerr
		}
		for _, f := range files {
			d := blobDigest(f.Content)
			if _, ok := blobs[d]; !ok {
				blobs[d] = f.Content
			}
		}
	}
	out := make([]profile.File, 0, len(p.Paths))
	for _, f := range p.Paths {
		content, ok := blobs[f.Digest]
		if !ok {
			return nil, &invalidErr{msg: fmt.Sprintf("missing blob for %s (%s)", f.Path, f.Digest)}
		}
		out = append(out, profile.File{Path: f.Path, Mode: f.Mode, Content: content})
	}
	return out, nil
}

func blobDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func profileError(err error) *protocol.Error {
	var inv *invalidErr
	switch {
	case errors.As(err, &inv):
		return invalidParams(inv.msg)
	case errors.Is(err, profile.ErrDenied):
		return &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()}
	case errors.Is(err, profile.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()}
	case errors.Is(err, profile.ErrTooLarge):
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	default:
		return rpcError(err)
	}
}

type invalidErr struct{ msg string }

func (e *invalidErr) Error() string { return e.msg }
