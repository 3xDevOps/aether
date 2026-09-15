package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/evidence"
)

const evidenceCleanupInterval = time.Hour

// evidenceCleanupService performs one bounded retention batch immediately and
// then advances one bounded batch per interval. Its context and Close method
// always terminate the worker; cleanup never runs as an immortal goroutine.
type evidenceCleanupService struct {
	evidence *evidence.Service
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newEvidenceCleanupService(svc *evidence.Service, interval time.Duration) *evidenceCleanupService {
	if interval <= 0 {
		interval = evidenceCleanupInterval
	}
	return &evidenceCleanupService{evidence: svc, interval: interval}
}

func (s *evidenceCleanupService) Start(ctx context.Context) error {
	if s.evidence == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.cleanup(runCtx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.cleanup(runCtx)
			}
		}
	}()
	return nil
}

func (s *evidenceCleanupService) cleanup(ctx context.Context) {
	if n, err := s.evidence.CleanupExpired(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("server: evidence retention cleanup failed", "error", err, "cleaned", n)
	}
}

func (s *evidenceCleanupService) Close() error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	return nil
}

func init() {
	registerService("evidence-gc", func(d Deps) (Service, error) {
		return newEvidenceCleanupService(d.Evidence, evidenceCleanupInterval), nil
	})
}
