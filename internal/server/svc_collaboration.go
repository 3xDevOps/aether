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
			attachmentValidator = attachmentReferenceValidator(d.Runs)
		}
		svc, err := collab.New(collab.Config{
			Store:      d.Store,
			Runs:       d.Store,
			Workspaces: d.Store,
			Bus:        d.Bus,
			Control:    d.Control,
			Inject: func(ctx context.Context, run domain.RunID, actor domain.MemberID, prompt domain.AgentPrompt, steer bool, delivered func(error)) (string, error) {
				if d.Runs == nil {
					return "", fmt.Errorf("scheduler injection is unavailable")
				}
				return d.Runs.Inject(ctx, run, actor, prompt, steer, delivered)
			},
			Attachments: attachmentValidator,
			Ready:       d.Runs.RecoveryReady(),
			Reattached:  d.Runs.Reattached,
		})
		if err != nil {
			return nil, err
		}
		if d.SSH != nil {
			d.SSH.Services.Rooms = svc
			if d.Evidence != nil {
				d.SSH.Services.Evidence = d.Evidence
			}
		}
		return svc, nil
	})
}

// attachmentReferenceValidator holds attachment references to the
// scheduler's server-generated path contract; the room never reads one as
// a host path.
func attachmentReferenceValidator(r interface {
	ValidateAttachment(context.Context, domain.RunID, string) error
}) collab.AttachmentValidator {
	return func(ctx context.Context, _ domain.WorkspaceID, run domain.RunID, reference string) error {
		if r == nil {
			return fmt.Errorf("attachment validator is unavailable")
		}
		return r.ValidateAttachment(ctx, run, reference)
	}
}
