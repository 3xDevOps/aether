package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceEnvironmentGet, permissions.View, workspaceTarget, (*Server).workspaceEnvironmentGet)
	registerGuarded(protocol.MethodWorkspaceEnvironmentSet, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceEnvironmentSet)
}

func (s *Server) workspaceSecrets(id domain.WorkspaceID) (secrets, files map[string]string, err error) {
	if s.cfg.Secrets == nil {
		return map[string]string{}, map[string]string{}, nil
	}
	if secrets, err = s.cfg.Secrets.Get(id); err != nil {
		return nil, nil, err
	}
	files, err = s.cfg.Secrets.Files(id)
	return secrets, files, err
}

// environmentResult never carries a secret's value or a file's content: only
// the name and the path and size leave the server.
func environmentResult(ws *domain.Workspace, secrets, files map[string]string) protocol.WorkspaceEnvironmentResult {
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
	listed := make([]protocol.WorkspaceFileInfo, 0, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		listed = append(listed, protocol.WorkspaceFileInfo{Path: name, Size: len(files[name])})
	}
	return protocol.WorkspaceEnvironmentResult{
		WorkspaceID: string(ws.ID),
		SetupScript: ws.Environment.SetupPolicy.Script,
		Variables:   variables,
		Files:       listed,
		Egress:      ws.Environment.Egress.On,
		EgressHosts: append([]string{}, slices.Sorted(slices.Values(egress.Hosts(ws.Environment.Egress)))...),
	}
}

func normalizeHosts(entries []string) ([]string, *protocol.Error) {
	hosts := make([]string, 0, len(entries))
	for _, entry := range entries {
		host, err := egress.NormalizeHost(entry)
		if err != nil {
			return nil, invalidParams(err.Error())
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func (s *Server) workspaceEnvironmentGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceEnvironmentGetParams](params)
	if perr != nil {
		return nil, perr
	}
	id := domain.WorkspaceID(p.WorkspaceID)
	s.environmentMu.Lock()
	defer s.environmentMu.Unlock()
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	secrets, files, err := s.workspaceSecrets(id)
	if err != nil {
		return nil, rpcError(err)
	}
	return environmentResult(ws, secrets, files), nil
}

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
	for _, f := range p.Files {
		if err := gitengine.ValidatePath(f.Path); err != nil || f.Path == "" || path.Clean(f.Path) != f.Path {
			return nil, invalidParams(fmt.Sprintf(`file path %q must be relative to the repository root, with no "." or ".." component and nothing under .git`, f.Path))
		}
		if slices.ContainsFunc(strings.Split(f.Path, "/"), func(part string) bool { return strings.TrimSpace(part) != part }) {
			return nil, invalidParams(fmt.Sprintf("file path %q has a name that starts or ends with whitespace", f.Path))
		}
		if len(f.Content) > protocol.MaxWorkspaceVariableValueBytes {
			return nil, invalidParams(fmt.Sprintf("file %s is larger than %d bytes", f.Path, protocol.MaxWorkspaceVariableValueBytes))
		}
	}
	if (len(p.Files) > 0 || len(p.RemoveFiles) > 0) && s.cfg.Secrets == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace files are not configured"}
	}
	allow, perr := normalizeHosts(p.EgressAllow)
	if perr != nil {
		return nil, perr
	}
	deny, perr := normalizeHosts(p.EgressDeny)
	if perr != nil {
		return nil, perr
	}

	s.environmentMu.Lock()
	defer s.environmentMu.Unlock()
	id := domain.WorkspaceID(p.WorkspaceID)
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	secrets, files, err := s.workspaceSecrets(id)
	if err != nil {
		return nil, rpcError(err)
	}
	saved, savedFiles := maps.Clone(secrets), maps.Clone(files)
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

	var removedFiles, setFiles []string
	for _, name := range p.RemoveFiles {
		if _, ok := files[name]; ok {
			removedFiles = append(removedFiles, name)
			delete(files, name)
		}
	}
	for _, f := range p.Files {
		files[f.Path] = f.Content
		setFiles = append(setFiles, f.Path)
	}
	if len(files) > protocol.MaxWorkspaceFiles {
		return nil, invalidParams(fmt.Sprintf("a workspace holds at most %d files; remove one first", protocol.MaxWorkspaceFiles))
	}
	if len(setFiles) > 0 {
		changes = append(changes, "files set: "+strings.Join(setFiles, ", "))
	}
	if len(removedFiles) > 0 {
		changes = append(changes, "files removed: "+strings.Join(removedFiles, ", "))
	}

	policy := ws.Environment.Egress
	if p.Egress != nil && *p.Egress != policy.On {
		policy.On = *p.Egress
		state := "off"
		if policy.On {
			state = "on"
		}
		changes = append(changes, "egress control "+state)
	}
	hosts := slices.Clone(egress.Hosts(policy))
	var allowed []string
	for _, host := range allow {
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
			allowed = append(allowed, host)
		}
	}
	for _, host := range deny {
		i := slices.Index(hosts, host)
		if i < 0 {
			return nil, invalidParams(host + " is not on the egress host list")
		}
		hosts = slices.Delete(hosts, i, i+1)
	}
	if len(allowed) > 0 {
		changes = append(changes, "egress allowed "+strings.Join(allowed, ", "))
	}
	if len(deny) > 0 {
		changes = append(changes, "egress denied "+strings.Join(deny, ", "))
	}
	if len(allowed) > 0 || len(deny) > 0 {
		slices.Sort(hosts)
		policy.Hosts = hosts
	}
	ws.Environment.Egress = policy

	if s.cfg.Secrets != nil {
		// A launch reads the row and the secret file under this lock, so it
		// gets both from before this change or both from after it.
		s.cfg.Secrets.Lock()
		defer s.cfg.Secrets.Unlock()
		err = s.cfg.Secrets.Put(id, secrets)
		if err == nil {
			err = s.cfg.Secrets.PutFiles(id, files)
		}
	}
	if err == nil {
		err = s.cfg.Store.SetWorkspaceEnvironment(ctx, id, ws.Environment)
	}
	if err != nil {
		// The secret files go back to what they were: a secret that was
		// turning plain would otherwise be gone from both places. A Put that
		// failed may already have replaced its file.
		if s.cfg.Secrets != nil {
			if restoreErr := errors.Join(s.cfg.Secrets.Put(id, saved), s.cfg.Secrets.PutFiles(id, savedFiles)); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore the workspace's secrets and files: %w", restoreErr))
			}
		}
		return nil, rpcError(err)
	}
	result := environmentResult(ws, secrets, files)
	if len(changes) > 0 {
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: id,
			ActorID:     member,
			Payload: events.TimelinePayload{
				Kind:    events.TimelineNote,
				Message: "workspace environment: " + strings.Join(changes, "; "),
			},
		}); err != nil {
			result.Warning = "the change is saved, but its timeline note was not recorded: " + err.Error()
		}
	}
	return result, nil
}
