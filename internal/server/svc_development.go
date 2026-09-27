package server

import (
	"context"
	"encoding/json"
	"io"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
)

func init() {
	registerService("development", func(d Deps) (Service, error) {
		d.Runs.UseDevelopmentTakeover(func(ctx context.Context, id domain.RunID, member domain.MemberID) error {
			if mission := d.SSH.Services.MissionControl; mission != nil {
				return mission.Takeover(ctx, id, member)
			}
			return nil
		})
		d.SSH.Services.Development = developmentService{runs: d.Runs}
		return nil, nil
	})
}

type developmentService struct{ runs *scheduler.Scheduler }

func (s developmentService) Call(ctx context.Context, run domain.Run, p control.Principal, method string, params json.RawMessage, authorize func(context.Context) error) (any, error) {
	if authorize == nil {
		return nil, control.ErrInvalid
	}
	return s.runs.CallDevelopment(ctx, run.ID, p, method, params, func() error { return authorize(ctx) })
}
func (s developmentService) AttachTerminal(ctx context.Context, run domain.Run, p control.Principal, target protocol.DevTerminalTarget, fence protocol.DevControlFence, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint, authorize func(context.Context) error) error {
	if authorize == nil {
		return control.ErrInvalid
	}
	if target.RunID != "" && target.RunID != string(run.ID) {
		return control.ErrInvalid
	}
	return s.runs.AttachDevelopmentTerminal(ctx, run.ID, p, target, fence, client, conn, resize, func() error {
		return s.runs.AuthorizeDevelopment(ctx, run.ID, p, func() error { return authorize(ctx) })
	})
}
func (s developmentService) BrowserFrames(ctx context.Context, run domain.Run, p control.Principal, target protocol.DevBrowserPageTarget, authorize func(context.Context) error) (<-chan protocol.DevBrowserFrame, func(), error) {
	if authorize == nil {
		return nil, nil, control.ErrInvalid
	}
	return s.runs.DevelopmentBrowserFrames(ctx, run.ID, p, target, func() error { return authorize(ctx) })
}
func (s developmentService) OpenArtifact(ctx context.Context, run domain.Run, p control.Principal, id string, authorize func(context.Context) error) (protocol.DevArtifact, io.ReadCloser, error) {
	if authorize == nil {
		return protocol.DevArtifact{}, nil, control.ErrInvalid
	}
	return s.runs.OpenDevelopmentArtifact(ctx, run.ID, p, id, func() error { return authorize(ctx) })
}

var _ sshd.DevelopmentService = developmentService{}
