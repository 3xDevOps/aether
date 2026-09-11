package server

import (
	"path/filepath"

	mirrorsvc "github.com/3xDevOps/Aether/internal/mirror"
)

// Workspace mirrors are synchronous: the builder attaches one service
// instance to both the control-channel handlers and launch base capture.
func init() {
	registerService("mirror", func(d Deps) (Service, error) {
		svc, err := mirrorsvc.New(mirrorsvc.Config{
			Root:  filepath.Join(d.DataDir, "mirrors"),
			Store: d.Store,
			Git:   d.Git,
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Mirrors = svc
		d.Runs.UseBaseCapture(svc)
		return nil, nil
	})
}
