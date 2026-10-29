package mission

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// mission.create refuses a worker or fallback account nobody shared with
// its caller, with run.launch's error, before anything runs on it.
func TestCreateRefusesAccountsNotSharedWithTheCaller(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	owner := regressionMember(t, f.db, "owner")
	stranger := regressionMember(t, f.db, "stranger")
	if _, err := f.db.ShareAccount(ctx, owner.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	self := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchTUI)}
	worker := func(account *domain.Member) protocol.MissionExecutionChoice {
		return protocol.MissionExecutionChoice{AccountMemberID: string(account.ID), Harness: "claude", Mode: string(domain.LaunchTUI)}
	}
	want := "permission denied: member " + string(stranger.ID) + " has not shared their account with you"
	for _, tc := range []struct {
		name     string
		choices  []protocol.MissionExecutionChoice
		fallback []string
		refused  bool
	}{
		{name: "own account", choices: []protocol.MissionExecutionChoice{self}},
		{name: "shared worker account", choices: []protocol.MissionExecutionChoice{self, worker(owner)}, fallback: []string{string(owner.ID)}},
		{name: "unshared worker account", choices: []protocol.MissionExecutionChoice{self, worker(stranger)}, refused: true},
		{name: "unshared fallback account", choices: []protocol.MissionExecutionChoice{self}, fallback: []string{string(stranger.ID)}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launches := len(f.launcher.requests)
			_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
				WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-" + tc.name,
				Integrator: protocol.MissionIntegrator(self), ExecutionChoices: tc.choices, FallbackAccountMemberIDs: tc.fallback,
			})
			switch {
			case !tc.refused && err != nil:
				t.Fatalf("create: %v", err)
			case tc.refused && (!errors.Is(err, permissions.ErrDenied) || err.Error() != want):
				t.Fatalf("create = %v, want %q", err, want)
			case tc.refused && len(f.launcher.requests) != launches:
				t.Fatalf("a refused create launched %d runs", len(f.launcher.requests)-launches)
			}
		})
	}
}
