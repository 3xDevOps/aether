package sshd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestConfigRPCAuthorizationAndOwnLifecycle(t *testing.T) {
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) {
		c.Homes = homes
		c.Config = NewConfigBackend(homes, c.Store)
	})
	admin := controlClient(t, e)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	if _, err = homes.ConfigWrite(context.Background(), bob.ID, "claude", ".claude", "settings.json", []byte("bob"), "", nil); err != nil {
		t.Fatal(err)
	}
	viewerSigner, _ := addMember(t, e, "Viewer", domain.RoleViewer, false)

	var imported protocol.ConfigImportResult
	if err = admin.Call(protocol.MethodConfigImport, protocol.ConfigImportParams{
		Harness: "claude",
		Files: []protocol.ConfigImportFile{{
			Path:          "settings.json",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("admin")),
			Mode:          0o644,
		}},
	}, &imported); err != nil {
		t.Fatalf("admin config.import: %v", err)
	}
	if imported.Files != 1 || imported.Harness != "claude" {
		t.Fatalf("config.import result = %+v", imported)
	}
	var read protocol.ConfigFileReadResult
	if err = admin.Call(protocol.MethodConfigRead, protocol.ConfigReadParams{Harness: "claude", Path: "settings.json"}, &read); err != nil {
		t.Fatalf("admin config.read: %v", err)
	}
	if read.Content != "admin" || read.Revision == "" || !read.Writable {
		t.Fatalf("admin config.read = %+v", read)
	}
	if err = admin.Call(protocol.MethodConfigWrite, protocol.ConfigWriteParams{
		Harness: "claude", Path: "settings.json", Content: "updated", Revision: read.Revision,
	}, &read); err != nil {
		t.Fatalf("admin config.write: %v", err)
	}
	if read.Content != "updated" || read.Revision == "" {
		t.Fatalf("admin config.write result = %+v", read)
	}

	// A member selector is not part of the protocol contract. Even an admin's
	// extra JSON field cannot redirect the operation into Bob's home.
	var ignored protocol.ConfigFileReadResult
	if err = admin.Call(protocol.MethodConfigWrite, map[string]any{
		"member_id": bob.ID, "harness": "claude", "path": "settings.json",
		"content": "redirected", "revision": read.Revision,
	}, &ignored); err != nil {
		t.Fatalf("admin own config.write with ignored selector: %v", err)
	}
	bobRead, err := homes.ConfigRead(context.Background(), bob.ID, "claude", ".claude", "settings.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(bobRead.Content) != "bob" {
		t.Fatalf("admin redirected Bob's config: %q", bobRead.Content)
	}

	viewer := controlAs(t, e, viewerSigner)
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodConfigRoots, struct{}{}},
		{protocol.MethodConfigRead, protocol.ConfigReadParams{Harness: "claude", Path: "settings.json"}},
		{protocol.MethodConfigWrite, protocol.ConfigWriteParams{Harness: "claude", Path: "settings.json", Content: "viewer"}},
		{protocol.MethodConfigImport, protocol.ConfigImportParams{Harness: "claude"}},
	} {
		var pe *protocol.Error
		if err := viewer.Call(call.method, call.params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
			t.Errorf("viewer %s = %v, want CodeDenied", call.method, err)
		}
	}
}

func TestConfigImportPartialResultSurvivesCancellation(t *testing.T) {
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) {
		c.Homes = homes
		c.Config = NewConfigBackend(homes, c.Store)
	})
	home, err := homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(home, ".claude", "first.json")
	secondPath := filepath.Join(home, ".claude", "later.json")
	ctx := &cancelWhenFileAppears{Context: context.Background(), path: firstPath, done: make(chan struct{})}
	params := protocol.ConfigImportParams{
		Harness: "claude",
		Files: []protocol.ConfigImportFile{
			{Path: "first.json", ContentBase64: base64.StdEncoding.EncodeToString([]byte("first")), Mode: 0o644},
			{Path: "later.json", ContentBase64: base64.StdEncoding.EncodeToString([]byte("later")), Mode: 0o644},
		},
	}
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	raw, perr := e.srv.Local(e.member.ID).Call(ctx, protocol.MethodConfigImport, payload)
	if perr != nil {
		t.Fatalf("partial config.import RPC error = %v", perr)
	}
	var result protocol.ConfigImportResult
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.Bytes != int64(len("first")) {
		t.Fatalf("partial result counts = %+v", result)
	}
	if len(result.ImportedPaths) != 1 || result.ImportedPaths[0] != "first.json" {
		t.Fatalf("partial result paths = %v", result.ImportedPaths)
	}
	if !strings.Contains(result.Error, "later.json") || !strings.Contains(result.Error, "context canceled") {
		t.Fatalf("partial result error = %q", result.Error)
	}
	content, err := os.ReadFile(firstPath)
	if err != nil || string(content) != "first" {
		t.Fatalf("first file = %q, err=%v", content, err)
	}
	if _, err = os.Stat(secondPath); !os.IsNotExist(err) {
		t.Fatalf("later file stat = %v, want absent", err)
	}
}

