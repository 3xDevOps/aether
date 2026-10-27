package sshd

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeRunWaits struct {
	WatchService
	waits []protocol.WaitCondition
}

func (f fakeRunWaits) RunWaits(context.Context, domain.RunID) ([]protocol.WaitCondition, error) {
	return append([]protocol.WaitCondition(nil), f.waits...), nil
}

func (fakeRunWaits) CancelRunWait(context.Context, domain.RunID, string, domain.MemberID) (protocol.WaitCondition, error) {
	return protocol.WaitCondition{}, nil
}

// What a wait read with the owner's GitHub login is not shown to other
// viewers of the run.
func TestWaitConditionsLeaveOutTheOwnersReadsForOthers(t *testing.T) {
	w := protocol.WaitCondition{
		ID: "wat-1", Spec: "pr:acme/app#7:checks", State: protocol.WaitPending, Summary: "checks 1 passed, 2 pending",
		Detail: "failing checks: private-job", HeadSHA: "1111111", LastError: "read pull request acme/app#7: 502",
		Observed: protocol.WaitObserved{PRState: "open", HeadSHA: "1111111"},
	}
	s := &Server{cfg: Config{Services: Services{Watches: fakeRunWaits{waits: []protocol.WaitCondition{w}}}}}
	got := s.waitConditions("run-1", false)
	if len(got) != 1 || got[0].Spec != w.Spec || got[0].State != w.State || got[0].Summary != "" || got[0].Detail != "" ||
		got[0].HeadSHA != "" || got[0].LastError != "" || got[0].Observed != (protocol.WaitObserved{}) {
		t.Fatalf("viewer sees %+v, want only spec and state", got)
	}
	if full := s.waitConditions("run-1", true); full[0].Summary != w.Summary || full[0].Detail != w.Detail || full[0].Observed != w.Observed {
		t.Fatalf("owner sees %+v, want everything", full)
	}
}
