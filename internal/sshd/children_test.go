package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// refusingChildren fails every write of a run's child-run setting.
type refusingChildren struct{ ChildService }

func (refusingChildren) Allow(context.Context, *domain.Member, domain.RunID, string) (protocol.RunChildrenAllowResult, error) {
	return protocol.RunChildrenAllowResult{}, errors.New("store: disk full")
}

func TestRunLaunchStopsARunWhoseChildSettingWasNotRecorded(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.Services.Children = refusingChildren{} })
	e.runs.admitInto = e.store
	_, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "build it", Harness: "claude", Children: domain.ChildrenOff,
	})
	if perr == nil || !strings.Contains(perr.Message, "disk full") || !strings.Contains(perr.Message, "was stopped") {
		t.Fatalf("launch = %v, want the setting's error and the run stopped", perr)
	}
	killed := 0
	for _, call := range e.runs.Calls() {
		if strings.HasPrefix(call, "kill:") {
			killed++
		}
	}
	if killed != 1 {
		t.Fatalf("calls = %v, want the launched run killed once", e.runs.Calls())
	}
}
