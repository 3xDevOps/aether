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
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/secretstore"
	"github.com/3xDevOps/Aether/internal/store"
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

func TestWorkspaceEgress(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	admin := controlAs(t, e, e.signer)
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck
	ws := string(e.ws.ID)
	on := true
	set := func(change protocol.WorkspaceEnvironmentSetParams) (protocol.WorkspaceEnvironmentResult, error) {
		change.WorkspaceID = ws
		var res protocol.WorkspaceEnvironmentResult
		callErr := admin.Call(protocol.MethodWorkspaceEnvironmentSet, change, &res)
		return res, callErr
	}

	var fresh protocol.WorkspaceEnvironmentResult
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws}, &fresh); err != nil {
		t.Fatal(err)
	}
	defaults := slices.Sorted(slices.Values(egress.Defaults))
	if fresh.Egress || !slices.Equal(fresh.EgressHosts, defaults) {
		t.Fatalf("a new workspace: egress %t, hosts %q; want off and the defaults", fresh.Egress, fresh.EgressHosts)
	}

	wantDenied(t, controlAs(t, e, collab).Call(protocol.MethodWorkspaceEnvironmentSet,
		protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, Egress: &on}, nil), "collaborator turning egress control on")
	res, err := set(protocol.WorkspaceEnvironmentSetParams{Egress: &on, EgressAllow: []string{"Mirror.Example.com."}})
	if err != nil {
		t.Fatalf("turn egress control on: %v", err)
	}
	if !res.Egress || len(res.EgressHosts) != len(defaults)+1 || !slices.IsSorted(res.EgressHosts) || !slices.Contains(res.EgressHosts, "mirror.example.com") {
		t.Fatalf("after turning it on: egress %t, hosts %q; want on, the defaults and mirror.example.com, sorted", res.Egress, res.EgressHosts)
	}
	if note, want := waitTimelineNote(t, sub), "workspace environment: egress control on; egress allowed mirror.example.com"; note != want {
		t.Fatalf("timeline note = %q, want %q", note, want)
	}

	off := false
	if res, err = set(protocol.WorkspaceEnvironmentSetParams{Egress: &off, EgressDeny: []string{"mirror.example.com", "*.cursor.sh"}}); err != nil {
		t.Fatalf("turn egress control off: %v", err)
	}
	if res.Egress || len(res.EgressHosts) != len(defaults)-1 || slices.Contains(res.EgressHosts, "*.cursor.sh") {
		t.Fatalf("after turning it off: egress %t, hosts %q; want off and the list kept without the denied hosts", res.Egress, res.EgressHosts)
	}
	if res, err = set(protocol.WorkspaceEnvironmentSetParams{Egress: &on, EgressDeny: res.EgressHosts}); err != nil || len(res.EgressHosts) != 0 {
		t.Fatalf("denying every host = %q, %v; want an empty list", res.EgressHosts, err)
	}
	var emptied json.RawMessage
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws}, &emptied); err != nil || !strings.Contains(string(emptied), `"egress_hosts":[]`) {
		t.Fatalf("reading an emptied list = %s, %v; want \"egress_hosts\":[], not the defaults or null", emptied, err)
	}

	for name, change := range map[string]protocol.WorkspaceEnvironmentSetParams{
		"URL":           {EgressAllow: []string{"https://example.com"}},
		"absent host":   {EgressDeny: []string{"absent.example.com"}},
		"inner star":    {EgressAllow: []string{"a.*.example.com"}},
		"host and port": {EgressAllow: []string{"example.com:443"}},
	} {
		var pe *protocol.Error
		if _, err = set(change); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Errorf("%s = %v, want CodeInvalidParams", name, err)
		}
	}
	stored, err := e.store.GetWorkspace(ctx, e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Environment.Egress.On || !slices.Equal(stored.Environment.Egress.Hosts, res.EgressHosts) {
		t.Fatalf("stored egress policy = %+v, want what the last change returned", stored.Environment.Egress)
	}
}

