package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeFiles struct {
	treeErr error
	readErr error
	diffErr error
}

func (f *fakeFiles) FilesTree(context.Context, domain.WorkspaceID, domain.RunID, string, string) ([]gitengine.TreeEntry, error) {
	return nil, f.treeErr
}

func (f *fakeFiles) FilesRead(context.Context, domain.WorkspaceID, domain.RunID, string, string, int) ([]byte, bool, bool, error) {
	return nil, false, false, f.readErr
}

func (f *fakeFiles) FileDiff(context.Context, domain.RunID, string) (gitengine.Patch, error) {
	return gitengine.Patch{}, f.diffErr
}

func TestFilesReadRejectsUnsafePathAsInvalidParams(t *testing.T) {
	reader := &fakeFiles{}
	e := newTestEnv(t, func(c *Config) { c.Services.Files = reader })
	c := controlClient(t, e)
	for _, path := range []string{"../x", ".git/config"} {
		err := c.Call(protocol.MethodFilesRead, protocol.FilesReadParams{
			WorkspaceID: string(e.ws.ID),
			Path:        path,
		}, nil)
		pe := wireErrOf(t, err)
		if pe.Code != protocol.CodeInvalidParams {
			t.Errorf("files.read %q code = %d, want %d", path, pe.Code, protocol.CodeInvalidParams)
		}
		if strings.Contains(pe.Message, "/") && strings.Contains(pe.Message, "data") {
			t.Errorf("files.read %q leaked a host path: %q", path, pe.Message)
		}
	}
}

func TestFilesReadDoesNotEchoServicePath(t *testing.T) {
	reader := &fakeFiles{readErr: errors.New("open /var/lib/aether/checkouts/run: permission denied")}
	e := newTestEnv(t, func(c *Config) { c.Services.Files = reader })
	c := controlClient(t, e)
	err := c.Call(protocol.MethodFilesRead, protocol.FilesReadParams{
		WorkspaceID: string(e.ws.ID),
		Path:        "file.txt",
	}, nil)
	pe := wireErrOf(t, err)
	if pe.Code != protocol.CodeUnavailable {
		t.Fatalf("files.read service failure code = %d, want %d", pe.Code, protocol.CodeUnavailable)
	}
	if strings.Contains(pe.Message, "/var/lib/aether") {
		t.Fatalf("files.read leaked host path: %q", pe.Message)
	}
}
