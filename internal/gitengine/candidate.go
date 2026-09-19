package gitengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

const (
	candidateRefRoot        = "refs/aether/candidates/"
	candidateMaxInputs      = 32
	candidateMaxResolutions = 256
)

var (
	ErrCandidateNotFound            = errors.New("gitengine: candidate not found")
	ErrCandidateFrozen              = errors.New("gitengine: candidate is frozen")
	ErrCandidateConflict            = errors.New("gitengine: candidate has unresolved conflicts")
	ErrCandidateReceiptConflict     = errors.New("gitengine: candidate delivery receipt conflict")
	ErrCandidateVerificationChanged = errors.New("gitengine: candidate verification checkout changed")
)

// CandidateRevisionInput identifies one retained evidence snapshot and its
// authoritative source base. Revision and Base must be complete object IDs.
type CandidateRevisionInput struct {
	Revision string `json:"revision"`
	Base     string `json:"base"`
}

// CandidateAssembly is the durable result of candidate assembly. Conflicts
// are relative repository paths in deterministic order.
type CandidateAssembly struct {
	Revision      string   `json:"revision"`
	Conflicts     []string `json:"conflicts,omitempty"`
	AppliedInputs int      `json:"applied_inputs"`
}

// CandidateResolution is one path-safe edit to a conflicted candidate.
type CandidateResolution struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Delete  bool   `json:"delete,omitempty"`
}

// CandidateGitReceipt is the durable result of a candidate delivery. A
// proposal ref is populated for proposal actions and is otherwise empty.
type CandidateGitReceipt struct {
	Result           string `json:"result"`
	ProposalRef      string `json:"proposal_ref,omitempty"`
	PreviousRevision string `json:"previous_revision,omitempty"`
	Revision         string `json:"revision"`
}

type candidateMeta struct {
	Workspace      domain.WorkspaceID       `json:"workspace"`
	CandidateID    string                   `json:"candidate_id"`
	TargetRevision string                   `json:"target_revision"`
	Revision       string                   `json:"revision"`
	Inputs         []CandidateRevisionInput `json:"inputs,omitempty"`
	AppliedInputs  int                      `json:"applied_inputs"`
	PendingInput   int                      `json:"pending_input,omitempty"`
	Conflicts      []string                 `json:"conflicts,omitempty"`
	Frozen         bool                     `json:"frozen"`
	CreatedAt      time.Time                `json:"created_at"`
}

func candidateInputRef(id string, index int) string {
	return candidateRefRoot + id + "/inputs/" + fmt.Sprint(index)
}
func candidateRevisionRef(id string) string { return candidateRefRoot + id + "/revision" }
func candidateReceiptRef(id, requestID string) string {
	return candidateRefRoot + id + "/receipts/" + requestID
}

func (e *Engine) candidatePath(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", fmt.Errorf("gitengine: candidate id %q: %w", id, err)
	}
	return filepath.Join(e.cfg.CheckoutsDir, "candidates", id), nil
}
func (e *Engine) candidateMetaPath(id string) (string, error) {
	p, err := e.candidatePath(id)
	if err != nil {
		return "", err
	}
	return p + ".json", nil
}
func (e *Engine) candidateVerificationPath(id, verificationID string) (string, error) {
	if err := validateID(id); err != nil {
		return "", fmt.Errorf("gitengine: candidate id %q: %w", id, err)
	}
	if err := validateID(verificationID); err != nil {
		return "", fmt.Errorf("gitengine: verification id %q: %w", verificationID, err)
	}
	return filepath.Join(e.cfg.CheckoutsDir, "verifications", id, verificationID), nil
}

func (e *Engine) loadCandidateMeta(id string) (candidateMeta, error) {
	p, err := e.candidateMetaPath(id)
	if err != nil {
		return candidateMeta{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return candidateMeta{}, fmt.Errorf("%w: %s", ErrCandidateNotFound, id)
		}
		return candidateMeta{}, fmt.Errorf("gitengine: read candidate metadata: %w", err)
	}
	var meta candidateMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return candidateMeta{}, fmt.Errorf("gitengine: decode candidate metadata: %w", err)
	}
	if meta.CandidateID != id || meta.Workspace == "" {
		return candidateMeta{}, fmt.Errorf("%w: malformed metadata", ErrCandidateNotFound)
	}
	return meta, nil
}

