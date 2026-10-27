package sshd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeFiles struct {
	treeErr  error
	readErr  error
	writeErr error
	diffErr  error
}

func (f *fakeFiles) FilesTree(context.Context, domain.WorkspaceID, domain.RunID, string, string) ([]gitengine.TreeEntry, error) {
	return nil, f.treeErr
}

func (f *fakeFiles) FilesRead(context.Context, domain.WorkspaceID, domain.RunID, string, string, int) (gitengine.FileRead, error) {
	return gitengine.FileRead{}, f.readErr
}

func (f *fakeFiles) FilesWrite(context.Context, domain.WorkspaceID, domain.RunID, string, string, []byte, string, domain.GitIdentity, []byte) (gitengine.FileRead, error) {
	return gitengine.FileRead{}, f.writeErr
}

func (f *fakeFiles) FileDiff(context.Context, domain.RunID, string) (gitengine.Patch, error) {
	return gitengine.Patch{}, f.diffErr
}

func TestFilesReadRejectsUnsafePathAsInvalidParams(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestFileDiffStagingLimitDoesNotClaimCheckoutRemoved(t *testing.T) {
	reader := &fakeFiles{diffErr: errors.Join(gitengine.ErrSnapshotStorageLimit, errors.New("private /host/checkout"))}
	e := newTestEnv(t, func(c *Config) { c.Services.Files = reader })
	err := controlClient(t, e).Call(protocol.MethodFilesDiff, protocol.FilesDiffParams{
		RunID: string(e.run.ID), Path: "file.txt",
	}, nil)
	pe := wireErrOf(t, err)
	if pe.Code != protocol.CodeUnavailable || pe.Message != "files.diff: current diff input exceeds 128 MiB or available disk headroom" {
		t.Fatalf("diff staging refusal = %+v", pe)
	}
}
func TestFilesWriteRequiresRunOrWorkspaceWritePermission(t *testing.T) {
	t.Parallel()
	reader := &fakeFiles{}
	e := newTestEnv(t, func(c *Config) { c.Services.Files = reader })
	viewerSigner, _ := addMember(t, e, "Viewer", domain.RoleViewer, false)
	viewer := controlAs(t, e, viewerSigner)
	for _, target := range []protocol.FilesWriteParams{
		{WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Path: "file.txt", Content: "edit"},
		{WorkspaceID: string(e.ws.ID), Path: "file.txt", Content: "edit"},
	} {
		if pe := wireErrOf(t, viewer.Call(protocol.MethodFilesWrite, target, nil)); pe.Code != protocol.CodeDenied {
			t.Errorf("viewer files.write target %#v code = %d, want %d", target.RunID, pe.Code, protocol.CodeDenied)
		}
	}
}

func TestFilesUploadWritesBytesIntoARunCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	engine, err := gitengine.New(gitengine.Config{ReposDir: filepath.Join(root, "repos"), CheckoutsDir: filepath.Join(root, "checkouts")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	e := newTestEnv(t, func(c *Config) { c.Services.Files = engine })
	repo, err := engine.InitWorkspaceRepo(t.Context(), e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	workspaceImportGit(t, root, "init", "--initial-branch=main", source)
	workspaceImportCommit(t, source)
	workspaceImportGit(t, source, "push", "-q", repo, "main")
	checkout, _, err := engine.CreateRunCheckout(t.Context(), e.ws.ID, e.run.ID, "main", "upload", "")
	if err != nil {
		t.Fatal(err)
	}

	c := controlClient(t, e)
	payload := []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe, '\n'}
	upload := protocol.FilesUploadParams{WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Path: "assets/logo.png", Content: payload}
	if err = c.Call(protocol.MethodFilesUpload, upload, nil); err != nil {
		t.Fatal(err)
	}
	if got, readErr := os.ReadFile(filepath.Join(checkout, "assets", "logo.png")); readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded file = %q (%v), want %q", got, readErr, payload)
	}

	if pe := wireErrOf(t, c.Call(protocol.MethodFilesUpload, upload, nil)); pe.Code != protocol.CodeConflict {
		t.Errorf("upload over an existing file = %+v, want conflict", pe)
	}
	base := upload
	base.RunID, base.Path = "", "base.bin"
	if pe := wireErrOf(t, c.Call(protocol.MethodFilesUpload, base, nil)); pe.Code != protocol.CodeInvalidParams || pe.Message != "run_id is required" {
		t.Errorf("upload without a run = %+v, want run_id is required", pe)
	}
	text := protocol.FilesWriteParams{WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Path: "nul.txt", Content: "a\x00b"}
	if pe := wireErrOf(t, c.Call(protocol.MethodFilesWrite, text, nil)); pe.Code != protocol.CodeInvalidParams {
		t.Errorf("files.write with a NUL byte = %+v, want invalid params", pe)
	}
}
