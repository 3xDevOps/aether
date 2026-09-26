package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type developmentRetentionGit struct{ workspace domain.WorkspaceID }

func (g developmentRetentionGit) CaptureEvidence(context.Context, domain.RunID, string) (gitengine.EvidenceRevision, error) {
	return gitengine.EvidenceRevision{WorkspaceID: g.workspace, Commit: testBaseCommit, Tree: testBaseCommit, RetainedRefCreated: true}, nil
}
func (developmentRetentionGit) RenderEvidence(context.Context, domain.WorkspaceID, string, int) (gitengine.Patch, error) {
	return gitengine.Patch{}, nil
}
func (developmentRetentionGit) RemoveEvidence(context.Context, domain.WorkspaceID, string) error {
	return nil
}

func TestDevelopmentRetentionUsesAuthenticatedOriginAndIndependentIdempotency(t *testing.T) {
	e, run := developmentFixture(t)
	before, err := e.sched.Capabilities(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(before, protocol.MethodDevArtifactRetain) {
		t.Fatal("advertised retention without durable evidence source")
	}
	service, err := evidence.New(evidence.Config{Store: e.db, Runs: e.db, Git: developmentRetentionGit{workspace: e.ws.ID}, Artifacts: e.sched, EvidenceDir: t.TempDir(), AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatal(err)
	}
	e.sched.UseEvidence(service)
	capabilities, err := e.sched.Capabilities(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(capabilities, protocol.MethodDevArtifactRetain) {
		t.Fatal("live durable retention not advertised")
	}
	artifact := captureFixture(t, e.sched, run.ID)
	raw, err := json.Marshal(protocol.DevArtifactRetainParams{ArtifactIDs: []string{artifact.ID}, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	agentResult, err := e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevArtifactRetain, raw)
	if err != nil {
		t.Fatal(err)
	}
	humanResult, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevArtifactRetain, raw, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	agentID := agentResult.(protocol.DevArtifactRetainResult).PacketID
	humanID := humanResult.(protocol.DevArtifactRetainResult).PacketID
	if agentID == humanID {
		t.Fatal("human and run principals shared an idempotency namespace")
	}
	for _, want := range []struct {
		id     string
		origin store.EvidenceOrigin
	}{
		{agentID, store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(run.ID)}},
		{humanID, store.EvidenceOrigin{Kind: store.EvidenceOriginHuman, ID: string(e.member.ID)}},
	} {
		packet, lookupErr := e.db.GetEvidencePacket(t.Context(), want.id)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if packet.Origin != want.origin || packet.RunID != run.ID || len(packet.Captures) != 1 || packet.Captures[0].ID != artifact.ID {
			t.Fatalf("retained evidence has incorrect authenticated provenance: %#v", packet)
		}
	}
	dir, err := e.sched.captureDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removeErr := os.RemoveAll(dir); removeErr != nil {
		t.Fatal(removeErr)
	}
	agentRetry, err := e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevArtifactRetain, raw)
	if err != nil || agentRetry.(protocol.DevArtifactRetainResult).PacketID != agentID {
		t.Fatalf("authenticated agent retry after transient cleanup: %v, %v", agentRetry, err)
	}
	humanRetry, err := e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}, protocol.MethodDevArtifactRetain, raw, func() error { return nil })
	if err != nil || humanRetry.(protocol.DevArtifactRetainResult).PacketID != humanID {
		t.Fatalf("authenticated human retry after transient cleanup: %v, %v", humanRetry, err)
	}
	var forged map[string]any
	if decodeErr := json.Unmarshal(raw, &forged); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	forged["origin"] = map[string]string{"kind": "human", "id": string(e.member.ID)}
	forgedRaw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, forgedErr := e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevArtifactRetain, forgedRaw); forgedErr == nil {
		t.Fatal("agent selected human evidence origin")
	}
}

type developmentRetentionBoundary struct {
	entered chan struct{}
	release chan struct{}
}

