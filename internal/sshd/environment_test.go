package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const envTestDockerfile = `FROM ubuntu:24.04
RUN apt-get update && \
    apt-get install -y golang-go=2:1.22~2build1
RUN apt-get install -y jq
`

const envTestManifest = `[
  {
    "name": "go",
    "version": "1.22",
    "reason": "repository language",
    "start_line": 2,
    "end_line": 3,
    "check_command": "go version"
  },
  {
    "name": "jq",
    "version": "1.7",
    "start_line": 4,
    "end_line": 4,
    "check_command": "jq --version"
  }
]`

// fakeEnvService records environment build, rollback, and edit calls.
type envCall struct {
	verb      string
	workspace domain.WorkspaceID
	version   int
	member    domain.MemberID
	harness   string
	request   string
}

type fakeEnvService struct {
	calls           chan envCall
	rollbackVersion int
	editVersion     int
	err             error
}

func newFakeEnvService() *fakeEnvService {
	return &fakeEnvService{calls: make(chan envCall, 8)}
}

func (f *fakeEnvService) BuildEnvironment(_ context.Context, workspace domain.WorkspaceID, version int) error {
	f.calls <- envCall{verb: "build", workspace: workspace, version: version}
	return f.err
}

func (f *fakeEnvService) RollbackEnvironment(_ context.Context, workspace domain.WorkspaceID) (int, error) {
	f.calls <- envCall{verb: "rollback", workspace: workspace}
	if f.err != nil {
		return 0, f.err
	}
	return f.rollbackVersion, nil
}

func (f *fakeEnvService) EditEnvironment(_ context.Context, workspace domain.WorkspaceID, member domain.MemberID, harness, request string) (int, error) {
	f.calls <- envCall{verb: "edit", workspace: workspace, member: member, harness: harness, request: request}
	if f.err != nil {
		return 0, f.err
	}
	return f.editVersion, nil
}

func envSaveParams(e *testEnv) protocol.EnvSaveParams {
	return protocol.EnvSaveParams{
		Workspace:  protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Dockerfile: envTestDockerfile,
		Manifest:   json.RawMessage(envTestManifest),
		Source:     "manual",
	}
}

func TestEnvMethodsDeniedForNonAdmin(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	cc := controlAs(t, e, collab)

	ws := protocol.WorkspaceSelector{ID: string(e.ws.ID)}
	wantDenied(t, cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil), "collaborator env.save")
	wantDenied(t, cc.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{Workspace: ws}, nil), "collaborator env.build")
	wantDenied(t, cc.Call(protocol.MethodEnvStatus, protocol.EnvStatusParams{Workspace: ws}, nil), "collaborator env.status")
	wantDenied(t, cc.Call(protocol.MethodEnvRollback, protocol.EnvRollbackParams{Workspace: ws}, nil), "collaborator env.rollback")
	wantDenied(t, cc.Call(protocol.MethodEnvEdit, protocol.EnvEditParams{Workspace: ws, Harness: "claude", Request: "add go"}, nil), "collaborator env.edit")
	wantDenied(t, cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{Workspace: ws, Version: 1}, nil), "collaborator env.get")
}

func TestEnvSaveRejectsInvalidDefinitionWithDetail(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	p := envSaveParams(e)
	p.Dockerfile = "FROM ubuntu:24.04\nCOPY secrets /root/\nRUN apt-get install -y golang-go\nRUN apt-get install -y jq\n"
	err := cc.Call(protocol.MethodEnvSave, p, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.save with COPY = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(pe.Message, "COPY") {
		t.Fatalf("env.save error %q does not carry the validator's detail", pe.Message)
	}

	p = envSaveParams(e)
	p.Manifest = json.RawMessage(`[]`)
	err = cc.Call(protocol.MethodEnvSave, p, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.save with empty manifest = %v, want CodeInvalidParams", err)
	}

	p = envSaveParams(e)
	p.Source = "wishful"
	err = cc.Call(protocol.MethodEnvSave, p, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.save with unknown source = %v, want CodeInvalidParams", err)
	}
}

func TestEnvSaveStatusRoundTrip(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	var saved protocol.EnvSaveResult
	if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), &saved); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("env.save version = %d, want 1", saved.Version)
	}

	var status protocol.EnvStatusResult
	if err := cc.Call(protocol.MethodEnvStatus, protocol.EnvStatusParams{
		Workspace: protocol.WorkspaceSelector{Name: e.ws.Name},
	}, &status); err != nil {
		t.Fatalf("env.status: %v", err)
	}
	if len(status.Versions) != 1 {
		t.Fatalf("env.status versions = %+v, want one", status.Versions)
	}
	v := status.Versions[0]
	if v.Version != 1 || v.Status != domain.EnvironmentSaved || v.Active {
		t.Fatalf("env.status version = %+v, want saved inactive version 1", v)
	}
	if v.Source != domain.EnvironmentSourceManual {
		t.Fatalf("env.status source = %q, want manual", v.Source)
	}
	if len(v.Manifest) != 2 || v.Manifest[0].Name != "go" || v.Manifest[1].Name != "jq" {
		t.Fatalf("env.status manifest = %+v, want the saved items", v.Manifest)
	}
	if status.ActiveVersion != 0 {
		t.Fatalf("env.status active version = %d, want 0", status.ActiveVersion)
	}
}

