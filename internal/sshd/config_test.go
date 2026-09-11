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
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
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
