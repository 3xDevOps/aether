package gitengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
)

type candidateVerificationMeta struct {
	Workspace      domain.WorkspaceID `json:"workspace"`
	CandidateID    string             `json:"candidate_id"`
	VerificationID string             `json:"verification_id"`
	Revision       string             `json:"revision"`
}

func (e *Engine) candidateVerificationMetaPath(id, verificationID string) (string, error) {
	p, err := e.candidateVerificationPath(id, verificationID)
	if err != nil {
		return "", err
	}
	return p + ".json", nil
}

func (e *Engine) loadCandidateVerificationMeta(id, verificationID string) (candidateVerificationMeta, error) {
	p, err := e.candidateVerificationMetaPath(id, verificationID)
	if err != nil {
		return candidateVerificationMeta{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return candidateVerificationMeta{}, fmt.Errorf("%w: verification %s", ErrCandidateNotFound, verificationID)
		}
		return candidateVerificationMeta{}, err
	}
	var meta candidateVerificationMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return candidateVerificationMeta{}, err
	}
	if meta.CandidateID != id || meta.VerificationID != verificationID || meta.Workspace == "" {
		return candidateVerificationMeta{}, ErrCandidateNotFound
	}
	return meta, nil
}

func (e *Engine) saveCandidateVerificationMeta(id, verificationID string, meta candidateVerificationMeta) error {
	p, err := e.candidateVerificationMetaPath(id, verificationID)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(p), 0o700); mkdirErr != nil {
		return mkdirErr
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".verification-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if chmodErr := tmp.Chmod(0o600); chmodErr != nil {
		_ = tmp.Close()
		return chmodErr
	}
	if _, writeErr := tmp.Write(data); writeErr != nil {
		_ = tmp.Close()
		return writeErr
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		_ = tmp.Close()
		return syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	if renameErr := os.Rename(tmpName, p); renameErr != nil {
		return renameErr
	}
	dir, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// CandidateVerificationCheckout creates an independent disposable clone. Its
// Git database is inside the disposable tree; post-run checks use a separate
// server-owned scratch database and never inspect this mutable database.
func (e *Engine) CandidateVerificationCheckout(ctx context.Context, ws domain.WorkspaceID, id, verificationID, revision string) (string, error) {
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	if err := e.checkCandidate(ctx, ws, id, revision); err != nil {
		return "", err
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return "", err
	}
	p, err := e.candidateVerificationPath(id, verificationID)
	if err != nil {
		return "", err
	}
	candidateCheckout, err := e.candidatePath(id)
	if err != nil {
		return "", err
	}
	if meta, mErr := e.loadCandidateVerificationMeta(id, verificationID); mErr == nil {
		if meta.Workspace != ws || meta.Revision != revision {
			return "", fmt.Errorf("gitengine: verification revision is immutable")
		}
		return p, nil
	} else if !errors.Is(mErr, ErrCandidateNotFound) {
		return "", mErr
	}
	if _, gitErr := e.git(ctx, repo, "cat-file", "-e", revision+"^{commit}"); gitErr != nil {
		return "", gitErr
	}
	if _, statErr := os.Lstat(p); statErr == nil {
		return "", fmt.Errorf("gitengine: verification checkout exists without metadata")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(p), 0o700); mkdirErr != nil {
		return "", mkdirErr
	}
	if _, cloneErr := e.git(ctx, "", "clone", "--local", "--no-hardlinks", "--no-checkout", candidateCheckout, p); cloneErr != nil {
		return "", fmt.Errorf("gitengine: create verification checkout: %w", cloneErr)
	}
	scratch, err := verificationScratchPath(e, id, verificationID)
	if err != nil {
		_ = os.RemoveAll(p)
		return "", err
	}
	cleanup := func() {
		_ = os.RemoveAll(p)
		_ = os.RemoveAll(scratch)
		mp, _ := e.candidateVerificationMetaPath(id, verificationID)
		if mp != "" {
			_ = os.Remove(mp)
		}
	}
	if _, err := e.git(ctx, p, "checkout", "--detach", revision); err != nil {
		cleanup()
		return "", err
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		cleanup()
		return "", err
	}
	if err := e.ensureScratchGitDir(ctx, scratch, p); err != nil {
		cleanup()
		return "", err
	}
	if err := configureCandidateScratchObjects(repo, scratch); err != nil {
		cleanup()
		return "", err
	}
	index := filepath.Join(scratch, "index")
	if _, _, err := e.gitWorktree(ctx, p, scratch, index, -1, "read-tree", revision); err != nil {
		cleanup()
		return "", err
	}
	if _, _, err := e.gitWorktree(ctx, p, scratch, index, -1, "checkout-index", "-a", "-f"); err != nil {
		cleanup()
		return "", err
	}
	meta := candidateVerificationMeta{Workspace: ws, CandidateID: id, VerificationID: verificationID, Revision: revision}
	if err := e.saveCandidateVerificationMeta(id, verificationID, meta); err != nil {
		cleanup()
		return "", err
	}
	return p, nil
}

func verificationScratchPath(e *Engine, id, verificationID string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	if err := validateID(verificationID); err != nil {
		return "", err
	}
	return filepath.Join(e.cfg.CheckoutsDir, "verification-git", id, verificationID), nil
}
func configureCandidateScratchObjects(repo, scratch string) error {
	info := filepath.Join(scratch, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(repo, "objects")+"\n"), 0o600)
}