func TestEnvBuildWithoutDefinitionIsInvalidState(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("env.build without a definition = %v, want CodeInvalidState", err)
	}
}

func TestEnvBuildLaunchesAsynchronously(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	var built protocol.EnvBuildResult
	if err := cc.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, &built); err != nil {
		t.Fatalf("env.build: %v", err)
	}
	if built.Version != 1 {
		t.Fatalf("env.build version = %d, want 1", built.Version)
	}
	select {
	case call := <-svc.calls:
		if call.verb != "build" || call.workspace != e.ws.ID || call.version != 1 {
			t.Fatalf("build call = %+v, want build of version 1", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("env.build never reached the environment service")
	}
}

func TestEnvBuildUnknownVersionNotFound(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	err := cc.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Version:   9,
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("env.build of unknown version = %v, want CodeNotFound", err)
	}
}

func TestEnvRollbackReportsNewActiveVersion(t *testing.T) {
	svc := newFakeEnvService()
	svc.rollbackVersion = 1
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	// Two saved versions with version 2 active: a rollback candidate.
	ctx := context.Background()
	for range 2 {
		if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil); err != nil {
			t.Fatalf("env.save: %v", err)
		}
	}
	if err := e.store.SetEnvironmentStatus(ctx, e.ws.ID, 2, domain.EnvironmentActive, ""); err != nil {
		t.Fatalf("activate version 2: %v", err)
	}

	var rolled protocol.EnvRollbackResult
	if err := cc.Call(protocol.MethodEnvRollback, protocol.EnvRollbackParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, &rolled); err != nil {
		t.Fatalf("env.rollback: %v", err)
	}
	if rolled.Version != 1 {
		t.Fatalf("env.rollback version = %d, want 1", rolled.Version)
	}
	select {
	case call := <-svc.calls:
		if call.verb != "rollback" || call.workspace != e.ws.ID {
			t.Fatalf("rollback call = %+v, want rollback", call)
		}
	default:
		t.Fatal("env.rollback never reached the environment service")
	}
}

func TestEnvRollbackWithoutActiveVersionIsInvalidState(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvRollback, protocol.EnvRollbackParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("env.rollback without an active version = %v, want CodeInvalidState", err)
	}
}

func TestEnvEditRejectsUnknownHarnessBeforeSpawning(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvEdit, protocol.EnvEditParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Harness:   "opencode",
		Request:   "add go",
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.edit with unknown harness = %v, want CodeInvalidParams", err)
	}
	if !strings.Contains(pe.Message, "claude") {
		t.Fatalf("env.edit error %q does not name the supported agents", pe.Message)
	}
	select {
	case call := <-svc.calls:
		t.Fatalf("env.edit spawned %+v despite the rejected harness", call)
	default:
	}
}

func TestEnvEditRejectsEmptyRequest(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvEdit, protocol.EnvEditParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Harness:   "claude",
		Request:   "   ",
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.edit with an empty request = %v, want CodeInvalidParams", err)
	}
	select {
	case call := <-svc.calls:
		t.Fatalf("env.edit spawned %+v despite the empty request", call)
	default:
	}
}

