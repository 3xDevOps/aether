package mission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type emptyMissionLauncher struct{}

func (emptyMissionLauncher) LaunchMission(context.Context, MissionLaunchRequest) (*domain.Run, error) {
	panic("empty mission database must not launch a run")
}

func TestStartupRecoveryFailureCanCloseAndRetry(t *testing.T) {
	db := openMissionRegressionDB(t)
	svc, err := New(Config{Store: db, Runs: emptyMissionLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 2 {
		if startErr := svc.Start(ctx); !errors.Is(startErr, context.Canceled) {
			t.Fatalf("startup recovery error = %v, want context cancellation", startErr)
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- svc.Close() }()
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatalf("close after failed startup: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close hung after failed startup")
	}
	if startErr := svc.Start(context.Background()); startErr != nil {
		t.Fatalf("retry startup with a healthy store: %v", startErr)
	}
	if closeErr := svc.Close(); closeErr != nil {
		t.Fatalf("close after successful retry: %v", closeErr)
	}
}
