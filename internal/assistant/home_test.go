package assistant

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// fakeHost records what was discarded and removed, and runs no adapter.
type fakeHost struct{ discarded, removed []domain.MemberID }

func (*fakeHost) AssistantAdapter(string) (scheduler.AssistantLaunch, error) {
	return scheduler.AssistantLaunch{}, errors.New("fakeHost: no adapter")
}

func (*fakeHost) StartAssistantExec(context.Context, domain.MemberID, string, runtime.ExecSpec, func()) (runtime.ManagedExec, error) {
	return nil, errors.New("fakeHost: no adapter")
}

func (*fakeHost) StopAssistantExec(context.Context, runtime.ExecIdentity) error { return nil }

func (h *fakeHost) DiscardAssistantState(_ context.Context, member domain.MemberID) error {
	h.discarded = append(h.discarded, member)
	return nil
}

func (h *fakeHost) RemoveAssistant(_ context.Context, member domain.MemberID) error {
	h.removed = append(h.removed, member)
	return nil
}