func (e *Engine) saveCandidateMeta(id string, meta candidateMeta) error {
	p, err := e.candidateMetaPath(id)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(p), 0o700); mkdirErr != nil {
		return fmt.Errorf("gitengine: create candidate metadata directory: %w", mkdirErr)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("gitengine: encode candidate metadata: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".candidate-*.tmp")
	if err != nil {
		return fmt.Errorf("gitengine: stage candidate metadata: %w", err)
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
		return fmt.Errorf("gitengine: install candidate metadata: %w", renameErr)
	}
	dir, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return err
	}
	return nil
}

func candidateObjectError(name, value string) error {
	return fmt.Errorf("gitengine: %s %q is not a full object id", name, value)
}

func (e *Engine) retainedCandidateRevision(ctx context.Context, repo, id string, index int, input CandidateRevisionInput) error {
	if !validObjectID(input.Revision) {
		return candidateObjectError("candidate revision", input.Revision)
	}
	if !validObjectID(input.Base) {
		return candidateObjectError("candidate base", input.Base)
	}
	ref := candidateInputRef(id, index)
	got, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || got != input.Revision {
		return fmt.Errorf("gitengine: retained input %d is not revision %s", index, input.Revision)
	}
	parent, err := e.git(ctx, repo, "rev-parse", "--verify", input.Revision+"^1")
	if err != nil || parent != input.Base {
		return fmt.Errorf("gitengine: retained input %d base does not match authoritative evidence", index)
	}
	return nil
}

// RetainCandidateInput verifies the private evidence ref and atomically pins
// its exact revision under the candidate-owned input ref.
func (e *Engine) RetainCandidateInput(ctx context.Context, ws domain.WorkspaceID, candidateID string, index int, packetID, revision, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(candidateID); err != nil {
		return err
	}
	if err := validateID(packetID); err != nil {
		return fmt.Errorf("gitengine: packet id: %w", err)
	}
	if index < 0 || index >= candidateMaxInputs {
		return fmt.Errorf("gitengine: candidate input index out of bounds")
	}
	if !validObjectID(revision) {
		return candidateObjectError("evidence revision", revision)
	}
	if !validObjectID(base) {
		return candidateObjectError("evidence base", base)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	evidence := evidenceRef(packetID)
	inputRef := candidateInputRef(candidateID, index)
	if existing, eerr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", inputRef); eerr == nil {
		if existing != revision {
			return fmt.Errorf("gitengine: candidate input %d already pins a different revision", index)
		}
		parent, pErr := e.git(ctx, repo, "rev-parse", "--verify", revision+"^1")
		if pErr != nil || parent != base {
			return fmt.Errorf("gitengine: candidate input %d base does not match retained evidence", index)
		}
		return nil
	}
	got, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", evidence)
	if err != nil || got != revision {
		return fmt.Errorf("gitengine: evidence ref %s is not the requested revision", packetID)
	}
	parent, err := e.git(ctx, repo, "rev-parse", "--verify", revision+"^1")
	if err != nil || parent != base {
		return fmt.Errorf("gitengine: evidence revision base mismatch")
	}
	transaction := "start\nverify " + evidence + " " + revision + "\ncreate " + inputRef + " " + revision + "\nprepare\ncommit\n"
	if _, err := e.gitInput(ctx, repo, gitEnv(), []byte(transaction), "update-ref", "--stdin"); err != nil {
		if existing, eerr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", inputRef); eerr == nil && existing == revision {
			return nil
		}
		return fmt.Errorf("gitengine: retain candidate input: %w", err)
	}
	return nil
}