func TestEnvEditLaunchesAsynchronously(t *testing.T) {
	svc := newFakeEnvService()
	e := newTestEnv(t, func(c *Config) { c.Services.Environments = svc })
	cc := controlClient(t, e)

	var res protocol.EnvEditResult
	if err := cc.Call(protocol.MethodEnvEdit, protocol.EnvEditParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Harness:   "claude",
		Request:   "add go 1.22",
	}, &res); err != nil {
		t.Fatalf("env.edit: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("env.edit result = %+v, want accepted", res)
	}
	select {
	case call := <-svc.calls:
		if call.verb != "edit" || call.workspace != e.ws.ID || call.member != e.member.ID ||
			call.harness != "claude" || call.request != "add go 1.22" {
			t.Fatalf("edit call = %+v, want the submitted harness and request for the caller", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("env.edit never reached the environment service")
	}
}

func TestEnvEditUnavailableWithoutService(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvEdit, protocol.EnvEditParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Harness:   "claude",
		Request:   "add go",
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("env.edit without a service = %v, want CodeUnavailable", err)
	}
}

func TestEnvGetRoundTripsStoredVersion(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	var got protocol.EnvGetResult
	if err := cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Version:   1,
	}, &got); err != nil {
		t.Fatalf("env.get: %v", err)
	}
	if got.Version != 1 || got.Dockerfile != envTestDockerfile {
		t.Fatalf("env.get = %+v, want version 1 with the saved Dockerfile", got)
	}
	if len(got.Manifest) != 2 || got.Manifest[0].Name != "go" || got.Manifest[1].Name != "jq" {
		t.Fatalf("env.get manifest = %+v, want the saved items", got.Manifest)
	}
	if got.Source != domain.EnvironmentSourceManual || got.Status != domain.EnvironmentSaved {
		t.Fatalf("env.get provenance = %q/%q, want manual/saved", got.Source, got.Status)
	}
	if got.Diff != "" {
		t.Fatalf("env.get diff = %q, want none without diff_against", got.Diff)
	}
}

func TestEnvGetMissingVersion(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Version:   3,
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("env.get of a missing version = %v, want CodeNotFound", err)
	}

	err = cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("env.get without a version = %v, want CodeInvalidParams", err)
	}
}

func TestEnvGetDiffReflectsChange(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	if err := cc.Call(protocol.MethodEnvSave, envSaveParams(e), nil); err != nil {
		t.Fatalf("env.save version 1: %v", err)
	}
	edited := envSaveParams(e)
	edited.Dockerfile = strings.Replace(envTestDockerfile, "install -y jq", "install -y jq ripgrep", 1)
	if err := cc.Call(protocol.MethodEnvSave, edited, nil); err != nil {
		t.Fatalf("env.save version 2: %v", err)
	}

	var got protocol.EnvGetResult
	if err := cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{
		Workspace:   protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Version:     2,
		DiffAgainst: 1,
	}, &got); err != nil {
		t.Fatalf("env.get with diff_against: %v", err)
	}
	// The exact header shape matters: the dashboard's parsePatch keys on
	// `diff --git ` and the `+++ b/` marker to name the file.
	for _, want := range []string{
		"diff --git a/Dockerfile b/Dockerfile",
		"--- a/Dockerfile",
		"+++ b/Dockerfile",
		"@@",
		"-RUN apt-get install -y jq",
		"+RUN apt-get install -y jq ripgrep",
	} {
		if !strings.Contains(got.Diff, want) {
			t.Fatalf("env.get diff missing %q:\n%s", want, got.Diff)
		}
	}

	var same protocol.EnvGetResult
	if err := cc.Call(protocol.MethodEnvGet, protocol.EnvGetParams{
		Workspace:   protocol.WorkspaceSelector{ID: string(e.ws.ID)},
		Version:     1,
		DiffAgainst: 1,
	}, &same); err != nil {
		t.Fatalf("env.get self diff: %v", err)
	}
	if same.Diff != "" {
		t.Fatalf("env.get self diff = %q, want empty", same.Diff)
	}
}

func TestEnvBuildUnavailableWithoutService(t *testing.T) {
	e := newTestEnv(t, nil)
	cc := controlClient(t, e)

	err := cc.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{
		Workspace: protocol.WorkspaceSelector{ID: string(e.ws.ID)},
	}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("env.build without a service = %v, want CodeUnavailable", err)
	}
}
