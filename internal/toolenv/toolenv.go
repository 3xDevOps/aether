// Package toolenv manages server-owned immutable workspace tool trees.
package toolenv

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

var (
	ErrInvalidIdentifier = errors.New("toolenv: invalid identifier")
	ErrTraversal         = errors.New("toolenv: path traversal")
	ErrSymlink           = errors.New("toolenv: symlink is not allowed")
)

type Manager struct {
	root  string
	store store.Store
}

// NewManager creates a manager rooted at one server-owned directory. An
// optional store enables promotion and active-head operations.
func NewManager(root string, stores ...store.Store) (*Manager, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("toolenv: root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("toolenv: create root: %w", err)
	}
	if info, err := os.Lstat(root); err != nil {
		return nil, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	m := &Manager{root: root}
	if len(stores) > 0 {
		m.store = stores[0]
	}
	if err := os.MkdirAll(filepath.Join(root, "staging"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		return nil, err
	}
	return m, nil
}

// New is a short alias for NewManager.
func New(root string, stores ...store.Store) (*Manager, error) { return NewManager(root, stores...) }

func (m *Manager) Root() string         { return m.root }
func (m *Manager) SnapshotRoot() string { return filepath.Join(m.root, "snapshots") }

func validIdentifier(id string) bool {
	return id != "" && id != "." && id != ".." && !filepath.IsAbs(id) &&
		!strings.ContainsAny(id, "/\\\x00") && filepath.Clean(id) == id
}

func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !validIdentifier(id) {
			return ErrInvalidIdentifier
		}
	}
	return nil
}

func (m *Manager) stagingRoot(member, workspace string) (string, error) {
	if err := checkIDs(member, workspace); err != nil {
		return "", err
	}
	return filepath.Join(m.root, "staging", member, workspace), nil
}

func (m *Manager) snapshotRoot(member, workspace string) (string, error) {
	if err := checkIDs(member, workspace); err != nil {
		return "", err
	}
	return filepath.Join(m.SnapshotRoot(), member, workspace), nil
}

// CreateStaging returns a fresh per-member and per-workspace directory.
func (m *Manager) CreateStaging(member, workspace string) (string, error) {
	base, err := m.stagingRoot(member, workspace)
	if err != nil {
		return "", err
	}
	if mkdirErr := os.MkdirAll(base, 0o700); mkdirErr != nil {
		return "", mkdirErr
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, id)
	if mkdirErr := os.Mkdir(path, 0o700); mkdirErr != nil {
		return "", mkdirErr
	}
	return path, nil
}

// StagingDir is an alias used by shell lifecycle code.
func (m *Manager) StagingDir(member, workspace string) (string, error) {
	return m.CreateStaging(member, workspace)
}

func (m *Manager) ensureStaging(path string) error {
	clean := filepath.Clean(path)
	root := filepath.Join(m.root, "staging")
	rel, err := filepath.Rel(root, clean)
	if err != nil || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrTraversal
	}
	for p := clean; ; p = filepath.Dir(p) {
		info, lstatErr := os.Lstat(p)
		if lstatErr != nil {
			return lstatErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if p == root {
			break
		}
		if p == filepath.Dir(p) {
			return ErrTraversal
		}
	}
	return nil
}
func (m *Manager) CleanupStaging(path string) error {
	if err := m.ensureStaging(path); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Clean(path))
}

func (m *Manager) snapshotPath(member, workspace, id string) (string, error) {
	base, err := m.snapshotRoot(member, workspace)
	if err != nil {
		return "", err
	}
	if idErr := checkIDs(id); idErr != nil {
		return "", idErr
	}
	return filepath.Join(base, id), nil
}
func (m *Manager) ensureSnapshot(path string) error {
	clean := filepath.Clean(path)
	root := m.SnapshotRoot()
	rel, err := filepath.Rel(root, clean)
	if err != nil || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrTraversal
	}
	for p := clean; ; p = filepath.Dir(p) {
		info, lstatErr := os.Lstat(p)
		if lstatErr != nil {
			return lstatErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if p == root {
			break
		}
		if p == filepath.Dir(p) {
			return ErrTraversal
		}
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("toolenv: snapshot is not a directory")
	}
	return nil
}