func (b *developmentRetentionBoundary) LastSeq(ctx context.Context) (uint64, error) {
	close(b.entered)
	select {
	case <-b.release:
		return 1, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestDevelopmentRetentionWithdrawnBeforePublication(t *testing.T) {
	for _, kind := range []control.PrincipalKind{control.PrincipalMember, control.PrincipalRunAgent} {
		changes := []string{"role", "pending-membership", "account"}
		if kind == control.PrincipalMember {
			changes = append(changes, "workspace-steer", "removed-membership")
		}
		for _, change := range changes {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				e, run := developmentFixture(t)
				actor := e.member
				if kind == control.PrincipalMember {
					actor = &domain.Member{DisplayName: "Sharing caller", TailnetLogin: "caller@example.test", Role: domain.RoleCollaborator}
					if err := e.db.CreateMember(t.Context(), actor); err != nil {
						t.Fatal(err)
					}
				}
				account := &domain.Member{DisplayName: "Account owner", TailnetLogin: "account@example.test", Role: domain.RoleCollaborator}
				if err := e.db.CreateMember(t.Context(), account); err != nil {
					t.Fatal(err)
				}
				run.AccountMemberID = account.ID
				if err := e.db.UpdateRun(t.Context(), run); err != nil {
					t.Fatal(err)
				}
				if err := e.db.ShareAccount(t.Context(), account.ID, actor.ID); err != nil {
					t.Fatal(err)
				}
				gate := &sync.Mutex{}
				boundary := &developmentRetentionBoundary{entered: make(chan struct{}), release: make(chan struct{})}
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(boundary.release) }) }
				defer unblock()
				root := t.TempDir()
				service, err := evidence.New(evidence.Config{
					Store: e.db, Runs: e.db, Git: developmentRetentionGit{workspace: e.ws.ID},
					Artifacts: e.sched, EvidenceDir: root, Events: boundary, AuthorizationMu: gate,
				})
				if err != nil {
					t.Fatal(err)
				}
				e.sched.UseEvidence(service)
				artifact := captureFixture(t, e.sched, run.ID)
				raw, err := json.Marshal(protocol.DevArtifactRetainParams{ArtifactIDs: []string{artifact.ID}, IdempotencyKey: "withdrawn"})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					var callErr error
					if kind == control.PrincipalRunAgent {
						_, callErr = e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevArtifactRetain, raw)
					} else {
						_, callErr = e.sched.CallDevelopment(t.Context(), run.ID, control.Principal{Kind: kind, MemberID: actor.ID}, protocol.MethodDevArtifactRetain, raw, func() error { return nil })
					}
					done <- callErr
				}()
				select {
				case <-boundary.entered:
				case callErr := <-done:
					t.Fatalf("retention failed before selected capture staging: %v", callErr)
				case <-time.After(waitTimeout):
					t.Fatal("retention did not stage selected capture")
				}
				var revokeErr error
				gate.Lock()
				switch change {
				case "role":
					actor.Role = domain.RoleViewer
					revokeErr = e.db.UpdateMember(t.Context(), actor)
				case "pending-membership":
					actor.Pending = true
					revokeErr = e.db.UpdateMember(t.Context(), actor)
				case "account":
					revokeErr = e.db.RevokeAccountShare(t.Context(), account.ID, actor.ID)
				case "workspace-steer":
					revokeErr = e.db.SetWorkspaceSteerOthers(t.Context(), e.ws.ID, domain.SteerOthersAdminsOnly)
				case "removed-membership":
					revokeErr = e.db.DeleteMember(t.Context(), actor.ID)
				}
				gate.Unlock()
				if revokeErr != nil {
					t.Fatal(revokeErr)
				}
				unblock()
				select {
				case callErr := <-done:
					want := permissions.ErrDenied
					if change == "removed-membership" {
						want = store.ErrNotFound
					}
					if !errors.Is(callErr, want) {
						t.Fatalf("retention after completed %s revocation: %v", change, callErr)
					}
				case <-time.After(waitTimeout):
					t.Fatal("retention did not finish after revocation")
				}
				packets, err := e.db.ListEvidencePackets(t.Context(), e.ws.ID, run.ID, "", evidence.MaxPageSize)
				if err != nil || len(packets.Items) != 0 {
					t.Fatalf("denied selection left View-accessible metadata: %v, %v", packets, err)
				}
				staged, err := e.db.ListRunEvidenceStaging(t.Context(), run.ID, evidence.MaxPageSize)
				if err != nil || len(staged) != 0 {
					t.Fatalf("denied selection left staging rows: %v, %v", staged, err)
				}
				files, err := os.ReadDir(root)
				if err != nil || len(files) != 0 {
					t.Fatalf("denied selection left retained capture bytes: %v, %v", files, err)
				}
				dir, err := e.sched.captureDir(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := readCapture(dir, run.ID, artifact.ID); err != nil {
					t.Fatalf("denied retention removed original transient capture: %v", err)
				}
			})
		}
	}
}
