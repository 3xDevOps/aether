package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/dictation"
)

func init() {
	registerService("dictation", func(d Deps) (Service, error) {
		containers, ok := d.Runtime.(dictation.Containers)
		if !ok {
			return nil, nil
		}
		dir := dictation.Dir(d.DataDir)
		engine, err := dictation.NewEngine(dir, containers)
		if err != nil {
			return nil, err
		}
		svc := dictation.New(dir, engine)
		d.SSH.Services.Dictation = svc
		return dictationService{Service: svc, containers: containers, owner: dictation.Owner(dir)}, nil
	})
}

type dictationService struct {
	*dictation.Service
	containers dictation.Containers
	owner      string
}

// Start removes an engine container an earlier server left running. It
// never holds up or fails the server: the first clip removes a leftover
// again before it starts an engine.
func (s dictationService) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.containers.RemoveDictation(ctx, s.owner); err != nil {
		slog.Warn("dictation: remove an engine left by an earlier server", "error", err)
	}
	return nil
}