// SnapshotPath resolves one immutable snapshot after validating that the
// snapshot belongs to the requested member and workspace. It never follows
// symlinks and returns the exact snapshot tree, not the current head.
func (m *Manager) SnapshotPath(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, id domain.ToolSnapshotID) (string, error) {
	if m.store == nil {
		return "", fmt.Errorf("toolenv: store is required")
	}
	if err := checkIDs(string(member), string(workspace), string(id)); err != nil {
		return "", err
	}
	snapshot, err := m.store.GetToolSnapshot(ctx, id)
	if err != nil {
		return "", err
	}
	if snapshot.MemberID != member || snapshot.WorkspaceID != workspace {
		return "", store.ErrNotFound
	}
	path, err := m.snapshotPath(string(member), string(workspace), string(id))
	if err != nil {
		return "", err
	}
	if ensureErr := m.ensureSnapshot(path); ensureErr != nil {
		return "", ensureErr
	}
	return path, nil
}

// ResolvePath is an explicit alias for SnapshotPath used by lifecycle
// callers that resolve a pinned snapshot rather than the active head.
func (m *Manager) ResolvePath(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, id domain.ToolSnapshotID) (string, error) {
	return m.SnapshotPath(ctx, member, workspace, id)
}
func (m *Manager) duplicateSnapshot(ctx context.Context, member, workspace, digest string) (*domain.ToolSnapshot, error) {
	snapshots, err := m.store.ListToolSnapshots(ctx, domain.MemberID(member), domain.WorkspaceID(workspace))
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		if snapshot.Digest != digest {
			continue
		}
		_, resolveErr := m.SnapshotPath(ctx, snapshot.MemberID, snapshot.WorkspaceID, snapshot.ID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		return snapshot, nil
	}
	return nil, store.ErrNotFound
}