func TestWorkspaceFiles(t *testing.T) {
	t.Parallel()
	const content = "DATABASE_URL=postgres://db.example.test/app # a comment parsing would drop\n"
	secretsDir := filepath.Join(t.TempDir(), "workspace-secrets")
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secretstore.New(secretsDir) })
	ctx := context.Background()
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	admin := controlAs(t, e, e.signer)
	ws := string(e.ws.ID)
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeTimeline}}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	change := protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, Files: []protocol.WorkspaceFile{
		{Path: ".env", Content: content},
		{Path: "apps/web/.env.local", Content: "NEXT_PUBLIC_A=1\n"},
	}}
	for who, signer := range map[string]*protocol.Client{"viewer": controlAs(t, e, viewer), "collaborator": controlAs(t, e, collab)} {
		wantDenied(t, signer.Call(protocol.MethodWorkspaceEnvironmentSet, change, nil), who+" setting workspace files")
	}
	var saved json.RawMessage
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentSet, change, &saved); err != nil {
		t.Fatalf("admin sets files: %v", err)
	}
	note := waitTimelineNote(t, sub)
	if want := "workspace environment: files set: .env, apps/web/.env.local"; note != want {
		t.Fatalf("timeline note = %q, want %q", note, want)
	}
	var read json.RawMessage
	if err = controlAs(t, e, viewer).Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws}, &read); err != nil {
		t.Fatalf("viewer workspace.environment.get: %v", err)
	}
	var got protocol.WorkspaceEnvironmentResult
	if err = json.Unmarshal(read, &got); err != nil {
		t.Fatal(err)
	}
	if want := []protocol.WorkspaceFileInfo{{Path: ".env", Size: len(content)}, {Path: "apps/web/.env.local", Size: 16}}; !slices.Equal(got.Files, want) {
		t.Fatalf("files = %+v, want %+v", got.Files, want)
	}
	for where, text := range map[string]string{"set result": string(saved), "get result": string(read), "timeline": note} {
		if strings.Contains(text, "postgres://") || strings.Contains(text, "NEXT_PUBLIC_A") {
			t.Errorf("%s carries a file's content: %s", where, text)
		}
	}
	if file, readErr := os.ReadFile(filepath.Join(secretsDir, ws+".files.json")); readErr != nil || !strings.Contains(string(file), "postgres://") {
		t.Fatalf("files file = %q, %v, want it to hold the content", file, readErr)
	}

	var next protocol.WorkspaceEnvironmentResult
	if err = admin.Call(protocol.MethodWorkspaceEnvironmentSet, protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, RemoveFiles: []string{".env", "never-set"}}, &next); err != nil {
		t.Fatalf("remove a file: %v", err)
	}
	if want := []protocol.WorkspaceFileInfo{{Path: "apps/web/.env.local", Size: 16}}; !slices.Equal(next.Files, want) {
		t.Fatalf("files after removing .env = %+v, want %+v", next.Files, want)
	}
	if note = waitTimelineNote(t, sub); note != "workspace environment: files removed: .env" {
		t.Fatalf("timeline note = %q", note)
	}

	many := make([]protocol.WorkspaceFile, protocol.MaxWorkspaceFiles)
	for i := range many {
		many[i] = protocol.WorkspaceFile{Path: fmt.Sprintf("config/%d.env", i)}
	}
	for name, refused := range map[string]struct {
		files   []protocol.WorkspaceFile
		message string
	}{
		"absolute":   {[]protocol.WorkspaceFile{{Path: "/etc/app.env"}}, `file path "/etc/app.env" must be relative`},
		"parent":     {[]protocol.WorkspaceFile{{Path: "../.env"}}, `file path "../.env" must be relative`},
		"under .git": {[]protocol.WorkspaceFile{{Path: ".git/hooks/pre-commit"}}, `file path ".git/hooks/pre-commit" must be relative`},
		"not clean":  {[]protocol.WorkspaceFile{{Path: "apps/"}}, `file path "apps/" must be relative`},
		"empty":      {[]protocol.WorkspaceFile{{Path: ""}}, `file path "" must be relative`},
		"spaced end": {[]protocol.WorkspaceFile{{Path: ".env "}}, `file path ".env " has a name that starts or ends with whitespace`},
		"spaced dir": {[]protocol.WorkspaceFile{{Path: " apps/.env"}}, `file path " apps/.env" has a name that starts or ends with whitespace`},
		"too big":    {[]protocol.WorkspaceFile{{Path: ".env", Content: strings.Repeat("x", protocol.MaxWorkspaceVariableValueBytes+1)}}, "file .env is larger than 65536 bytes"},
		"too many":   {many, "a workspace holds at most 20 files"},
	} {
		var pe *protocol.Error
		err := admin.Call(protocol.MethodWorkspaceEnvironmentSet, protocol.WorkspaceEnvironmentSetParams{WorkspaceID: ws, Files: refused.files}, nil)
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams || !strings.Contains(pe.Message, refused.message) {
			t.Errorf("%s = %v, want CodeInvalidParams naming %q", name, err, refused.message)
		}
	}
}

