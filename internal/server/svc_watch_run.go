package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Run waits: the conditions a run registers with coord.report until are
// watches the watch service svc_watch.go builds evaluates, and their wakes
// go to the run's inbox through the coordination service svc_coord.go
// builds. Both are built first. The scheduler judges a reported wait by
// the same conditions (storeWaitConditions), again on every
// run.wait.changed.
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
		return &waitRefresher{bus: d.Bus, runs: d.Runs}, nil
	})
}

// storeWaitConditions is the scheduler's view of a run's wait conditions,
// read from the store the watch service keeps them in, so it is there
// from the scheduler's first judgement after a start.
type storeWaitConditions struct{ db *store.DB }

func (w storeWaitConditions) RunWaits(ctx context.Context, run domain.RunID) ([]scheduler.WaitCondition, error) {
	waits, err := w.db.RunWaitConditions(ctx, run)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.WaitCondition, 0, len(waits))
	for _, c := range waits {
		out = append(out, scheduler.WaitCondition{
			Spec: c.Spec, CreatedAt: c.CreatedAt, State: c.WaitState, Summary: c.Summary,
			LastCheckedAt: timeOf(c.LastCheckAt), SourceCheckedAt: timeOf(c.SourceCheckedAt), NextCheckAt: timeOf(c.NextCheckAt),
			ConsecutiveFailures: c.Failures, LastError: c.Error, ResolvedAt: timeOf(c.ResolvedAt), DeliveredAt: timeOf(c.DeliveredAt),
			LastDeliveryError: c.DeliveryError,
		})
	}
	return out, nil
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// waitRefresher has the scheduler judge a run's wait again whenever one of
// its conditions changes.
type waitRefresher struct {
	bus  events.Bus
	runs *scheduler.Scheduler
	stop context.CancelFunc
	done chan struct{}
}

func (r *waitRefresher) Start(ctx context.Context) error {
	sub, err := r.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunWaitChanged}}, Buffer: 256,
	})
	if err != nil {
		return fmt.Errorf("watch-run: follow run.wait.changed: %w", err)
	}
	loop, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.stop, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		defer sub.Close() //nolint:errcheck // shutdown
		for {
			select {
			case <-loop.Done():
				return
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				p, _ := e.Payload.(events.RunWaitChangedPayload)
				if err := r.runs.WaitChanged(loop, p.RunID); err != nil && loop.Err() == nil {
					slog.Warn("watch-run: judge the run's wait again", "run", p.RunID, "error", err)
				}
			}
		}
	}()
	return nil
}

func (r *waitRefresher) Close() error {
	if r.stop != nil {
		r.stop()
		<-r.done
	}
	return nil
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

func (l lazyWaits) EndRunWaits(ctx context.Context, run domain.RunID, cause string, before time.Time) error {
	w, err := l.service()
	if errors.Is(err, errNoWatches) {
		return nil
	}
	return w.EndRunWaits(ctx, run, cause, before)
}
