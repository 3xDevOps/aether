package vm

import (
	"context"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type helper interface {
	Ping(context.Context) (guest.Info, error)
	Boot(context.Context, guest.BootSpec) error
	Spawn(context.Context, guest.ProcessSpec) (process, error)
	Process(id string) process
	Stats(context.Context) (guest.Stats, error)
	SyncClock(context.Context, time.Time) error
	PowerOff(context.Context) error
	Close() error
}

type process interface {
	Attach(fromStart bool) runtime.Attachment
	CloseStdin(context.Context) error
	Resize(context.Context, guest.Size) error
	Signal(context.Context, syscall.Signal) error
	Status(context.Context) (guest.State, error)
	Wait(context.Context) (int, error)
}

func dialHelper(vsockPath string) (helper, error) {
	c, err := guest.Dial(vsockPath)
	if err != nil {
		return nil, err
	}
	return guestClient{c}, nil
}

type guestClient struct{ *guest.Client }

func (c guestClient) Spawn(ctx context.Context, spec guest.ProcessSpec) (process, error) {
	p, err := c.Client.Spawn(ctx, spec)
	if err != nil {
		return nil, err
	}
	return guestProcess{p}, nil
}

func (c guestClient) Process(id string) process { return guestProcess{c.Client.Process(id)} }

type guestProcess struct{ *guest.Process }

func (p guestProcess) Attach(fromStart bool) runtime.Attachment { return p.Process.Attach(fromStart) }