// CandidateCheckout creates the server-owned assembly checkout at an exact
// target commit. Its .git database is never returned as a verification tree.
func (e *Engine) CandidateCheckout(ctx context.Context, ws domain.WorkspaceID, id, targetRevision string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateID(id); err != nil {
		return "", err
	}
	if !validObjectID(targetRevision) {
		return "", candidateObjectError("target revision", targetRevision)
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return "", err
	}
	resolved, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", targetRevision+"^{commit}")
	if err != nil || resolved != targetRevision {
		return "", fmt.Errorf("gitengine: target revision is unavailable")
	}
	p, err := e.candidatePath(id)
	if err != nil {
		return "", err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	if meta, mErr := e.loadCandidateMeta(id); mErr == nil {
		if meta.Workspace != ws {
			return "", fmt.Errorf("gitengine: candidate workspace mismatch")
		}
		if meta.TargetRevision != targetRevision {
			return "", fmt.Errorf("gitengine: candidate target revision is immutable")
		}
		return p, nil
	} else if !errors.Is(mErr, ErrCandidateNotFound) {
		return "", mErr
	}
	if _, statErr := os.Lstat(p); statErr == nil {
		return "", fmt.Errorf("gitengine: candidate checkout exists without metadata")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if _, err := e.git(ctx, "", "clone", "--local", "--no-hardlinks", "--no-checkout", repo, p); err != nil {
		return "", fmt.Errorf("gitengine: create candidate checkout: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(p)
		if mp, _ := e.candidateMetaPath(id); mp != "" {
			_ = os.Remove(mp)
		}
		_, _ = e.git(ctx, repo, "update-ref", "-d", candidateRevisionRef(id), targetRevision)
	}
	if _, err := e.git(ctx, p, "checkout", "--detach", targetRevision); err != nil {
		cleanup()
		return "", err
	}
	if _, err := e.git(ctx, repo, "update-ref", candidateRevisionRef(id), targetRevision, strings.Repeat("0", len(targetRevision))); err != nil {
		cleanup()
		return "", fmt.Errorf("gitengine: retain candidate revision: %w", err)
	}
	meta := candidateMeta{Workspace: ws, CandidateID: id, TargetRevision: targetRevision, Revision: targetRevision, CreatedAt: time.Now().UTC()}
	if err := e.saveCandidateMeta(id, meta); err != nil {
		cleanup()
		return "", err
	}
	return p, nil
}

func candidateSourceMessage(ctx context.Context, e *Engine, repo, revision string) string {
	msg, err := e.git(ctx, repo, "show", "-s", "--format=%B", revision)
	if err != nil || strings.TrimSpace(msg) == "" {
		return "Apply candidate evidence"
	}
	return msg
}

func (e *Engine) candidateCommit(ctx context.Context, checkout, repo, parent, revision string) (string, error) {
	tree, err := e.git(ctx, checkout, "write-tree")
	if err != nil {
		return "", err
	}
	// ResolveCandidate commits the staged tree directly; discard any failed
	// cherry-pick sequencer state before moving HEAD to that tree.
	_, _ = e.git(ctx, checkout, "cherry-pick", "--quit")
	msg := candidateSourceMessage(ctx, e, repo, revision)
	env := append(gitEnv(), "GIT_AUTHOR_NAME=Aether", "GIT_AUTHOR_EMAIL=aether@localhost", "GIT_COMMITTER_NAME=Aether", "GIT_COMMITTER_EMAIL=aether@localhost")
	commit, err := e.gitInput(ctx, checkout, env, []byte(msg), "commit-tree", tree, "-p", parent)
	if err != nil {
		return "", fmt.Errorf("gitengine: write candidate revision: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if !validObjectID(commit) {
		return "", errors.New("gitengine: candidate commit produced invalid revision")
	}
	stagingRef := candidateRefRoot + filepath.Base(checkout) + "/staging"
	sourceRef := "refs/heads/aether-candidate-staging-" + filepath.Base(checkout)
	if _, err := e.git(ctx, checkout, "update-ref", sourceRef, commit); err != nil {
		return "", err
	}
	if _, err := e.git(ctx, repo, "fetch", "--quiet", "--no-tags", checkout, sourceRef+":"+stagingRef); err != nil {
		return "", fmt.Errorf("gitengine: retain assembled objects: %w", err)
	}
	defer func() {
		_, _ = e.git(ctx, checkout, "update-ref", "-d", sourceRef)
		_, _ = e.git(ctx, repo, "update-ref", "-d", stagingRef)
	}()
	if _, err := e.git(ctx, checkout, "update-ref", "HEAD", commit, parent); err != nil {
		return "", err
	}
	if _, err := e.git(ctx, repo, "update-ref", candidateRevisionRef(filepath.Base(checkout)), commit, parent); err != nil {
		return "", err
	}
	if _, err := e.git(ctx, checkout, "reset", "--hard", commit); err != nil {
		return "", err
	}
	return commit, nil
}

func candidateConflicts(ctx context.Context, e *Engine, checkout string) ([]string, error) {
	out, err := e.gitBytes(ctx, checkout, "diff", "--name-only", "-z", "--diff-filter=U", "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) != 0 {
			paths = append(paths, string(raw))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (e *Engine) reconcileCandidateMeta(ctx context.Context, repo, checkout, id string, meta candidateMeta) (candidateMeta, error) {
	ref, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", candidateRevisionRef(id))
	if err != nil || !validObjectID(ref) {
		return meta, fmt.Errorf("gitengine: candidate revision ref unavailable")
	}
	head, headErr := e.git(ctx, checkout, "rev-parse", "--verify", "HEAD")
	if headErr == nil && head != ref {
		if _, err := e.git(ctx, checkout, "reset", "--hard", ref); err != nil {
			return meta, err
		}
	}
	if headErr == nil && head == ref && len(meta.Conflicts) == 0 && meta.PendingInput >= meta.AppliedInputs && meta.PendingInput < len(meta.Inputs) {
		status, statusErr := e.git(ctx, checkout, "status", "--porcelain=v1", "--untracked-files=no")
		if statusErr != nil {
			return meta, statusErr
		}
		if strings.TrimSpace(status) != "" {
			if _, err := e.git(ctx, checkout, "reset", "--hard", ref); err != nil {
				return meta, err
			}
		}
	}
	if meta.Revision != ref {
		if parent, pErr := e.git(ctx, repo, "rev-parse", "--verify", ref+"^1"); pErr == nil && parent == meta.Revision && meta.PendingInput >= meta.AppliedInputs && meta.PendingInput < len(meta.Inputs) {
			meta.AppliedInputs = meta.PendingInput + 1
			meta.PendingInput = 0
			meta.Conflicts = nil
		}
		meta.Revision = ref
		if err := e.saveCandidateMeta(id, meta); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

// RenderCandidatePatch renders only the frozen candidate's owned target range.
func (e *Engine) RenderCandidatePatch(ctx context.Context, ws domain.WorkspaceID, id, revision string, maxBytes int) (Patch, error) {
	if err := e.CheckCandidate(ctx, ws, id, revision); err != nil {
		return Patch{}, err
	}
	meta, err := e.loadCandidateMeta(id)
	if err != nil {
		return Patch{}, err
	}
	if maxBytes <= 0 || maxBytes > 1<<20 {
		maxBytes = 1 << 20
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return Patch{}, err
	}
	text, truncated, err := e.gitBareBounded(ctx, repo, maxBytes, "diff", "--no-color", "--no-renames", meta.TargetRevision, revision, "--")
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: meta.TargetRevision, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// AssembleCandidate applies retained evidence snapshots in order. A conflict
// leaves the journal and working tree intact so ResolveCandidate can continue.
func (e *Engine) AssembleCandidate(ctx context.Context, ws domain.WorkspaceID, id string, inputs []CandidateRevisionInput) (CandidateAssembly, error) {
	if err := ctx.Err(); err != nil {
		return CandidateAssembly{}, err
	}
	if len(inputs) > candidateMaxInputs {
		return CandidateAssembly{}, errors.New("gitengine: too many candidate inputs")
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return CandidateAssembly{}, err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	meta, err := e.loadCandidateMeta(id)
	if err != nil {
		return CandidateAssembly{}, err
	}
	if meta.Workspace != ws {
		return CandidateAssembly{}, fmt.Errorf("gitengine: candidate workspace mismatch")
	}
	checkout, err := e.candidatePath(id)
	if err != nil {
		return CandidateAssembly{}, err
	}
	if meta, err = e.reconcileCandidateMeta(ctx, repo, checkout, id, meta); err != nil {
		return CandidateAssembly{}, err
	}
	if meta.Frozen {
		if len(meta.Inputs) != 0 && !equalCandidateInputs(meta.Inputs, inputs) {
			return e.assemblyResult(meta), errors.New("gitengine: frozen candidate input order changed")
		}
		return e.assemblyResult(meta), nil
	}
	if meta.PendingInput >= len(inputs) {
		meta.PendingInput = 0
	}
	if meta.AppliedInputs > len(inputs) {
		return CandidateAssembly{}, errors.New("gitengine: candidate input order changed")
	}
	if meta.AppliedInputs == 0 && len(meta.Inputs) == 0 {
		meta.Inputs = append([]CandidateRevisionInput(nil), inputs...)
	}
	if len(meta.Inputs) != 0 && !equalCandidateInputs(meta.Inputs, inputs) {
		return CandidateAssembly{}, errors.New("gitengine: candidate input order changed")
	}
	if len(meta.Conflicts) != 0 {
		return e.assemblyResult(meta), ErrCandidateConflict
	}
	if err := e.saveCandidateMeta(id, meta); err != nil {
		return CandidateAssembly{}, err
	}
	for i := meta.AppliedInputs; i < len(inputs); i++ {
		if err := ctx.Err(); err != nil {
			return e.assemblyResult(meta), err
		}
		input := inputs[i]
		meta.PendingInput, meta.Conflicts = i, nil
		if err := e.saveCandidateMeta(id, meta); err != nil {
			return e.assemblyResult(meta), err
		}
		revision := input.Revision
		if err := e.retainedCandidateRevision(ctx, repo, id, i, input); err != nil {
			return e.assemblyResult(meta), err
		}
		if _, err := e.git(ctx, checkout, "cherry-pick", "--no-commit", "--no-edit", revision); err != nil {
			conflicts, cErr := candidateConflicts(ctx, e, checkout)
			if cErr != nil || len(conflicts) == 0 {
				if cErr != nil {
					return e.assemblyResult(meta), cErr
				}
				return e.assemblyResult(meta), err
			}
			meta.PendingInput, meta.Conflicts = i, conflicts
			if saveErr := e.saveCandidateMeta(id, meta); saveErr != nil {
				return e.assemblyResult(meta), saveErr
			}
			return e.assemblyResult(meta), ErrCandidateConflict
		}
		newRevision, err := e.candidateCommit(ctx, checkout, repo, meta.Revision, revision)
		if err != nil {
			return e.assemblyResult(meta), err
		}
		meta.Revision, meta.AppliedInputs, meta.PendingInput, meta.Conflicts = newRevision, i+1, 0, nil
		if err := e.saveCandidateMeta(id, meta); err != nil {
			return e.assemblyResult(meta), err
		}
	}
	meta.Frozen = true
	meta.Conflicts = nil
	if err := e.saveCandidateMeta(id, meta); err != nil {
		return e.assemblyResult(meta), err
	}
	return e.assemblyResult(meta), nil
}

func (e *Engine) assemblyResult(meta candidateMeta) CandidateAssembly {
	return CandidateAssembly{Revision: meta.Revision, Conflicts: append([]string(nil), meta.Conflicts...), AppliedInputs: meta.AppliedInputs}
}

func equalCandidateInputs(a, b []CandidateRevisionInput) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeCandidateFile(root *os.Root, name string, content []byte) error {
	if err := ValidatePath(name); err != nil || name == "" || strings.HasSuffix(name, "/") {
		return ErrInvalidPath
	}
	parentName := path.Dir(name)
	parent, err := rootfs.OpenRoot(root, parentName)
	if errors.Is(err, os.ErrNotExist) {
		if err = root.MkdirAll(parentName, 0o755); err == nil {
			parent, err = rootfs.OpenRoot(root, parentName)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	defer func() { _ = parent.Close() }()
	leaf := path.Base(name)
	if info, statErr := parent.Lstat(leaf); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink", ErrInvalidPath)
	}
	file, err := parent.OpenFile(leaf, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func deleteCandidateFile(root *os.Root, name string) error {
	if err := ValidatePath(name); err != nil || name == "" || strings.HasSuffix(name, "/") {
		return ErrInvalidPath
	}
	parent, err := rootfs.OpenRoot(root, path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	leaf := path.Base(name)
	if info, err := parent.Lstat(leaf); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink", ErrInvalidPath)
	}
	if err := parent.Remove(leaf); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func ValidateCandidateResolutions(files []CandidateResolution) error {
	if len(files) > candidateMaxResolutions {
		return errors.New("gitengine: too many candidate resolutions")
	}
	for _, file := range files {
		if err := ValidatePath(file.Path); err != nil || file.Path == "" || strings.HasSuffix(file.Path, "/") {
			return fmt.Errorf("%w: %s", ErrInvalidPath, file.Path)
		}
		if !file.Delete {
			for line := range strings.Lines(file.Content) {
				if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, "=======") || strings.HasPrefix(line, ">>>>>>>") {
					return fmt.Errorf("gitengine: unresolved conflict marker in %s", file.Path)
				}
			}
		}
	}
	return nil
}

// ResolveCandidate applies path-safe edits to the current conflict and resumes
// ordered assembly. A candidate freezes only after every retained input lands.
func (e *Engine) ResolveCandidate(ctx context.Context, ws domain.WorkspaceID, id string, files []CandidateResolution) (CandidateAssembly, error) {
	if err := ctx.Err(); err != nil {
		return CandidateAssembly{}, err
	}
	if err := ValidateCandidateResolutions(files); err != nil {
		return CandidateAssembly{}, err
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return CandidateAssembly{}, err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	meta, err := e.loadCandidateMeta(id)
	if err != nil {
		return CandidateAssembly{}, err
	}
	if meta.Workspace != ws {
		return CandidateAssembly{}, fmt.Errorf("gitengine: candidate workspace mismatch")
	}
	checkout, err := e.candidatePath(id)
	if err != nil {
		return CandidateAssembly{}, err
	}
	if meta, err = e.reconcileCandidateMeta(ctx, repo, checkout, id, meta); err != nil {
		return CandidateAssembly{}, err
	}
	if meta.Frozen {
		return e.assemblyResult(meta), ErrCandidateFrozen
	}
	if len(meta.Conflicts) == 0 {
		return e.assemblyResult(meta), errors.New("gitengine: candidate has no recorded conflicts")
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return CandidateAssembly{}, err
	}
	for _, file := range files {
		if file.Delete {
			if deleteErr := deleteCandidateFile(root, file.Path); deleteErr != nil {
				_ = root.Close()
				return e.assemblyResult(meta), deleteErr
			}
			if _, addErr := e.git(ctx, checkout, "add", "-A", "--", file.Path); addErr != nil {
				_ = root.Close()
				return e.assemblyResult(meta), addErr
			}
		} else {
			if writeErr := writeCandidateFile(root, file.Path, []byte(file.Content)); writeErr != nil {
				_ = root.Close()
				return e.assemblyResult(meta), writeErr
			}
			if _, addErr := e.git(ctx, checkout, "add", "--", file.Path); addErr != nil {
				_ = root.Close()
				return e.assemblyResult(meta), addErr
			}
		}
	}
	_ = root.Close()
	stages, err := e.git(ctx, checkout, "ls-files", "-u")
	if err != nil {
		return e.assemblyResult(meta), err
	}
	conflicts, cErr := candidateConflicts(ctx, e, checkout)
	if cErr != nil {
		return e.assemblyResult(meta), cErr
	}
	if strings.TrimSpace(stages) != "" || len(conflicts) != 0 {
		if len(conflicts) == 0 {
			conflicts = append([]string(nil), meta.Conflicts...)
		}
		meta.Conflicts = conflicts
		if metaErr := e.saveCandidateMeta(id, meta); metaErr != nil {
			return e.assemblyResult(meta), metaErr
		}
		return e.assemblyResult(meta), ErrCandidateConflict
	}
	if meta.PendingInput < 0 || meta.PendingInput >= len(meta.Inputs) {
		return e.assemblyResult(meta), errors.New("gitengine: invalid candidate journal")
	}
	pending := meta.Inputs[meta.PendingInput]
	newRevision, err := e.candidateCommit(ctx, checkout, repo, meta.Revision, pending.Revision)
	if err != nil {
		return e.assemblyResult(meta), err
	}
	meta.Revision, meta.AppliedInputs, meta.PendingInput, meta.Conflicts = newRevision, meta.PendingInput+1, 0, nil
	if err := e.saveCandidateMeta(id, meta); err != nil {
		return e.assemblyResult(meta), err
	}
	for i := meta.AppliedInputs; i < len(meta.Inputs); i++ {
		input := meta.Inputs[i]
		meta.PendingInput, meta.Conflicts = i, nil
		if err := e.saveCandidateMeta(id, meta); err != nil {
			return e.assemblyResult(meta), err
		}
		if err := e.retainedCandidateRevision(ctx, repo, id, i, input); err != nil {
			return e.assemblyResult(meta), err
		}
		if _, err := e.git(ctx, checkout, "cherry-pick", "--no-commit", "--no-edit", input.Revision); err != nil {
			conflicts, cErr := candidateConflicts(ctx, e, checkout)
			if cErr != nil || len(conflicts) == 0 {
				if cErr != nil {
					return e.assemblyResult(meta), cErr
				}
				return e.assemblyResult(meta), err
			}
			meta.PendingInput, meta.Conflicts = i, conflicts
			if saveErr := e.saveCandidateMeta(id, meta); saveErr != nil {
				return e.assemblyResult(meta), saveErr
			}
			return e.assemblyResult(meta), ErrCandidateConflict
		}
		newRevision, err := e.candidateCommit(ctx, checkout, repo, meta.Revision, input.Revision)
		if err != nil {
			return e.assemblyResult(meta), err
		}
		meta.Revision, meta.AppliedInputs = newRevision, i+1
		if err := e.saveCandidateMeta(id, meta); err != nil {
			return e.assemblyResult(meta), err
		}
	}
	meta.Frozen = true
	if err := e.saveCandidateMeta(id, meta); err != nil {
		return e.assemblyResult(meta), err
	}
	return e.assemblyResult(meta), nil
}

func (e *Engine) CheckCandidate(ctx context.Context, ws domain.WorkspaceID, id, revision string) error {
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	return e.checkCandidate(ctx, ws, id, revision)
}

// CheckCandidate validates that revision is the immutable candidate revision.
func (e *Engine) checkCandidate(ctx context.Context, ws domain.WorkspaceID, id, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return err
	}
	checkout, err := e.candidatePath(id)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(checkout); statErr != nil {
		return fmt.Errorf("gitengine: candidate checkout: %w", statErr)
	}
	meta, err := e.loadCandidateMeta(id)
	if err != nil {
		return err
	}
	if meta, err = e.reconcileCandidateMeta(ctx, repo, checkout, id, meta); err != nil {
		return err
	}
	if meta.Workspace != ws {
		return fmt.Errorf("gitengine: candidate workspace mismatch")
	}
	got, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", candidateRevisionRef(id))
	if err != nil || got != revision || meta.Revision != revision {
		return fmt.Errorf("gitengine: candidate revision mismatch")
	}
	if len(meta.Inputs) == 0 {
		return fmt.Errorf("gitengine: candidate has no retained inputs")
	}
	refs, refErr := e.git(ctx, repo, "for-each-ref", "--format=%(refname) %(objectname)", candidateRefRoot+id+"/inputs/")
	if refErr != nil {
		return refErr
	}
	seen := make(map[int]bool, len(meta.Inputs))
	for line := range strings.SplitSeq(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("gitengine: malformed candidate input ref")
		}
		ref, object := fields[0], fields[1]
		prefix := candidateRefRoot + id + "/inputs/"
		index, parseErr := strconv.Atoi(strings.TrimPrefix(ref, prefix))
		if parseErr != nil || !strings.HasPrefix(ref, prefix) || index < 0 || index >= len(meta.Inputs) || seen[index] || object != meta.Inputs[index].Revision {
			return fmt.Errorf("gitengine: candidate retained input set is malformed")
		}
		seen[index] = true
	}
	if len(seen) != len(meta.Inputs) {
		return fmt.Errorf("gitengine: candidate retained input set is incomplete")
	}
	if !meta.Frozen {
		return fmt.Errorf("gitengine: candidate is not frozen")
	}
	return nil
}
