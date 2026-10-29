package assistant

import (
	"context"
	"errors"
	"testing"

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

func (*fakeHost) AssistantLoginFound(domain.MemberID, string) (bool, error) { return false, nil }

// TestSignInWaitsOutRemoval refuses a sign-in for a member whose removal
// has begun, and runs it otherwise.
func TestSignInWaitsOutRemoval(t *testing.T) {
	s := New(Config{Dir: t.TempDir(), Host: &fakeHost{}})
	started := 0
	start := func(context.Context) error {
		started++
		return nil
	}
	if err := s.SignIn(t.Context(), "m1", start); err != nil || started != 1 {
		t.Fatalf("SignIn = %v, started %d", err, started)
	}
	if err := s.Fence(t.Context(), "m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SignIn(t.Context(), "m1", start); !errors.Is(err, ErrRemoving) || started != 1 {
		t.Fatalf("SignIn during removal = %v, started %d; want ErrRemoving", err, started)
	}
}
