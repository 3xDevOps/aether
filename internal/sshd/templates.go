package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/templates"
)

// TemplateService is the seam for task templates and cron schedules.
// Satisfied by *templates.Service.
type TemplateService interface {
	// List returns a workspace's templates by name.
	List(ctx context.Context, workspace domain.WorkspaceID) ([]*store.Template, error)
	// Save creates or replaces a template, validating its prompt,
	// parameters, and mode, and stamping the stored identity into t.
	Save(ctx context.Context, t *store.Template) error
	// Delete removes a template and its schedule.
	Delete(ctx context.Context, workspace domain.WorkspaceID, name string) error
	// Launch renders a template and launches it as member through the
	// same scheduler path a hand-launched run takes; params override the
	// template's defaults.
	Launch(ctx context.Context, workspace domain.WorkspaceID, name string, member domain.MemberID, params map[string]string) (*templates.Launched, error)
	// Schedules returns a workspace's cron rules with their next fire time.
	Schedules(ctx context.Context, workspace domain.WorkspaceID) ([]templates.ScheduleInfo, error)
	// SaveSchedule creates or replaces a template's cron rule, recording
	// member as the identity every fire is attributed to and re-checked
	// against.
	SaveSchedule(ctx context.Context, workspace domain.WorkspaceID, template, spec string, member domain.MemberID) (*templates.ScheduleInfo, error)
	// DeleteSchedule removes a template's cron rule.
	DeleteSchedule(ctx context.Context, workspace domain.WorkspaceID, template string) error
}
