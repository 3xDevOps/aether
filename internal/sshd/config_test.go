package sshd

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
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
