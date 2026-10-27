package sshd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

type fakeRunWaits struct {
	WatchService
	waits []protocol.WaitCondition
	err   error
}

func (f fakeRunWaits) RunWaitsOf(_ context.Context, runs []domain.RunID) (map[domain.RunID][]protocol.WaitCondition, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[domain.RunID][]protocol.WaitCondition{}
	for _, r := range runs {
		out[r] = append([]protocol.WaitCondition(nil), f.waits...)
	}
	return out, nil
}

func (f fakeRunWaits) CancelRunWait(context.Context, domain.RunID, string, domain.MemberID) (protocol.WaitCondition, error) {
	return f.waits[0], nil
}

// What a wait read with the owner's GitHub login, and the free text of how
// it ended, is shown only to the run's owner and admins.
func TestWaitConditionsLeaveOutTheOwnersReadsForOthers(t *testing.T) {
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	members := map[string]*domain.Member{}
	for _, m := range []struct {
		name string
		role domain.Role
	}{{"owner", domain.RoleCollaborator}, {"viewer", domain.RoleCollaborator}, {"admin", domain.RoleAdmin}} {
		members[m.name] = &domain.Member{DisplayName: m.name, TailnetLogin: m.name + "@example.com", Color: "#e6194b", Role: m.role}
		if err := db.CreateMember(t.Context(), members[m.name]); err != nil {
			t.Fatal(err)
		}
	}
	w := protocol.WaitCondition{
		ID: "wat-1", Spec: "pr:acme/app#7:checks", State: protocol.WaitCancelled, Outcome: "", Summary: "checks 1 passed, 2 pending",
		Detail: "failing checks: private-job", HeadSHA: "1111111", LastError: "read pull request acme/app#7: 502",
		Observed: protocol.WaitObserved{PRState: "open", HeadSHA: "1111111"}, EndedCause: "the run's member lost access: private",
		LastDeliveryError: "notify run: private",
	}
	s := &Server{cfg: Config{Store: db, Services: Services{Watches: fakeRunWaits{waits: []protocol.WaitCondition{w}}}}}
	ws := &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err := db.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: members["owner"].ID, Task: "t", Harness: "claude", Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err := db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	read := func(member string) protocol.WaitCondition {
		out := []protocol.Run{{}}
		if err := s.fillWaitConditions(t.Context(), members[member].ID, []*domain.Run{run}, out); err != nil {
			t.Fatal(err)
		}
		if len(out[0].WaitConditions) != 1 {
			t.Fatalf("%s sees %+v, want one condition", member, out[0].WaitConditions)
		}
		return out[0].WaitConditions[0]
	}
	if got := read("viewer"); got.Spec != w.Spec || got.State != w.State || got.Summary != "" || got.Detail != "" || got.HeadSHA != "" ||
		got.LastError != "" || got.EndedCause != "" || got.LastDeliveryError != "" || got.Observed != (protocol.WaitObserved{}) {
		t.Fatalf("viewer sees %+v, want only the condition and its state", got)
	}
	for _, m := range []string{"owner", "admin"} {
		if got := read(m); got.Summary != w.Summary || got.Detail != w.Detail || got.EndedCause != w.EndedCause || got.Observed != w.Observed {
			t.Fatalf("%s sees %+v, want everything", m, got)
		}
	}
	cancel := func(member string) protocol.WaitCondition {
		res, perr := s.runWaitCancel(t.Context(), members[member].ID, []byte(`{"run_id":"`+string(run.ID)+`","wait_id":"wat-1"}`))
		if perr != nil {
			t.Fatal(perr)
		}
		return res.(protocol.RunWaitCancelResult).Wait
	}
	if got := cancel("viewer"); got.Detail != "" || got.Summary != "" || got.LastError != "" || got.EndedCause != "" {
		t.Fatalf("run.wait.cancel by a viewer returned %+v, want it redacted", got)
	}
	if got := cancel("owner"); got.Detail != w.Detail {
		t.Fatalf("run.wait.cancel by the owner returned %+v, want everything", got)
	}
	s.cfg.Services.Watches = fakeRunWaits{err: errors.New("database is locked")}
	if _, perr := s.runGet(t.Context(), members["owner"].ID, []byte(`{"run_id":"`+string(run.ID)+`"}`)); perr == nil {
		t.Fatal("run.get with an unreadable wait store succeeded, want its error")
	}
}
