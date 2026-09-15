package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/domain"
)

func init() {
	registerService("collaboration", func(d Deps) (Service, error) {
		if d.Control == nil {
			return nil, errors.New("collaboration needs the shared control service")
		}
		var attachmentValidator collab.AttachmentValidator
		if d.Runs != nil {
			attachmentValidator = terminalImageReferenceValidator(d.Runs)
		}
		svc, err := collab.New(collab.Config{
			Store:      d.Store,
			Runs:       d.Store,
			Workspaces: d.Store,
			Bus:        d.Bus,
			Control:    d.Control,
			Inject: func(ctx context.Context, run domain.RunID, actor domain.MemberID, message string) error {
				if d.Runs == nil {
					return fmt.Errorf("scheduler injection is unavailable")
				}
				return d.Runs.Inject(ctx, run, actor, message)
			},
			Attachments: attachmentValidator,
			Ready:       d.Runs.RecoveryReady(),
		})
		if err != nil {
			return nil, err
		}
		if d.SSH != nil {
			d.SSH.Services.Rooms = svc
		}
		return svc, nil
	})
}

// terminalImageReferenceValidator keeps image attachment references on the
// scheduler's strict, server-generated path contract. The room service does
// not interpret the reference as a host path.
func terminalImageReferenceValidator(r interface {
	ValidateTerminalImage(context.Context, domain.RunID, string) error
}) collab.AttachmentValidator {
	return func(ctx context.Context, _ domain.WorkspaceID, run domain.RunID, reference string) error {
		if r == nil {
			return fmt.Errorf("terminal image validator is unavailable")
		}
		return r.ValidateTerminalImage(ctx, run, reference)
	}
}
