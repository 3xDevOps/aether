package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// runs, checked again against its creator's role at every fire. Only its
// creator or the workspace's admin replaces or deletes a schedule.
func init() {
	registerGuarded(protocol.MethodTemplateList, permissions.View, workspaceTarget, (*Server).templateList)
	registerGuarded(protocol.MethodTemplateSave, permissions.WorkspaceAdmin, workspaceTarget, (*Server).templateSave)
	registerGuarded(protocol.MethodTemplateDelete, permissions.WorkspaceAdmin, workspaceTarget, (*Server).templateDelete)
	registerGuarded(protocol.MethodTemplateLaunch, permissions.Launch, workspaceTarget, (*Server).templateLaunch)
	registerGuarded(protocol.MethodScheduleList, permissions.View, workspaceTarget, (*Server).scheduleList)
	registerGuarded(protocol.MethodScheduleSave, permissions.Launch, workspaceTarget, (*Server).scheduleSave)
	registerGuarded(protocol.MethodScheduleDelete, permissions.Launch, workspaceTarget, (*Server).scheduleDelete)
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

func (s *Server) templateSave(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
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
		Model:       p.Model,
		Effort:      p.Effort,
		Params:      p.Params,
		BudgetUSD:   p.BudgetUSD,
	}
	if validator, ok := s.cfg.Runs.(LaunchValidator); ok && (p.Model != "" || p.Effort != "") {
		mode := t.Mode
		if mode == "" {
			mode = domain.LaunchHeadless
		}
		if err := validator.ValidateLaunch(ctx, member, member, t.Harness, mode, t.Model, t.Effort); err != nil {
			return nil, invalidParams(err.Error())
		}
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
	p, perr := decodeLaunchParams[protocol.TemplateLaunchParams](params)
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
	// A retry of a launch keeps its receipt after the template changes, so
	// the version is not part of the params it must repeat, nor is
	// replay_only.
	key, version, replayOnly := p.IdempotencyKey, p.ExpectedVersion, p.ReplayOnly
	p.IdempotencyKey, p.ExpectedVersion, p.ReplayOnly = "", "", false
	if replayOnly && key == "" {
		return nil, invalidParams("replay_only needs the idempotency_key of the launch to return")
	}
	run, replayed, perr := s.idempotentLaunch(ctx, member, key, protocol.MethodTemplateLaunch, p, nil, func(created func(domain.RunID)) (*domain.Run, *protocol.Error) {
		if replayOnly {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: no session was launched under idempotency_key %q", protocol.MethodTemplateLaunch, key)}
		}
		launched, err := svc.Launch(ctx, workspace, p.Name, member, p.Params, version, created)
		if err != nil {
			return nil, templateError(err)
		}
		return launched.Run, nil
	})
	if perr != nil {
		return nil, perr
	}
	out := protocol.TemplateLaunchResult{
		Run:        s.memberRunSnapshot(ctx, member, run),
		BaseCommit: run.BaseCommit,
		BaseBranch: run.BaseBranch,
		BaseSource: run.BaseSource,
		Replayed:   replayed,
	}
	if !run.BaseCheckedAt.IsZero() {
		out.BaseCheckedAt = run.BaseCheckedAt.UTC().Format(time.RFC3339)
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
	if perr := s.checkScheduleOwner(ctx, svc, member, workspace, p.Template, protocol.MethodScheduleSave); perr != nil {
		return nil, perr
	}
	info, err := svc.SaveSchedule(ctx, workspace, p.Template, p.Cron, member)
	if err != nil {
		return nil, templateError(err)
	}
	return protocol.ScheduleSaveResult{Schedule: scheduleWire(*info)}, nil
}

func (s *Server) scheduleDelete(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
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
	if perr := s.checkScheduleOwner(ctx, svc, member, workspace, p.Template, protocol.MethodScheduleDelete); perr != nil {
		return nil, perr
	}
	if err := svc.DeleteSchedule(ctx, workspace, p.Template); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

// checkScheduleOwner refuses method, replacing or deleting template's
// schedule in workspace, unless member created it or administers the
// workspace. A template without a schedule passes.
func (s *Server) checkScheduleOwner(ctx context.Context, svc TemplateService, member domain.MemberID, workspace domain.WorkspaceID, template, method string) *protocol.Error {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return rpcError(err)
	}
	if permissions.Check(permissions.WorkspaceAdmin, actor, permissions.Target{Workspace: workspace}) == nil {
		return nil
	}
	list, err := svc.Schedules(ctx, workspace)
	if err != nil {
		return rpcError(err)
	}
	for _, info := range list {
		if info.Schedule.Template == template && info.Schedule.MemberID != member {
			return &protocol.Error{Code: protocol.CodeDenied, Message: method + ": only the schedule's creator or the workspace's admin may replace or delete it"}
		}
	}
	return nil
}

// templateError maps the service's own validation failures - a bad cron
// rule, an unknown or missing parameter, an invalid name or mode - to
// invalid params, and everything else through the standard mapping.
func templateError(err error) *protocol.Error {
	if errors.Is(err, templates.ErrUnknownParam) || errors.Is(err, templates.ErrMissingParam) ||
		errors.Is(err, templates.ErrInvalidDefinition) {
		return invalidParams(err.Error())
	}
	if errors.Is(err, templates.ErrChanged) {
		return &protocol.Error{Code: protocol.CodeConflict, Message: err.Error()}
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
		Model:       t.Model,
		Effort:      t.Effort,
		Params:      t.Params,
		BudgetUSD:   t.BudgetUSD,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
		Version:     templates.Version(t),
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
