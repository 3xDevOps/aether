package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceEnvironmentGet, permissions.View, workspaceTarget, (*Server).workspaceEnvironmentGet)
	registerGuarded(protocol.MethodWorkspaceEnvironmentSet, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceEnvironmentSet)
}

func (s *Server) workspaceSecrets(id domain.WorkspaceID) (map[string]string, error) {
	if s.cfg.Secrets == nil {
		return map[string]string{}, nil
	}
	return s.cfg.Secrets.Get(id)
}

// environmentResult never carries a secret's value: only its name leaves the
// server.
func environmentResult(ws *domain.Workspace, secrets map[string]string) protocol.WorkspaceEnvironmentResult {
	variables := make([]protocol.WorkspaceVariable, 0, len(ws.Environment.Variables)+len(secrets))
	for name := range secrets {
		variables = append(variables, protocol.WorkspaceVariable{Name: name, Secret: true})
	}
	for name, value := range ws.Environment.Variables {
		if _, secret := secrets[name]; !secret {
			variables = append(variables, protocol.WorkspaceVariable{Name: name, Value: value})
		}
	}
	slices.SortFunc(variables, func(a, b protocol.WorkspaceVariable) int { return strings.Compare(a.Name, b.Name) })
	return protocol.WorkspaceEnvironmentResult{
		WorkspaceID: string(ws.ID),
		SetupScript: ws.Environment.SetupPolicy.Script,
		Variables:   variables,
	}
}

func (s *Server) workspaceEnvironmentGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceEnvironmentGetParams](params)
	if perr != nil {
		return nil, perr
	}
	id := domain.WorkspaceID(p.WorkspaceID)
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	secrets, err := s.workspaceSecrets(id)
	if err != nil {
		return nil, rpcError(err)
	}
	return environmentResult(ws, secrets), nil
}

// workspaceEnvironmentSet applies one change to a workspace's setup script
// and variables (admin only; the guard has already checked WorkspaceAdmin).
// The timeline note names what changed and never a value.
func (s *Server) workspaceEnvironmentSet(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceEnvironmentSetParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.SetupScript != nil && len(*p.SetupScript) > protocol.MaxWorkspaceSetupScriptBytes {
		return nil, invalidParams(fmt.Sprintf("setup_script is larger than %d bytes", protocol.MaxWorkspaceSetupScriptBytes))
	}
	for _, v := range p.Set {
		// The name is not echoed: a pasted NAME=value would put the value
		// in the error.
		if v.Name == "" || strings.ContainsAny(v.Name, "=\x00") {
			return nil, invalidParams(`a variable name must not be empty or contain "=" or NUL`)
		}
		if len(v.Value) > protocol.MaxWorkspaceVariableValueBytes {
			return nil, invalidParams(fmt.Sprintf("the value of %s is larger than %d bytes", v.Name, protocol.MaxWorkspaceVariableValueBytes))
		}
		if strings.ContainsRune(v.Value, 0) {
			return nil, invalidParams(fmt.Sprintf("the value of %s contains a NUL byte", v.Name))
		}
		if v.Secret && s.cfg.Secrets == nil {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace secrets are not configured"}
		}
	}

	s.environmentMu.Lock()
	defer s.environmentMu.Unlock()
	id := domain.WorkspaceID(p.WorkspaceID)
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	secrets, err := s.workspaceSecrets(id)
	if err != nil {
		return nil, rpcError(err)
	}
	variables := maps.Clone(ws.Environment.Variables)
	if variables == nil {
		variables = map[string]string{}
	}

	var changes []string
	if p.SetupScript != nil && *p.SetupScript != ws.Environment.SetupPolicy.Script {
		ws.Environment.SetupPolicy.Script = *p.SetupScript
		changes = append(changes, "setup script changed")
	}
	var removed []string
	for _, name := range p.Unset {
		_, plain := variables[name]
		_, secret := secrets[name]
		if plain || secret {
			removed = append(removed, name)
		}
		delete(variables, name)
		delete(secrets, name)
	}
	var set []string
	for _, v := range p.Set {
		if v.Secret {
			secrets[v.Name] = v.Value
			delete(variables, v.Name)
			set = append(set, v.Name+" (secret)")
		} else {
			variables[v.Name] = v.Value
			delete(secrets, v.Name)
			set = append(set, v.Name)
		}
	}
	if len(set) > 0 {
		changes = append(changes, "set "+strings.Join(set, ", "))
	}
	if len(removed) > 0 {
		changes = append(changes, "removed "+strings.Join(removed, ", "))
	}
	ws.Environment.Variables = variables

	// Secrets first: a variable that turned secret is then in both places,
	// never in neither, if the row update fails.
	if s.cfg.Secrets != nil {
		if err := s.cfg.Secrets.Put(id, secrets); err != nil {
			return nil, rpcError(err)
		}
	}
	if err := s.cfg.Store.SetWorkspaceEnvironment(ctx, id, ws.Environment); err != nil {
		return nil, rpcError(err)
	}
	if len(changes) > 0 {
		_, _ = s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: id,
			ActorID:     member,
			Payload: events.TimelinePayload{
				Kind:    events.TimelineNote,
				Message: "workspace environment: " + strings.Join(changes, "; "),
			},
		})
	}
	return environmentResult(ws, secrets), nil
}
