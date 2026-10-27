package server

import (
	"context"
	"errors"
	"strings"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Run waits: the conditions a run registers with coord.report until are
// watches the watch service svc_watch.go builds evaluates, and their wakes
// go to the run's inbox through the coordination service svc_coord.go
// builds. Both are built first.
func init() {
	registerService("watch-run", func(d Deps) (Service, error) {
		watches, ok := d.SSH.Services.Watches.(*watch.Service)
		if !ok {
			return nil, nil
		}
		if c, err := d.Runs.RequireCoordination(); err == nil {
			if n, ok := c.(watch.RunNotifier); ok {
				watches.UseRunNotifier(n)
			}
		}
		d.Runs.UseRunWaits(watches)
		return nil, nil
	})
}

// lazyWaits reads the watch service at call time: it is built after the
// coordination service that registers waits with it.
type lazyWaits struct{ ssh *sshd.Config }

var errNoWatches = errors.New("server: the watch service is not running")

func (l lazyWaits) service() (*watch.Service, error) {
	w, ok := l.ssh.Services.Watches.(*watch.Service)
	if !ok {
		return nil, errNoWatches
	}
	return w, nil
}

func (l lazyWaits) RegisterWait(ctx context.Context, owner *domain.Run, report *store.CoordStateReport, specs []protocol.WaitSpec) (bool, error) {
	w, err := l.service()
	if err != nil {
		return false, err
	}
	inserted, err := w.RegisterWait(ctx, owner, report, specs)
	if errors.Is(err, watch.ErrInvalid) {
		return false, &coord.InvalidWait{Reason: strings.TrimPrefix(err.Error(), watch.ErrInvalid.Error()+": ")}
	}
	return inserted, err
}

func (l lazyWaits) SettleRunWaits(ctx context.Context, run domain.RunID) ([]protocol.WaitCondition, error) {
	w, err := l.service()
	if err != nil {
		return nil, err
	}
	return w.SettleRunWaits(ctx, run)
}

func (l lazyWaits) EndRunWaits(ctx context.Context, run domain.RunID, cause string) error {
	w, err := l.service()
	if errors.Is(err, errNoWatches) {
		return nil
	}
	return w.EndRunWaits(ctx, run, cause)
}
