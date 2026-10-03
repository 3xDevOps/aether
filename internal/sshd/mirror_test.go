package sshd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type mirrorRPCFake struct {
	mu              sync.Mutex
	result          mirrorservice.Result
	statusErr       error
	configureErr    error
	refreshErr      error
	adoptErr        error
	disableErr      error
	calls           []string
	adoptGeneration int64
}

func (f *mirrorRPCFake) Configure(_ context.Context, _ domain.WorkspaceID, req mirrorservice.ConfigureRequest) (mirrorservice.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "configure:"+string(req.Auth))
	if f.configureErr != nil {
		return mirrorservice.Result{}, f.configureErr
	}
	return f.result, nil
}
func (f *mirrorRPCFake) Status(_ context.Context, _ domain.WorkspaceID) (mirrorservice.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "status")
	if f.statusErr != nil {
		return mirrorservice.Result{}, f.statusErr
	}
	return f.result, nil
}
func (f *mirrorRPCFake) Refresh(_ context.Context, _ domain.WorkspaceID) (mirrorservice.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "refresh")
	if f.refreshErr != nil {
		return mirrorservice.Result{}, f.refreshErr
	}
	return f.result, nil
}
func (f *mirrorRPCFake) Adopt(_ context.Context, _ domain.WorkspaceID, generation int64) (mirrorservice.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "adopt")
	f.adoptGeneration = generation
	if f.adoptErr != nil {
		return mirrorservice.Result{}, f.adoptErr
	}
	return f.result, nil
}
func (f *mirrorRPCFake) Disable(_ context.Context, _ domain.WorkspaceID) (mirrorservice.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "disable")
	if f.disableErr != nil {
		return mirrorservice.Result{}, f.disableErr
	}
	return f.result, nil
}
func (f *mirrorRPCFake) Capture(context.Context, domain.WorkspaceID, string) (mirrorservice.CaptureResult, error) {
	return mirrorservice.CaptureResult{}, errors.New("not used")
}

func mirrorRPCState(id domain.WorkspaceID) domain.WorkspaceMirror {
	stamp := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	return domain.WorkspaceMirror{
		WorkspaceID: id, SourceURL: "https://example.test/acme/repo", SourceIdentity: "example.test/acme/repo",
		Branch: "main", Auth: domain.MirrorAuthDeployKey, Generation: 7, Status: domain.MirrorStatusReady,
		ObservedCommit: strings.Repeat("a", 40), AcceptedCommit: strings.Repeat("b", 40),
		KeyFingerprint: "SHA256:ZmFrZUZpbmdlcnByaW50", CreatedAt: stamp, UpdatedAt: stamp,
		LastAttemptAt: stamp, LastSuccessAt: stamp,
	}
}

func TestWorkspaceMirrorStatusReadAccess(t *testing.T) {
	t.Parallel()
	for _, role := range []domain.Role{domain.RoleCollaborator, domain.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			fake := &mirrorRPCFake{statusErr: &gitengine.MirrorError{Kind: gitengine.MirrorErrorNotConfigured}}
			e := newTestEnv(t, func(c *Config) {
				c.Services.Mirrors = fake
				c.revalidateInterval = time.Hour
			})
			signer, member := addMember(t, e, "Reader", role, false)
			client := controlAs(t, e, signer)
			params := protocol.WorkspaceMirrorParams{WorkspaceID: string(e.ws.ID)}
			var local protocol.WorkspaceMirrorResult
			if err := client.Call(protocol.MethodWorkspaceMirrorStatus, params, &local); err != nil {
				t.Fatalf("local-only status: %v", err)
			}
			if local != (protocol.WorkspaceMirrorResult{}) {
				t.Fatalf("local-only status = %+v, want empty disabled state", local)
			}

			fake.mu.Lock()
			fake.statusErr = nil
			fake.result = mirrorservice.Result{
				Mirror: mirrorRPCState(e.ws.ID), PublicKey: "ssh-ed25519 AAAA-public-key",
			}
			fake.mu.Unlock()
			var configured protocol.WorkspaceMirrorResult
			if err := client.Call(protocol.MethodWorkspaceMirrorStatus, params, &configured); err != nil {
				t.Fatalf("configured status: %v", err)
			}
			if !configured.Enabled || configured.SourceURL != "https://example.test/acme/repo" ||
				configured.Branch != "main" || configured.Generation != 7 ||
				configured.Auth != "deploy-key" || configured.Status != "ready" ||
				configured.PublicKey != "ssh-ed25519 AAAA-public-key" {
				t.Fatalf("configured status = %+v", configured)
			}

			for _, method := range []string{protocol.MethodWorkspaceMirrorRefresh, protocol.MethodWorkspaceMirrorDisable} {
				wantDenied(t, client.Call(method, params, nil), method)
			}
			wantDenied(t, client.Call(protocol.MethodWorkspaceMirrorConfigure, protocol.WorkspaceMirrorConfigureParams{
				WorkspaceID: string(e.ws.ID), SourceURL: "https://example.test/repo", Branch: "main", Auth: "public",
			}, nil), "mirror configure")
			wantDenied(t, client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{
				WorkspaceID: string(e.ws.ID), Generation: 7,
			}, nil), "mirror adopt")

			if err := e.store.DeleteMember(context.Background(), member.ID); err != nil {
				t.Fatalf("delete member: %v", err)
			}
			wantDenied(t, client.Call(protocol.MethodWorkspaceMirrorStatus, params, nil), "revoked member mirror status")
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if strings.Join(fake.calls, ",") != "status,status" {
				t.Fatalf("service calls = %v, want only admitted status reads", fake.calls)
			}
		})
	}
}