type refusingEnvironmentStore struct {
	store.Store
	err error
}

func (s refusingEnvironmentStore) SetWorkspaceEnvironment(context.Context, domain.WorkspaceID, domain.WorkspaceEnvironment) error {
	return s.err
}

func TestWorkspaceEnvironmentSetThatHalfFails(t *testing.T) {
	const secret = "npm-token-7c2e91"
	secrets := secretstore.New(filepath.Join(t.TempDir(), "workspace-secrets"))
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secrets })
	set := func(variable protocol.WorkspaceVariable) (protocol.WorkspaceEnvironmentResult, *protocol.Error) {
		params, err := json.Marshal(protocol.WorkspaceEnvironmentSetParams{WorkspaceID: string(e.ws.ID), Set: []protocol.WorkspaceVariable{variable}})
		if err != nil {
			t.Fatal(err)
		}
		result, perr := e.srv.workspaceEnvironmentSet(context.Background(), e.member.ID, params)
		got, _ := result.(protocol.WorkspaceEnvironmentResult)
		return got, perr
	}
	if _, perr := set(protocol.WorkspaceVariable{Name: "NPM_TOKEN", Value: secret, Secret: true}); perr != nil {
		t.Fatalf("set the secret: %+v", perr)
	}

	// A secret turning plain leaves the secret file before the row has it.
	e.srv.cfg.Store = refusingEnvironmentStore{Store: e.store, err: errors.New("database is locked")}
	if _, perr := set(protocol.WorkspaceVariable{Name: "NPM_TOKEN", Value: "now-plain"}); perr == nil || !strings.Contains(perr.Message, "database is locked") {
		t.Fatalf("set with the row update refused = %+v, want the store's error", perr)
	}
	if kept, err := secrets.Get(e.ws.ID); err != nil || kept["NPM_TOKEN"] != secret {
		t.Fatalf("secrets after the refused save = %v, %v, want NPM_TOKEN as it was", kept, err)
	}
	e.srv.cfg.Store = e.store

	e.srv.cfg.Bus = &failingPublishBus{Bus: e.bus, failAt: 1, err: errors.New("event log unavailable")}
	got, perr := set(protocol.WorkspaceVariable{Name: "NODE_ENV", Value: "test"})
	want := []protocol.WorkspaceVariable{{Name: "NODE_ENV", Value: "test"}, {Name: "NPM_TOKEN", Secret: true}}
	if perr != nil || !slices.Equal(got.Variables, want) || !strings.Contains(got.Warning, "timeline note was not recorded: event log unavailable") {
		t.Fatalf("set with the timeline note refused = %+v, %+v, want the change saved and a warning", got, perr)
	}
}

func TestWorkspaceEnvironmentSetWaitsForALaunchReadingIt(t *testing.T) {
	secrets := secretstore.New(t.TempDir())
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secrets })
	params, err := json.Marshal(protocol.WorkspaceEnvironmentSetParams{
		WorkspaceID: string(e.ws.ID),
		Set:         []protocol.WorkspaceVariable{{Name: "NPM_TOKEN", Value: "npm-token-7c2e91", Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	secrets.RLock()
	saved := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.workspaceEnvironmentSet(context.Background(), e.member.ID, params)
		saved <- perr
	}()
	select {
	case perr := <-saved:
		t.Fatalf("set returned while a launch was reading the environment: %+v", perr)
	case <-time.After(50 * time.Millisecond):
	}
	if kept, err := secrets.Get(e.ws.ID); err != nil || len(kept) != 0 {
		t.Fatalf("secrets written under the launch's read = %d, %v", len(kept), err)
	}
	secrets.RUnlock()
	if perr := <-saved; perr != nil {
		t.Fatalf("set after the launch read: %+v", perr)
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
