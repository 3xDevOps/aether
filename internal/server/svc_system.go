package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

func init() {
	registerService("system", func(d Deps) (Service, error) {
		source, _ := d.Runtime.(sysstats.ContainerSource)
		svc := &systemService{sampler: sysstats.New(sysstats.Options{Source: source})}
		d.SSH.Services.System = &systemReader{sampler: svc.sampler, store: d.Store}
		return svc, nil
	})
}

type systemService struct {
	sampler *sysstats.Sampler
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

// Start ignores ctx's deadline on purpose: it bounds setup, not the sampler.
func (s *systemService) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		s.sampler.Run(ctx)
	}()
	return nil
}

func (s *systemService) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
			<-s.done
		}
	})
	return nil
}

// systemReader resolves each container's owner per reading, so a handoff
// shows at once.
type systemReader struct {
	sampler *sysstats.Sampler
	store   store.Store
}

func (r *systemReader) Read(ctx context.Context, since time.Time) (sysstats.Snapshot, error) {
	snap := r.sampler.Read(since)
	var failure error
	snap.Containers, failure = r.attribute(ctx, snap.Containers)
	if failure != nil {
		snap.ContainersError = strings.TrimSpace(snap.ContainersError + " " + failure.Error())
	}
	return snap, nil
}

// attribute names each container's owner. A browser sidecar has its own
// limits, so it stays a separate entry owned by its run, with kind "browser".
// A container with no owner keeps an empty Owner.Kind.
func (r *systemReader) attribute(ctx context.Context, in []sysstats.ContainerUsage) ([]sysstats.ContainerUsage, error) {
	out := make([]sysstats.ContainerUsage, 0, len(in))
	byID := make(map[string]sysstats.Owner, len(in))
	var sidecars []sysstats.ContainerUsage
	var failure error
	for _, c := range in {
		if c.BrowserRunContainer != "" {
			sidecars = append(sidecars, c)
			continue
		}
		owner, err := r.owner(ctx, c.CreationKey)
		failure = errors.Join(failure, err)
		c.Owner = owner
		byID[c.ID] = owner
		out = append(out, c)
	}
	for _, c := range sidecars {
		if owner := byID[c.BrowserRunContainer]; owner.Kind == "run" {
			owner.Kind = "browser"
			c.Owner = owner
		}
		out = append(out, c)
	}
	return out, failure
}

func (r *systemReader) owner(ctx context.Context, key string) (sysstats.Owner, error) {
	if member, ok := strings.CutPrefix(key, "terminal:"); ok {
		return sysstats.Owner{Kind: "member", ID: member, MemberID: member}, nil
	}
	run, err := r.store.GetRun(ctx, domain.RunID(key))
	if errors.Is(err, store.ErrNotFound) {
		return sysstats.Owner{}, nil
	}
	if err != nil {
		return sysstats.Owner{}, fmt.Errorf("container owner lookup failed for %q: %w", key, err)
	}
	return sysstats.Owner{
		Kind: "run", ID: string(run.ID), MemberID: string(run.MemberID),
		WorkspaceID: string(run.WorkspaceID), Title: run.Title, Status: string(run.Status),
	}, nil
}