func TestWorkspaceMirrorStatusPendingMemberDenied(t *testing.T) {
	t.Parallel()
	fake := &mirrorRPCFake{}
	e := newTestEnv(t, func(c *Config) { c.Services.Mirrors = fake })
	signer, _ := addMember(t, e, "Pending", domain.RoleCollaborator, true)
	client := controlAs(t, e, signer)
	wantDenied(t, client.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{
		WorkspaceID: string(e.ws.ID),
	}, nil), "pending member mirror status")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Fatalf("pending member reached mirror service: %v", fake.calls)
	}
}

func TestWorkspaceMirrorLifecycleAndSanitizedTimeline(t *testing.T) {
	t.Parallel()
	fake := &mirrorRPCFake{}
	e := newTestEnv(t, func(c *Config) { c.Services.Mirrors = fake })
	fake.result = mirrorservice.Result{
		Mirror: mirrorRPCState(e.ws.ID), PublicKey: "ssh-ed25519 AAAA-public-key", Warning: "remote deploy-key revocation remains external",
	}
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{Filter: events.Filter{Workspace: e.ws.ID, Types: []events.Type{events.TypeTimeline}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	client := controlClient(t, e)
	var configured protocol.WorkspaceMirrorResult
	if err := client.Call(protocol.MethodWorkspaceMirrorConfigure, protocol.WorkspaceMirrorConfigureParams{
		WorkspaceID: string(e.ws.ID), SourceURL: fake.result.Mirror.SourceURL, Branch: "main", Auth: "deploy-key", KnownHosts: "known",
	}, &configured); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !configured.Enabled || configured.PublicKey == "" || configured.SourceURL != fake.result.Mirror.SourceURL || configured.Generation != 7 {
		t.Fatalf("configure result = %+v", configured)
	}
	if strings.Contains(configured.PublicKey, "private") || configured.Warning == "" {
		t.Fatalf("configure result leaked or omitted public data: %+v", configured)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.TimelinePayload)
		if !ok || p.Kind != events.TimelineNote || ev.ActorID != e.member.ID {
			t.Fatalf("timeline event = %+v actor=%s", ev.Payload, ev.ActorID)
		}
		for _, want := range []string{"source=https://example.test/acme/repo", "branch=main", "generation=7", "commit=" + fake.result.Mirror.AcceptedCommit} {
			if !strings.Contains(p.Message, want) {
				t.Fatalf("timeline message %q missing %q", p.Message, want)
			}
		}
		if strings.Contains(p.Message, "private") || strings.Contains(p.Message, "ssh-ed25519 AAAA-public-key") {
			t.Fatalf("timeline leaked key data: %q", p.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no configure timeline event")
	}

	var refreshed, adopted, disabled protocol.WorkspaceMirrorResult
	if err := client.Call(protocol.MethodWorkspaceMirrorRefresh, protocol.WorkspaceMirrorParams{WorkspaceID: string(e.ws.ID)}, &refreshed); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: string(e.ws.ID), Generation: 7}, &adopted); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := client.Call(protocol.MethodWorkspaceMirrorDisable, protocol.WorkspaceMirrorParams{WorkspaceID: string(e.ws.ID)}, &disabled); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("disable result enabled=true")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if strings.Join(fake.calls, ",") != "configure:deploy-key,refresh,adopt,disable" || fake.adoptGeneration != 7 {
		t.Fatalf("service calls = %v generation=%d", fake.calls, fake.adoptGeneration)
	}
}

func TestWorkspaceMirrorInputValidation(t *testing.T) {
	t.Parallel()
	fake := &mirrorRPCFake{}
	e := newTestEnv(t, func(c *Config) { c.Services.Mirrors = fake })
	client := controlClient(t, e)
	cases := []struct {
		name   string
		method string
		params any
	}{
		{"status workspace", protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{}},
		{"configure source", protocol.MethodWorkspaceMirrorConfigure, protocol.WorkspaceMirrorConfigureParams{WorkspaceID: string(e.ws.ID), Branch: "main", Auth: "public"}},
		{"configure auth", protocol.MethodWorkspaceMirrorConfigure, protocol.WorkspaceMirrorConfigureParams{WorkspaceID: string(e.ws.ID), SourceURL: "https://example.test/repo", Branch: "main", Auth: "other"}},
		{"adopt generation", protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{WorkspaceID: string(e.ws.ID)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := client.Call(tc.method, tc.params, nil)
			if err == nil || wireErrOf(t, err).Code != protocol.CodeInvalidParams {
				t.Fatalf("error = %v, want invalid params", err)
			}
		})
	}
}
