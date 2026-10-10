package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/secretstore"
)

func TestWorkspaceEnvironment(t *testing.T) {
	t.Parallel()
	const secret = "npm-token-7c2e91"
	secretsDir := filepath.Join(t.TempDir(), "workspace-secrets")
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secretstore.New(secretsDir) })
	ctx := context.Background()
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	admin := controlAs(t, e, e.signer)

	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	script := "npm ci\n"
	change := protocol.WorkspaceEnvironmentSetParams{
		WorkspaceID: string(e.ws.ID),
		SetupScript: &script,
		Set: []protocol.WorkspaceVariable{
			{Name: "NODE_ENV", Value: "test"},
			{Name: "NPM_TOKEN", Value: secret, Secret: true},
		},
	}
	for who, signer := range map[string]*protocol.Client{"viewer": controlAs(t, e, viewer), "collaborator": controlAs(t, e, collab)} {
		wantDenied(t, signer.Call(protocol.MethodWorkspaceEnvironmentSet, change, nil), who+" workspace.environment.set")
	}

	var saved json.RawMessage
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentSet, change, &saved); err != nil {
		t.Fatalf("admin workspace.environment.set: %v", err)
	}
	note := waitTimelineNote(t, sub)
	if want := "workspace environment: setup script changed; set NODE_ENV, NPM_TOKEN (secret)"; note != want {
		t.Fatalf("timeline note = %q, want %q", note, want)
	}

	// What a member without the admin role reads back.
	var read json.RawMessage
	if err = controlAs(t, e, viewer).Call(protocol.MethodWorkspaceEnvironmentGet,
		protocol.WorkspaceEnvironmentGetParams{WorkspaceID: string(e.ws.ID)}, &read); err != nil {
		t.Fatalf("viewer workspace.environment.get: %v", err)
	}
	var got protocol.WorkspaceEnvironmentResult
	if err = json.Unmarshal(read, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []protocol.WorkspaceVariable{{Name: "NODE_ENV", Value: "test"}, {Name: "NPM_TOKEN", Secret: true}}
	if got.SetupScript != script || !slices.Equal(got.Variables, want) {
		t.Fatalf("environment = %+v, want script %q and %+v", got, script, want)
	}

	// The value is in the secret file and nowhere a member or a database
	// backup can read it.
	stored, err := e.store.GetWorkspace(ctx, e.ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	row, err := json.Marshal(stored.Environment)
	if err != nil {
		t.Fatalf("encode row: %v", err)
	}
	for where, text := range map[string]string{"set result": string(saved), "get result": string(read), "timeline": note, "workspace row": string(row)} {
		if strings.Contains(text, secret) {
			t.Errorf("%s carries the secret value: %s", where, text)
		}
	}
	file, err := os.ReadFile(filepath.Join(secretsDir, string(e.ws.ID)+".json"))
	if err != nil || !strings.Contains(string(file), secret) {
		t.Fatalf("secret file = %q, %v, want it to hold the value", file, err)
	}

	// A change names only what it touches: the script stays.
	var next protocol.WorkspaceEnvironmentResult
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentSet, protocol.WorkspaceEnvironmentSetParams{
		WorkspaceID: string(e.ws.ID),
		Set:         []protocol.WorkspaceVariable{{Name: "NODE_ENV", Value: "ci", Secret: true}},
		Unset:       []string{"NPM_TOKEN", "NEVER_SET"},
	}, &next); err != nil {
		t.Fatalf("second workspace.environment.set: %v", err)
	}
	if next.SetupScript != script || !slices.Equal(next.Variables, []protocol.WorkspaceVariable{{Name: "NODE_ENV", Secret: true}}) {
		t.Fatalf("environment after the second change = %+v", next)
	}
	if note = waitTimelineNote(t, sub); note != "workspace environment: set NODE_ENV (secret); removed NPM_TOKEN" {
		t.Fatalf("second timeline note = %q", note)
	}
	if stored, err = e.store.GetWorkspace(ctx, e.ws.ID); err != nil || len(stored.Environment.Variables) != 0 {
		t.Fatalf("workspace row keeps %v, %v after NODE_ENV turned secret", stored.Environment.Variables, err)
	}
}

func TestWorkspaceEnvironmentRefusals(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secretstore.New(t.TempDir()) })
	admin := controlAs(t, e, e.signer)
	long := strings.Repeat("x", protocol.MaxWorkspaceVariableValueBytes+1)

	for name, change := range map[string]protocol.WorkspaceEnvironmentSetParams{
		"empty name":    {Set: []protocol.WorkspaceVariable{{Name: "", Value: "v"}}},
		"pasted pair":   {Set: []protocol.WorkspaceVariable{{Name: "TOKEN=pasted-value", Secret: true}}},
		"NUL in value":  {Set: []protocol.WorkspaceVariable{{Name: "A", Value: "a\x00b"}}},
		"value too big": {Set: []protocol.WorkspaceVariable{{Name: "A", Value: long, Secret: true}}},
		"script too big": {SetupScript: func() *string {
			s := strings.Repeat("#", protocol.MaxWorkspaceSetupScriptBytes+1)
			return &s
		}()},
	} {
		change.WorkspaceID = string(e.ws.ID)
		var pe *protocol.Error
		err := admin.Call(protocol.MethodWorkspaceEnvironmentSet, change, nil)
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Errorf("%s = %v, want CodeInvalidParams", name, err)
			continue
		}
		if strings.Contains(pe.Message, "pasted-value") || strings.Contains(pe.Message, "xxxx") {
			t.Errorf("%s: the refusal echoes the value: %s", name, pe.Message)
		}
	}

	var pe *protocol.Error
	err := admin.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: "ws_missing"}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Errorf("unknown workspace = %v, want CodeNotFound", err)
	}
}

func TestSetupFailureReachesTheLauncherAsData(t *testing.T) {
	got := rpcError(fmt.Errorf("launch: %w", &scheduler.SetupFailure{Output: "npm error code E401\n"}))
	var failure protocol.SetupFailure
	if err := json.Unmarshal(got.Data, &failure); err != nil || failure.SetupOutput != "npm error code E401\n" {
		t.Fatalf("data = %s, %v, want the setup output", got.Data, err)
	}
	if strings.Contains(got.Message, "E401") {
		t.Fatalf("message = %q, want the output only in data", got.Message)
	}
}