type cancelWhenFileAppears struct {
	context.Context
	path string
	done chan struct{}
	once sync.Once
}

func (c *cancelWhenFileAppears) Done() <-chan struct{} {
	return c.done
}

func (c *cancelWhenFileAppears) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
	}
	if _, err := os.Stat(c.path); err == nil {
		c.once.Do(func() { close(c.done) })
		return context.Canceled
	}
	return c.Context.Err()
}

func TestConfigRootRuntimeIgnoresMatchImportExclusions(t *testing.T) {
	ctx := context.Background()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) {
		c.Homes = homes
		c.Config = NewConfigBackend(homes, c.Store)
	})

	custom := harness.Definition{
		Name:         "mybot",
		TUIArgs:      []string{"mybot", harness.TaskPlaceholder},
		HeadlessArgs: []string{"mybot", "-p", harness.TaskPlaceholder},
		Executable:   "mybot",
		ProfileRoot:  "/root/.mybot",
	}
	definition, err := json.Marshal(custom)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.store.UpsertHarnessDefinition(ctx, &store.HarnessDefinition{
		MemberID: e.member.ID, Name: custom.Name, Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}

	backend := NewConfigBackend(homes, e.store)
	raw, perr := e.srv.Local(e.member.ID).Call(ctx, protocol.MethodConfigRoots, json.RawMessage("{}"))
	if perr != nil {
		t.Fatalf("config.roots: %v", perr)
	}
	var rootsResult protocol.ConfigRootsResult
	if err = json.Unmarshal(raw, &rootsResult); err != nil {
		t.Fatal(err)
	}
	roots := rootsResult.Roots
	byHarness := make(map[string]protocol.ConfigRoot, len(roots))
	for _, root := range roots {
		if root.RuntimeIgnores == nil {
			t.Fatalf("%s runtime_ignores must be an array: %+v", root.Harness, root)
		}
		byHarness[root.Harness] = root
	}
	for _, name := range []string{"claude", "omp", "mybot"} {
		if _, ok := byHarness[name]; !ok {
			t.Fatalf("config.roots omitted %q: %+v", name, roots)
		}
	}
	if !containsString(byHarness["claude"].RuntimeIgnores, "projects/") ||
		containsString(byHarness["omp"].RuntimeIgnores, "projects/") ||
		len(byHarness["mybot"].RuntimeIgnores) != 0 {
		t.Fatalf("runtime policies are not harness-specific: claude=%v omp=%v mybot=%v",
			byHarness["claude"].RuntimeIgnores, byHarness["omp"].RuntimeIgnores,
			byHarness["mybot"].RuntimeIgnores)
	}

	cases := []struct {
		name     string
		files    []memberhome.ConfigFile
		accepted []string
		excluded map[string]string
	}{
		{
			name: "claude",
			files: []memberhome.ConfigFile{
				{Path: "projects/transcript.json", Content: []byte{}},
				{Path: "agent/sessions/session.json", Content: []byte{}},
			},
			accepted: []string{"agent/sessions/session.json"},
			excluded: map[string]string{"projects/transcript.json": "ignored"},
		},
		{
			name: "omp",
			files: []memberhome.ConfigFile{
				{Path: "projects/transcript.json", Content: []byte{}},
				{Path: "agent/sessions/session.json", Content: []byte{}},
			},
			accepted: []string{"projects/transcript.json"},
			excluded: map[string]string{"agent/sessions/session.json": "ignored"},
		},
		{
			name: "mybot",
			files: []memberhome.ConfigFile{
				{Path: "projects/transcript.json", Content: []byte{}},
			},
			accepted: []string{"projects/transcript.json"},
			excluded: map[string]string{},
		},
	}
	home, err := homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := backend.Import(ctx, e.member.ID, tc.name, tc.files)
			if err != nil {
				t.Fatalf("config import: %v", err)
			}
			if result.Files != len(tc.accepted) {
				t.Fatalf("accepted files = %d, want %d (%+v)", result.Files, len(tc.accepted), result)
			}
			if len(result.Excluded) != len(tc.excluded) {
				t.Fatalf("excluded files = %+v, want %+v", result.Excluded, tc.excluded)
			}
			for _, excluded := range result.Excluded {
				if want, ok := tc.excluded[excluded.Path]; !ok || excluded.Reason != want {
					t.Errorf("excluded %q reason = %q, want %q", excluded.Path, excluded.Reason, want)
				}
			}
			rootPath := filepath.FromSlash(strings.TrimPrefix(byHarness[tc.name].Path, "~/"))
			for _, accepted := range tc.accepted {
				target := filepath.Join(home, rootPath, filepath.FromSlash(accepted))
				if _, err := os.Stat(target); err != nil {
					t.Errorf("accepted file %q not installed at %s: %v", accepted, target, err)
				}
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