// CheckCandidateVerification compares the disposable worktree to the exact
// frozen tree using a server-owned scratch index. Tracked ignored files,
// deletions, and mode changes are all included; mutable checkout config is not
// consulted.
func (e *Engine) CheckCandidateVerification(ctx context.Context, ws domain.WorkspaceID, id, verificationID, revision string) error {
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	if err := e.checkCandidate(ctx, ws, id, revision); err != nil {
		return err
	}
	meta, err := e.loadCandidateVerificationMeta(id, verificationID)
	if err != nil {
		return err
	}
	if meta.Workspace != ws || meta.Revision != revision {
		return fmt.Errorf("gitengine: verification identity mismatch")
	}
	workTree, err := e.candidateVerificationPath(id, verificationID)
	if err != nil {
		return err
	}
	if fi, statErr := os.Lstat(workTree); statErr != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gitengine: verification checkout unavailable")
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return err
	}
	if !validObjectID(revision) {
		return candidateObjectError("verification revision", revision)
	}
	scratch, err := verificationScratchPath(e, id, verificationID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return err
	}
	if err := e.ensureScratchGitDir(ctx, scratch, workTree); err != nil {
		return err
	}
	if err := configureCandidateScratchObjects(repo, scratch); err != nil {
		return err
	}
	index := filepath.Join(scratch, "index")
	if _, _, err := e.gitWorktree(ctx, workTree, scratch, index, -1, "read-tree", revision); err != nil {
		return err
	}
	if _, _, err := e.gitWorktree(ctx, workTree, scratch, index, -1, "diff", "--quiet", "--no-ext-diff", "--no-renames"); err != nil {
		return fmt.Errorf("%w: %v", ErrCandidateVerificationChanged, err)
	}
	return nil
}

// RemoveCandidateVerification is restart-safe and only removes this
// candidate-owned disposable tree and its server-owned check database.
func (e *Engine) RemoveCandidateVerification(ctx context.Context, ws domain.WorkspaceID, id, verificationID string) error {
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.candidateVerificationPath(id, verificationID)
	if err != nil {
		return err
	}
	mp, err := e.candidateVerificationMetaPath(id, verificationID)
	if err != nil {
		return err
	}
	scratch, err := verificationScratchPath(e, id, verificationID)
	if err != nil {
		return err
	}
	meta, err := e.loadCandidateVerificationMeta(id, verificationID)
	if err != nil {
		if errors.Is(err, ErrCandidateNotFound) {
			if removeErr := os.RemoveAll(p); removeErr != nil {
				return removeErr
			}
			if removeErr := os.Remove(mp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
			return os.RemoveAll(scratch)
		}
		return err
	}
	if meta.Workspace != ws {
		return fmt.Errorf("gitengine: verification workspace mismatch")
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	if err := os.Remove(mp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(scratch); err != nil {
		return err
	}
	return nil
}
