package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (s *Scheduler) retainDevelopmentArtifacts(ctx context.Context, id domain.RunID, principal control.Principal, raw json.RawMessage, authorize func() error) (any, error) {
	var req protocol.DevArtifactRetainParams
	if err := decodeDevelopment(raw, &req); err != nil {
		return nil, err
	}
	if len(req.ArtifactIDs) == 0 {
		return nil, errors.New("select at least one capture to retain")
	}
	currentAuthority := func() error {
		if err := authorize(); err != nil {
			return err
		}
		run, err := s.cfg.Store.GetRun(ctx, id)
		if err != nil {
			return err
		}
		actor := principal.MemberID
		if principal.Kind == control.PrincipalRunAgent {
			actor = run.MemberID
		}
		member, err := s.cfg.Store.GetMember(ctx, actor)
		if err != nil {
			return err
		}
		workspace, err := s.cfg.Store.GetWorkspace(ctx, run.WorkspaceID)
		if err != nil {
			return err
		}
		return permissions.Check(permissions.Steer,
			permissions.Actor{ID: member.ID, Role: member.Role},
			permissions.Target{Workspace: run.WorkspaceID, Owner: run.MemberID, Protected: run.Protected, SteerOthers: workspace.SteerOthers})
	}
	if err := currentAuthority(); err != nil {
		return nil, err
	}
	if _, err := s.ResolveLiveRun(ctx, id, false); err != nil {
		return nil, err
	}
	s.mu.Lock()
	service := s.evidence
	s.mu.Unlock()
	if service == nil {
		return nil, errors.New("development capture retention unavailable")
	}
	origin := store.EvidenceOrigin{Kind: store.EvidenceOriginHuman, ID: string(principal.MemberID)}
	if principal.Kind == control.PrincipalRunAgent {
		origin = store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(id)}
	}
	// This is ordinary retained evidence. Report --evidence-ref consumes the
	// same packet ID; neither browser control ownership nor conflict policy
	// changes the authenticated principal recorded at this boundary.
	packet, err := service.Capture(ctx, evidence.Request{
		RunID: id, Origin: origin, Trigger: store.EvidenceReport,
		IdempotencyKey: req.IdempotencyKey, ArtifactIDs: req.ArtifactIDs,
		VerificationNotes: req.VerificationNotes, Provenance: "development-capture-selection",
		Authorize: currentAuthority,
	})
	if err != nil {
		return nil, err
	}
	return protocol.DevArtifactRetainResult{PacketID: packet.ID}, nil
}

// OpenEvidenceArtifact is only an internal source for evidence.Capture. The
// run-scoped capture store validates identity before opening immutable bytes;
// an open descriptor remains readable if transient cleanup unlinks the source.
func (s *Scheduler) OpenEvidenceArtifact(ctx context.Context, id domain.RunID, handle string) (protocol.DevArtifact, io.ReadCloser, error) {
	var artifact protocol.DevArtifact
	if err := ctx.Err(); err != nil {
		return artifact, nil, err
	}
	d := s.developmentState()
	d.captures.Lock()
	defer d.captures.Unlock()
	dir, err := s.captureDir(id)
	if err != nil {
		return artifact, nil, err
	}
	artifact, err = readCapture(dir, id, handle)
	if err != nil {
		return artifact, nil, err
	}
	reader, err := os.Open(filepath.Join(dir, handle+".png"))
	if err != nil {
		return artifact, nil, err
	}
	return artifact, reader, nil
}
