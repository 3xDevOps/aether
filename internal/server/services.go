package server

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Service is a long-lived background component owned by the server. Start
// returns once setup is done - its context bounds setup, not the
// service's lifetime. Close is idempotent and safe before Start ran.
type Service interface {
	Start(ctx context.Context) error
	Close() error
}

// Deps is what a service builder wires itself from. SSH points at the
// sshd config still under assembly, so a builder attaches its
// handler-facing seam to SSH.Services before sshd.New consumes it.
type Deps struct {
	Config  Config
	DataDir string
	Store   store.Store
	Bus     events.Bus
	Events  events.EventLog
	Runs    *scheduler.Scheduler
	Git     *gitengine.Engine
	PTY     *ptyhost.Host
	SSH     *sshd.Config
}

type serviceBuilder struct {
	name  string
	build func(Deps) (Service, error)
}

var serviceBuilders []serviceBuilder

// registerService adds a background service builder, mirroring the way
// sshd registers control methods: a feature wires itself from an init in
// its own file instead of editing New, Run, and Close. A builder that
// only attaches a seam to Deps.SSH.Services returns a nil Service.
func registerService(name string, build func(Deps) (Service, error)) {
	if name == "" || build == nil {
		panic("server: registerService requires a name and builder")
	}
	for _, b := range serviceBuilders {
		if b.name == name {
			panic("server: duplicate service: " + name)
		}
	}
	serviceBuilders = append(serviceBuilders, serviceBuilder{name: name, build: build})
}

// namedService pairs a built service with the name it registered under,
// so start and shutdown errors say which one failed.
type namedService struct {
	name string
	svc  Service
}

func (s *Server) buildServices(d Deps) error {
	for _, b := range serviceBuilders {
		svc, err := b.build(d)
		if err != nil {
			return fmt.Errorf("server: build service %s: %w", b.name, err)
		}
		if svc != nil {
			s.services = append(s.services, namedService{name: b.name, svc: svc})
		}
	}
	return nil
}

func (s *Server) startServices(ctx context.Context) error {
	for _, n := range s.services {
		if err := n.svc.Start(ctx); err != nil {
			return fmt.Errorf("server: start service %s: %w", n.name, err)
		}
	}
	return nil
}

// closeServices returns each service's Close in registration order, for
// the server's ordered shutdown list.
func (s *Server) closeServices() []func() error {
	closers := make([]func() error, 0, len(s.services))
	for _, n := range s.services {
		closers = append(closers, n.svc.Close)
	}
	return closers
}
