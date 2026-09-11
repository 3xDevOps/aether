package mirror

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestDisableWithSQLiteStoreDeletesRow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}()
	ctx := context.Background()
	workspace := &domain.Workspace{Name: "mirror-test"}
	if createWorkspaceErr := db.CreateWorkspace(ctx, workspace); createWorkspaceErr != nil {
		t.Fatal(createWorkspaceErr)
	}
	svc, err := New(Config{Root: t.TempDir(), Store: db, Git: &mirrorTestGit{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ctx, workspace.ID, ConfigureRequest{
		SourceURL: "https://example.test/source", Branch: "main", Auth: domain.MirrorAuthPublic,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Disable(ctx, workspace.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := db.GetWorkspaceMirror(ctx, workspace.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace mirror after disable = %v, want ErrNotFound", err)
	}
}
