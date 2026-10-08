package sshd

import (
	"bytes"
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
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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

func TestOpenCodeConfigUsesNativeHomeAndPreservesAuth(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) {
		c.Homes = homes
		c.Config = NewConfigBackend(homes, c.Store)
	})
	client := controlClient(t, e)
	home, err := homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	sentinels := map[string]string{
		".local/share/opencode/auth.json":     "data-login-sentinel",
		".local/share/opencode/opencode.json": "previous-import-sentinel",
		".config/opencode/auth.json":          "config-login-sentinel",
	}
	for rel, content := range sentinels {
		dest := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var roots protocol.ConfigRootsResult
	if err := client.Call(protocol.MethodConfigRoots, struct{}{}, &roots); err != nil {
		t.Fatal(err)
	}
	var root protocol.ConfigRoot
	for _, candidate := range roots.Roots {
		if candidate.Harness == "opencode" {
			root = candidate
			break
		}
	}
	if root.Path != "~/.config/opencode" || !containsString(root.CredentialNames, "auth.json") {
		t.Fatalf("OpenCode destination/credential policy = %+v", root)
	}
	const settings = "{\"model\":\"example/initial\"}\n"
	const plugin = "export default async () => ({})\n"
	var imported protocol.ConfigImportResult
	if err := client.Call(protocol.MethodConfigImport, protocol.ConfigImportParams{
		Harness: "opencode",
		Files: []protocol.ConfigImportFile{
			{Path: "opencode.json", ContentBase64: base64.StdEncoding.EncodeToString([]byte(settings)), Mode: 0o644},
			{Path: "plugins/local.js", ContentBase64: base64.StdEncoding.EncodeToString([]byte(plugin)), Mode: 0o644},
			{Path: "auth.json", ContentBase64: base64.StdEncoding.EncodeToString([]byte("must-not-overwrite"))},
		},
	}, &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Error != "" || imported.Files != 2 || imported.Bytes != int64(len(settings)+len(plugin)) {
		t.Fatalf("config.import = %+v", imported)
	}
	if len(imported.Excluded) != 1 || imported.Excluded[0].Path != "auth.json" || imported.Excluded[0].Reason != "credential" {
		t.Fatalf("credential exclusion = %+v", imported.Excluded)
	}
	for rel, content := range map[string]string{"opencode.json": settings, "plugins/local.js": plugin} {
		var read protocol.ConfigFileReadResult
		if err := client.Call(protocol.MethodConfigRead, protocol.ConfigReadParams{Harness: "opencode", Path: rel}, &read); err != nil {
			t.Fatal(err)
		}
		if read.Content != content || !read.Writable {
			t.Fatalf("config.read %s = %+v", rel, read)
		}
		persisted, err := os.ReadFile(filepath.Join(home, ".config", "opencode", filepath.FromSlash(rel)))
		if err != nil || string(persisted) != content {
			t.Fatalf("native configuration %s = %q, %v", rel, persisted, err)
		}
		if rel == "opencode.json" {
			const updated = "{\"model\":\"example/updated\"}\n"
			if err := client.Call(protocol.MethodConfigWrite, protocol.ConfigWriteParams{
				Harness: "opencode", Path: rel, Content: updated, Revision: read.Revision,
			}, &read); err != nil {
				t.Fatal(err)
			}
			persisted, err := os.ReadFile(filepath.Join(home, ".config", "opencode", rel))
			if err != nil || string(persisted) != updated || read.Content != updated {
				t.Fatalf("updated native configuration = %q, %v; response %+v", persisted, err, read)
			}
		}
	}
	var tree protocol.ConfigTreeResult
	if err := client.Call(protocol.MethodConfigTree, protocol.ConfigTreeParams{Harness: "opencode"}, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 2 || tree.Entries[0].Name != "opencode.json" || tree.Entries[1].Name != "plugins" {
		t.Fatalf("native configuration tree = %+v", tree.Entries)
	}
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodConfigRead, protocol.ConfigReadParams{Harness: "opencode", Path: "auth.json"}},
		{protocol.MethodConfigWrite, protocol.ConfigWriteParams{Harness: "opencode", Path: "auth.json", Content: "must-not-overwrite"}},
	} {
		var perr *protocol.Error
		if err := client.Call(call.method, call.params, nil); !errors.As(err, &perr) || perr.Code != protocol.CodeDenied {
			t.Errorf("%s auth.json = %v, want CodeDenied", call.method, err)
		}
	}
	for rel, content := range sentinels {
		persisted, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil || string(persisted) != content {
			t.Errorf("sentinel %s changed: %q, %v", rel, persisted, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("plugin written into data home: %v", err)
	}
}

func TestConfigImportPartialResultSurvivesCancellation(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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

func TestConfigImportReportsZeroWritesAfterPreflightFailure(t *testing.T) {
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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
	if err = os.MkdirAll(filepath.Join(home, ".claude", "blocked.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(protocol.ConfigImportParams{
		Harness: "claude",
		Files: []protocol.ConfigImportFile{
			{Path: "settings.json", ContentBase64: "e30="},
			{Path: "auth.json", ContentBase64: "e30="},
			{Path: "blocked.json", ContentBase64: "e30="},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, perr := e.srv.Local(e.member.ID).Call(context.Background(), protocol.MethodConfigImport, params)
	if perr != nil {
		t.Fatal(perr)
	}
	var result protocol.ConfigImportResult
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == "" || result.Files != 0 || result.Bytes != 0 || len(result.ImportedPaths) != 0 {
		t.Fatalf("preflight result must confirm no writes: %+v", result)
	}
	if len(result.Excluded) != 1 || result.Excluded[0].Path != "auth.json" || result.Excluded[0].Reason != "credential" {
		t.Fatalf("preflight lost exclusions: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("preflight mutated settings: %v", err)
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
	t.Parallel()
	ctx := context.Background()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
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
		DenyNames:    []string{"session-store.json"},
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
				{Path: "stats.db", Content: []byte{}},
				{Path: "stats.db-wal", Content: []byte{}},
				{Path: "stats.db-shm", Content: []byte{}},
				{Path: "agent/extensions/stats.db", Content: []byte{}},
			},
			accepted: []string{"projects/transcript.json", "agent/extensions/stats.db"},
			excluded: map[string]string{
				"agent/sessions/session.json": "ignored",
				"stats.db":                    "ignored", "stats.db-wal": "ignored", "stats.db-shm": "ignored",
			},
		},
		{
			name: "mybot",
			files: []memberhome.ConfigFile{
				{Path: "projects/transcript.json", Content: []byte{}},
				{Path: "SESSION-STORE.JSON/data", Content: []byte("not a real credential")},
			},
			accepted: []string{"projects/transcript.json"},
			excluded: map[string]string{"SESSION-STORE.JSON/data": "credential"},
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
				matched := false
				for _, pattern := range byHarness[tc.name].RuntimeIgnores {
					prefix := strings.TrimSuffix(pattern, "/")
					matched = matched || excluded.Path == prefix || strings.HasPrefix(excluded.Path, prefix+"/")
				}
				if excluded.Reason == "credential" {
					for component := range strings.SplitSeq(excluded.Path, "/") {
						for _, name := range byHarness[tc.name].CredentialNames {
							matched = matched || strings.EqualFold(component, name)
						}
					}
				}
				if !matched {
					t.Errorf("server exclusion %s is absent from browser policy", excluded.Path)
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

func TestDecodeConfigImportFilesAcceptsFileAboveOneMiB(t *testing.T) {
	content := bytes.Repeat([]byte("x"), (1<<20)+1)
	raw, err := json.Marshal([]protocol.ConfigImportFile{{
		Path: "instructions.md", ContentBase64: base64.StdEncoding.EncodeToString(content), Mode: 0o644,
	}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := decodeConfigImportFiles(raw)
	if err != nil {
		t.Fatalf("decode above 1 MiB: %v", err)
	}
	if len(files) != 1 || !bytes.Equal(files[0].Content, content) {
		t.Fatalf("decoded content differs: files=%d", len(files))
	}
}

func TestDecodeConfigImportFilesRequestCountBound(t *testing.T) {
	files := make([]protocol.ConfigImportFile, memberhome.ConfigImportMaxFiles+1)
	for i := range files {
		files[i] = protocol.ConfigImportFile{Path: "settings.json", ContentBase64: "eA=="}
	}
	for _, count := range []int{memberhome.ConfigImportMaxFiles, memberhome.ConfigImportMaxFiles + 1} {
		raw, err := json.Marshal(files[:count])
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeConfigImportFiles(raw)
		if count > memberhome.ConfigImportMaxFiles {
			if err == nil || decoded != nil {
				t.Fatalf("over-count request returned %d files, err=%v", len(decoded), err)
			}
		} else if err != nil || len(decoded) != count || string(decoded[count-1].Content) != "x" {
			t.Fatalf("at-count request returned %d files, err=%v", len(decoded), err)
		}
	}
}

func TestDecodeConfigImportFilesRejectsInvalidContent(t *testing.T) {
	files, err := decodeConfigImportFiles(json.RawMessage(`[{"path":"settings.json","content_base64":"%%%"}]`))
	if err == nil || files != nil {
		t.Fatalf("invalid base64 returned files=%v, err=%v", files, err)
	}
}