// Promote verifies and renames a completed staging tree before publishing its
// metadata and active head. The old head is untouched if either DB update fails.
func (m *Manager) Promote(ctx context.Context, member, workspace, staging string, manifest domain.ToolManifest, verify func(string) error) (*domain.ToolSnapshot, error) {
	if m.store == nil {
		return nil, fmt.Errorf("toolenv: store is required")
	}
	if err := checkIDs(member, workspace); err != nil {
		return nil, err
	}
	if err := m.ensureStaging(staging); err != nil {
		return nil, err
	}
	digest, _, err := DigestTree(staging)
	if err != nil {
		return nil, err
	}
	if manifest.Executable != "" {
		if executableIDErr := checkIDs(manifest.Executable); executableIDErr != nil {
			return nil, executableIDErr
		}
		executable := filepath.Join(staging, "bin", manifest.Executable)
		if verify != nil {
			if verifyErr := verify(executable); verifyErr != nil {
				return nil, verifyErr
			}
		} else if statErr := StatExecutable(staging, manifest.Executable); statErr != nil {
			return nil, statErr
		}
	}
	existing, duplicateErr := m.duplicateSnapshot(ctx, member, workspace, digest)
	if duplicateErr == nil {
		if headErr := m.store.SetToolHead(ctx, existing.MemberID, existing.WorkspaceID, existing.ID); headErr != nil {
			return nil, headErr
		}
		// Keep the staged tree intact. The caller or bounded staging cleanup
		// can retry removal without losing the duplicate's usable snapshot.
		return existing, nil
	} else if !errors.Is(duplicateErr, store.ErrNotFound) {
		return nil, duplicateErr
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	destination, err := m.snapshotPath(member, workspace, id)
	if err != nil {
		return nil, err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkdirErr != nil {
		return nil, mkdirErr
	}
	if renameErr := os.Rename(staging, destination); renameErr != nil {
		return nil, fmt.Errorf("toolenv: promote staging: %w", renameErr)
	}
	snapshot := &domain.ToolSnapshot{ID: domain.ToolSnapshotID(id), WorkspaceID: domain.WorkspaceID(workspace), MemberID: domain.MemberID(member), Digest: digest, Manifest: manifest}
	createErr := m.store.CreateToolSnapshot(ctx, snapshot)
	if createErr != nil {
		if errors.Is(createErr, store.ErrConflict) {
			conflictSnapshot, lookupErr := m.duplicateSnapshot(ctx, member, workspace, digest)
			if lookupErr == nil {
				if headErr := m.store.SetToolHead(ctx, conflictSnapshot.MemberID, conflictSnapshot.WorkspaceID, conflictSnapshot.ID); headErr != nil {
					return nil, headErr
				}
				// Leave the newly staged tree in place. It remains within the
				// manager root and can be reclaimed by bounded cleanup.
				return conflictSnapshot, nil
			}
			return nil, createErr
		}
		_ = os.RemoveAll(destination)
		return nil, createErr
	}
	if headErr := m.store.SetToolHead(ctx, snapshot.MemberID, snapshot.WorkspaceID, snapshot.ID); headErr != nil {
		return snapshot, headErr
	}
	return snapshot, nil
}

// StatExecutable requires bin/<name> in a tool tree (staging or snapshot) to
// be an executable regular file, resolving any symlink chain confined to the
// tree (os.Root): a link escaping the tree fails instead of statting host
// files.
func StatExecutable(tree, name string) error {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return fmt.Errorf("toolenv: open tool tree: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(filepath.Join("bin", name))
	if err != nil {
		return fmt.Errorf("toolenv: executable bin/%s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("toolenv: bin/%s is not an executable file", name)
	}
	return nil
}

func (m *Manager) ActivePath(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) (string, error) {
	if m.store == nil {
		return "", fmt.Errorf("toolenv: store is required")
	}
	s, err := m.store.GetToolHead(ctx, member, workspace)
	if err != nil {
		return "", err
	}
	return m.SnapshotPath(ctx, member, workspace, s.ID)
}

func (m *Manager) Rollback(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, id domain.ToolSnapshotID) error {
	if _, err := m.SnapshotPath(ctx, member, workspace, id); err != nil {
		return err
	}
	return m.store.SetToolHead(ctx, member, workspace, id)
}
func (m *Manager) snapshotProtected(ctx context.Context, snapshot *domain.ToolSnapshot) (bool, error) {
	head, err := m.store.GetToolHead(ctx, snapshot.MemberID, snapshot.WorkspaceID)
	if err == nil && head.ID == snapshot.ID {
		return true, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	pending, err := m.store.ListPendingWorkspaceShells(ctx, snapshot.MemberID, snapshot.WorkspaceID)
	if err != nil {
		return false, err
	}
	for _, session := range pending {
		if session.SnapshotID == snapshot.ID {
			return true, nil
		}
	}
	runs, err := m.store.ListActiveRuns(ctx)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.ToolSnapshotID == snapshot.ID && !run.Status.Terminal() {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) Reset(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) error {
	if m.store == nil {
		return fmt.Errorf("toolenv: store is required")
	}
	// Clear only metadata that can be safely removed. The active head is
	// cleared last, so a filesystem or store failure leaves the old snapshot
	// usable and permits a retry.
	pending, err := m.store.ListPendingWorkspaceShells(ctx, member, workspace)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p.StagingID != "" {
			path, e := m.stagingPathForID(member, workspace, p.StagingID)
			if e != nil {
				return e
			}
			if e = m.CleanupStaging(path); e != nil {
				return e
			}
		}
		if deleteErr := m.store.DeletePendingWorkspaceShell(ctx, p.ID); deleteErr != nil {
			return deleteErr
		}
	}
	snapshots, err := m.store.ListToolSnapshots(ctx, member, workspace)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		protected, e := m.snapshotProtected(ctx, snapshot)
		if e != nil {
			return e
		}
		if protected {
			continue
		}
		path, e := m.snapshotPath(string(member), string(workspace), string(snapshot.ID))
		if e != nil {
			return e
		}
		if e = m.ensureSnapshot(path); e != nil && !errors.Is(e, fs.ErrNotExist) {
			return e
		}
		if e = os.RemoveAll(path); e != nil {
			return e
		}
		if e = m.store.DeleteToolSnapshot(ctx, snapshot.ID); e != nil {
			if errors.Is(e, store.ErrInUse) {
				continue
			}
			return e
		}
	}
	return m.store.SetToolHead(ctx, member, workspace, "")
}

// CleanupAbandonedStaging removes at most limit abandoned staging trees older than age.
// It never invokes a shell or accesses paths outside the manager root.
func (m *Manager) CleanupAbandonedStaging(ctx context.Context, age time.Duration, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if age < 0 {
		age = 0
	}
	root := filepath.Join(m.root, "staging")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-age)
	removed := 0
	for _, member := range entries {
		if removed >= limit || !member.IsDir() {
			break
		}
		workspaces, e := os.ReadDir(filepath.Join(root, member.Name()))
		if e != nil {
			return removed, e
		}
		for _, workspace := range workspaces {
			if removed >= limit || !workspace.IsDir() {
				break
			}
			keep := make(map[string]struct{})
			if m.store != nil {
				pending, pendingErr := m.store.ListPendingWorkspaceShells(ctx, domain.MemberID(member.Name()), domain.WorkspaceID(workspace.Name()))
				if pendingErr != nil {
					return removed, pendingErr
				}
				for _, p := range pending {
					keep[p.StagingID] = struct{}{}
				}
			}
			candidates, candidatesErr := os.ReadDir(filepath.Join(root, member.Name(), workspace.Name()))
			if candidatesErr != nil {
				return removed, candidatesErr
			}
			for _, candidate := range candidates {
				if removed >= limit {
					break
				}
				if _, ok := keep[candidate.Name()]; ok {
					continue
				}
				info, infoErr := candidate.Info()
				if infoErr != nil {
					return removed, infoErr
				}
				if info.ModTime().After(cutoff) {
					continue
				}
				path := filepath.Join(root, member.Name(), workspace.Name(), candidate.Name())
				if cleanupErr := m.CleanupStaging(path); cleanupErr != nil {
					return removed, cleanupErr
				}
				removed++
			}
		}
	}
	return removed, nil
}

// CleanupPending removes at most limit expired pending sessions and their
// staging trees. Stores that do not expose the optional bounded query are
// left untouched.
func (m *Manager) CleanupPending(ctx context.Context, age time.Duration, limit int) (int, error) {
	type staleLister interface {
		ListPendingWorkspaceShellsBefore(context.Context, time.Time, int) ([]*store.PendingWorkspaceShell, error)
	}
	lister, ok := m.store.(staleLister)
	if !ok || limit <= 0 {
		return 0, nil
	}
	pending, err := lister.ListPendingWorkspaceShellsBefore(ctx, time.Now().Add(-age), limit)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, p := range pending {
		if p.StagingID != "" {
			path, e := m.stagingPathForID(p.MemberID, p.WorkspaceID, p.StagingID)
			if e != nil {
				return removed, e
			}
			if e = m.CleanupStaging(path); e != nil {
				return removed, e
			}
		}
		if deleteErr := m.store.DeletePendingWorkspaceShell(ctx, p.ID); deleteErr != nil {
			return removed, deleteErr
		}
		removed++
	}
	return removed, nil
}

func (m *Manager) stagingPathForID(member domain.MemberID, workspace domain.WorkspaceID, id string) (string, error) {
	base, err := m.stagingRoot(string(member), string(workspace))
	if err != nil {
		return "", err
	}
	if idErr := checkIDs(id); idErr != nil {
		return "", idErr
	}
	return filepath.Join(base, id), nil
}

// StagingPath resolves a pending staging identifier under the manager root.
func (m *Manager) StagingPath(member domain.MemberID, workspace domain.WorkspaceID, id string) (string, error) {
	return m.stagingPathForID(member, workspace, id)
}

// Recover removes incomplete staging trees that have no pending session.
func (m *Manager) Recover(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(m.root, "staging"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, memberEntry := range entries {
		if !memberEntry.IsDir() {
			continue
		}
		members, membersErr := os.ReadDir(filepath.Join(m.root, "staging", memberEntry.Name()))
		if membersErr != nil {
			return membersErr
		}
		for _, workspaceEntry := range members {
			if !workspaceEntry.IsDir() {
				continue
			}
			pending, pendingErr := m.store.ListPendingWorkspaceShells(ctx, domain.MemberID(memberEntry.Name()), domain.WorkspaceID(workspaceEntry.Name()))
			if pendingErr != nil {
				return pendingErr
			}
			keep := map[string]bool{}
			for _, p := range pending {
				keep[p.StagingID] = true
			}
			stagingEntries, stagingErr := os.ReadDir(filepath.Join(m.root, "staging", memberEntry.Name(), workspaceEntry.Name()))
			if stagingErr != nil {
				return stagingErr
			}
			for _, candidate := range stagingEntries {
				if !keep[candidate.Name()] {
					_ = os.RemoveAll(filepath.Join(m.root, "staging", memberEntry.Name(), workspaceEntry.Name(), candidate.Name()))
				}
			}
		}
	}
	return nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)), nil
}

// Keep time imported in API docs and ensure manifests can carry timestamps in
// callers without exposing paths.
var _ = time.Time{}
