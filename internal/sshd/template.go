package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/templates"
)

// Templates are workspace configuration, so creating, changing, and
// deleting one is workspace administration. Using one is launching a run,
// and so is scheduling one: a cron rule is a standing order for future
// runs, checked again against its creator's role at every fire.
func init() {
	registerMethod(protocol.MethodTemplateList, (*Server).templateList)
	registerGuarded(protocol.MethodTemplateSave, permissions.WorkspaceAdmin, nil, (*Server).templateSave)
	registerGuarded(protocol.MethodTemplateDelete, permissions.WorkspaceAdmin, nil, (*Server).templateDelete)
	registerGuarded(protocol.MethodTemplateLaunch, permissions.Launch, nil, (*Server).templateLaunch)
	registerMethod(protocol.MethodScheduleList, (*Server).scheduleList)
	registerGuarded(protocol.MethodScheduleSave, permissions.Launch, nil, (*Server).scheduleSave)
	registerGuarded(protocol.MethodScheduleDelete, permissions.Launch, nil, (*Server).scheduleDelete)
}

func (s *Server) templates() (TemplateService, *protocol.Error) {
	if s.cfg.Services.Templates == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "template service not configured"}
	}
	return s.cfg.Services.Templates, nil
}

// templateWorkspace resolves and validates the workspace every template
// method addresses.
func (s *Server) templateWorkspace(ctx context.Context, id string) (domain.WorkspaceID, *protocol.Error) {
	if id == "" {
		return "", invalidParams("workspace_id is required")
	}
	if _, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(id)); err != nil {
		return "", rpcError(err)
	}
	return domain.WorkspaceID(id), nil
}

func (s *Server) templateList(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.TemplateListParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	list, err := svc.List(ctx, workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.TemplateListResult{Templates: make([]protocol.Template, 0, len(list))}
	for _, t := range list {
		out.Templates = append(out.Templates, templateWire(t))
	}
	return out, nil
}

func (s *Server) templateSave(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.TemplateSaveParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	t := &store.Template{
		WorkspaceID: workspace,
		Name:        p.Name,
		Task:        p.Task,
		Harness:     p.Harness,
		Mode:        domain.LaunchMode(p.Mode),
		Params:      p.Params,
		BudgetUSD:   p.BudgetUSD,
	}
	if err := svc.Save(ctx, t); err != nil {
		return nil, templateError(err)
	}
	return protocol.TemplateSaveResult{Template: templateWire(t)}, nil
}

func (s *Server) templateDelete(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.TemplateDeleteParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalidParams("name is required")
	}
	if err := svc.Delete(ctx, workspace, p.Name); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

func (s *Server) templateLaunch(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.TemplateLaunchParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalidParams("name is required")
	}
	launched, err := svc.Launch(ctx, workspace, p.Name, member, p.Params)
	if err != nil {
		return nil, templateError(err)
	}
	out := protocol.TemplateLaunchResult{
		Run:        protocol.RunFromDomain(launched.Run),
		BaseBranch: launched.Base.Branch,
	}
	if launched.Base.Known {
		out.BaseAge = templates.FormatAge(launched.Base.Age)
	}
	return out, nil
}

func (s *Server) scheduleList(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ScheduleListParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	list, err := svc.Schedules(ctx, workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.ScheduleListResult{Schedules: make([]protocol.Schedule, 0, len(list))}
	for _, info := range list {
		out.Schedules = append(out.Schedules, scheduleWire(info))
	}
	return out, nil
}

func (s *Server) scheduleSave(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ScheduleSaveParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	if p.Template == "" || p.Cron == "" {
		return nil, invalidParams("template and cron are required")
	}
	info, err := svc.SaveSchedule(ctx, workspace, p.Template, p.Cron, member)
	if err != nil {
		return nil, templateError(err)
	}
	return protocol.ScheduleSaveResult{Schedule: scheduleWire(*info)}, nil
}

func (s *Server) scheduleDelete(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.templates()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ScheduleDeleteParams](params)
	if perr != nil {
		return nil, perr
	}
	workspace, perr := s.templateWorkspace(ctx, p.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	if p.Template == "" {
		return nil, invalidParams("template is required")
	}
	if err := svc.DeleteSchedule(ctx, workspace, p.Template); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

// templateError maps the service's own validation failures - a bad cron
// rule, an unknown or missing parameter, an invalid name or mode - to
// invalid params, and everything else through the standard mapping.
func templateError(err error) *protocol.Error {
	if errors.Is(err, templates.ErrUnknownParam) || errors.Is(err, templates.ErrMissingParam) ||
		errors.Is(err, templates.ErrInvalidDefinition) {
		return invalidParams(err.Error())
	}
	return rpcError(err)
}

func templateWire(t *store.Template) protocol.Template {
	return protocol.Template{
		ID:          t.ID,
		WorkspaceID: string(t.WorkspaceID),
		Name:        t.Name,
		Task:        t.Task,
		Harness:     t.Harness,
		Mode:        string(t.Mode),
		Params:      t.Params,
		BudgetUSD:   t.BudgetUSD,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func scheduleWire(info templates.ScheduleInfo) protocol.Schedule {
	sc := info.Schedule
	w := protocol.Schedule{
		ID:          sc.ID,
		WorkspaceID: string(sc.WorkspaceID),
		Template:    sc.Template,
		Cron:        sc.Cron,
		MemberID:    string(sc.MemberID),
		CreatedAt:   sc.CreatedAt.UTC().Format(time.RFC3339),
	}
	if sc.LastFiredAt != nil {
		w.LastFireAt = sc.LastFiredAt.UTC().Format(time.RFC3339)
	}
	if !info.Next.IsZero() {
		w.NextFireAt = info.Next.UTC().Format(time.RFC3339)
	}
	return w
}
