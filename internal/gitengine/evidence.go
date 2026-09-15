package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const (
	evidenceRefRoot = "refs/aether/evidence/"

	// Evidence diff statistics are metadata, not patch content. Keep both the
	// subprocess output and the parsed facts bounded before either can grow a
	// packet-sized allocation.
	evidenceDiffBytesLimit      = 1 << 20
	evidenceDiffItemsLimit      = 512
	evidenceDiffPathBytesLimit  = 4 << 10
	evidenceDiffPathsBytesLimit = 1 << 20
)

var (
	// ErrEvidenceNotFound reports an evidence packet that has no retained ref.
	ErrEvidenceNotFound = errors.New("gitengine: evidence not found")
	// ErrInvalidEvidenceID reports a packet identifier that cannot be used as a
	// server-owned ref component.
	ErrInvalidEvidenceID = errors.New("gitengine: invalid evidence id")
)

// EvidenceRevision is the immutable repository state retained for one packet.
// The commit's first parent is BaseCommit and its tree is Tree. ChangedFiles
// is computed from those two objects, rather than from the live checkout.
// RetainedRefCreated is true only when this call created the workspace ref;
// callers must not remove a ref returned by another attempt.
type EvidenceRevision struct {
	WorkspaceID           domain.WorkspaceID    `json:"workspace_id"`
	BaseCommit            string                `json:"base_commit"`
	Commit                string                `json:"commit"`
	Tree                  string                `json:"tree"`
	ChangedFiles          []events.FileDiffStat `json:"changed_files"`
	ChangedFilesTruncated bool                  `json:"changed_files_truncated,omitempty"`
	RetainedRefCreated    bool                  `json:"retained_ref_created,omitempty"`
}

// CaptureEvidence freezes run's current checkout into packetID's private,
// workspace-scoped evidence ref. The snapshot tree is first written to the
// run's existing sidecar object store, then a commit that points at that tree
// is fetched into the workspace repository. Fetching the commit recursively
// copies every tree and blob, so removing the checkout and sidecar cannot
// remove the evidence.
//
// packetID is the idempotency key: a second capture for an existing packet
// returns the already-retained revision without changing it.
func (e *Engine) CaptureEvidence(ctx context.Context, run domain.RunID, packetID string) (EvidenceRevision, error) {
	if err := ctx.Err(); err != nil {
		return EvidenceRevision{}, err
	}
	if err := validateID(string(run)); err != nil {
		return EvidenceRevision{}, fmt.Errorf("gitengine: run id %q: %w", run, err)
	}
	if err := validateID(packetID); err != nil {
		return EvidenceRevision{}, fmt.Errorf("%w: packet id %q: %v", ErrInvalidEvidenceID, packetID, err)
	}

	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return EvidenceRevision{}, err
	}

	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return EvidenceRevision{}, err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return EvidenceRevision{}, err
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return EvidenceRevision{}, err
	}
	ref := evidenceRef(packetID)
	if commit, found, refErr := e.evidenceRefCommit(ctx, repo, ref); refErr != nil {
		return EvidenceRevision{}, refErr
	} else if found {
		return e.readEvidenceRevision(ctx, repo, meta.Workspace, commit)
	}

	if !validObjectID(meta.Base) {
		return EvidenceRevision{}, fmt.Errorf("gitengine: run %s has invalid base commit", run)
	}
	base, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", meta.Base+"^{commit}")
	if err != nil || base != meta.Base {
		if err == nil {
			err = errors.New("resolved object differs")
		}
		return EvidenceRevision{}, fmt.Errorf("gitengine: evidence base for run %s is unavailable: %w", run, err)
	}
	if boundsErr := e.checkEvidenceCaptureBounds(ctx, run, checkout); boundsErr != nil {
		return EvidenceRevision{}, boundsErr
	}
	tree, err := e.writeSnapshotTree(ctx, run, checkout)
	if err != nil {
		return EvidenceRevision{}, fmt.Errorf("gitengine: capture evidence tree for run %s: %w", run, err)
	}
	if boundsErr := e.checkEvidenceSnapshotBounds(ctx, run, checkout, tree); boundsErr != nil {
		return EvidenceRevision{}, boundsErr
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return EvidenceRevision{}, err
	}
	staging, err := os.MkdirTemp("", "aether-evidence-")
	if err != nil {
		return EvidenceRevision{}, fmt.Errorf("gitengine: stage evidence: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if _, initErr := e.git(ctx, "", "init", "--bare", "--quiet", staging); initErr != nil {
		return EvidenceRevision{}, fmt.Errorf("gitengine: stage evidence: %w", initErr)
	}
	if alternatesErr := writeEvidenceAlternates(staging, store, checkout); alternatesErr != nil {
		return EvidenceRevision{}, alternatesErr
	}
	commit, err := e.commitEvidence(ctx, staging, tree, base, run, packetID)
	if err != nil {
		return EvidenceRevision{}, err
	}
	if _, err := e.git(ctx, staging, "update-ref", ref, commit); err != nil {
		return EvidenceRevision{}, fmt.Errorf("gitengine: stage evidence ref: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return EvidenceRevision{}, err
	}
	if _, err := e.git(ctx, repo, "fetch", "--quiet", "--no-tags", staging, ref+":"+ref); err != nil {
		// A concurrent server process may have won the same idempotency key.
		if existing, found, refErr := e.evidenceRefCommit(ctx, repo, ref); refErr == nil && found {
			return e.readEvidenceRevision(ctx, repo, meta.Workspace, existing)
		}
		return EvidenceRevision{}, fmt.Errorf("gitengine: retain evidence: %w", err)
	}
	revision, readErr := e.readEvidenceRevision(ctx, repo, meta.Workspace, commit)
	revision.RetainedRefCreated = true
	return revision, readErr
}

// RenderEvidence renders the retained revision's patch without opening or
// reconstructing its run checkout. commit must be the object directly named
// by a private evidence ref in workspaceID.
func (e *Engine) RenderEvidence(ctx context.Context, workspaceID domain.WorkspaceID, commit string, maxBytes int) (Patch, error) {
	if err := ctx.Err(); err != nil {
		return Patch{}, err
	}
	if !validObjectID(commit) {
		return Patch{}, fmt.Errorf("%w: %q", ErrInvalidObjectID, commit)
	}
	repo, err := e.existingRepoPath(workspaceID)
	if err != nil {
		return Patch{}, err
	}
	refs, err := e.git(ctx, repo, "for-each-ref", "--format=%(objectname)", evidenceRefRoot+"*")
	if err != nil {
		return Patch{}, err
	}
	retained := false
	for refCommit := range strings.SplitSeq(refs, "\n") {
		if strings.TrimSpace(refCommit) == commit {
			retained = true
			break
		}
	}
	if !retained {
		return Patch{}, fmt.Errorf("%w: %s", ErrEvidenceNotFound, commit)
	}
	revision, err := e.readEvidenceRevision(ctx, repo, workspaceID, commit)
	if err != nil {
		return Patch{}, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPatchBytes
	}
	text, truncated, err := e.gitBareBounded(ctx, repo, maxBytes,
		"diff", "--no-color", "--no-renames", revision.BaseCommit, revision.Commit, "--")
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: revision.BaseCommit, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// RemoveEvidence deletes only packetID's exact private evidence ref. Missing
// refs are intentionally harmless so retention GC can retry safely.
func (e *Engine) RemoveEvidence(ctx context.Context, workspaceID domain.WorkspaceID, packetID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(packetID); err != nil {
		return fmt.Errorf("%w: packet id %q: %v", ErrInvalidEvidenceID, packetID, err)
	}
	repo, err := e.existingRepoPath(workspaceID)
	if err != nil {
		return err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.git(ctx, repo, "update-ref", "-d", evidenceRef(packetID)); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("gitengine: remove evidence: %w", err)
	}
	return nil
}

// PruneEvidence repacks a workspace repository after evidence refs have been
// removed. Git's reachability walk preserves every live branch and object
// reachable from a checkout while reclaiming unreachable retained evidence.
// The maintenance lock serializes this with internal ref/object writers.
func (e *Engine) PruneEvidence(ctx context.Context, workspaceID domain.WorkspaceID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repo, err := e.existingRepoPath(workspaceID)
	if err != nil {
		return err
	}
	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	e.repoMaintenanceMu.Lock()
	defer e.repoMaintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.git(ctx, repo, "gc", "--prune=now", "--quiet"); err != nil {
		return fmt.Errorf("gitengine: prune evidence objects: %w", err)
	}
	return nil
}

func evidenceRef(packetID string) string { return evidenceRefRoot + packetID }

func (e *Engine) evidenceRefCommit(ctx context.Context, repo, ref string) (string, bool, error) {
	if _, err := e.git(ctx, repo, "show-ref", "--verify", "--quiet", ref); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	commit, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", false, fmt.Errorf("gitengine: malformed evidence ref: %w", err)
	}
	if !validObjectID(commit) {
		return "", false, errors.New("gitengine: malformed evidence ref object")
	}
	return commit, true, nil
}

func (e *Engine) readEvidenceRevision(ctx context.Context, repo string, workspace domain.WorkspaceID, commit string) (EvidenceRevision, error) {
	if !validObjectID(commit) {
		return EvidenceRevision{}, fmt.Errorf("%w: %q", ErrInvalidObjectID, commit)
	}
	parents, err := e.git(ctx, repo, "rev-list", "--parents", "-n1", commit)
	if err != nil {
		return EvidenceRevision{}, err
	}
	parts := strings.Fields(parents)
	if len(parts) != 2 || parts[0] != commit || !validObjectID(parts[1]) {
		return EvidenceRevision{}, fmt.Errorf("gitengine: evidence commit %s has no single base parent", commit)
	}
	tree, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil || !validObjectID(tree) {
		if err == nil {
			err = errors.New("invalid tree object")
		}
		return EvidenceRevision{}, fmt.Errorf("gitengine: evidence commit %s has no tree: %w", commit, err)
	}
	files, truncated, err := e.evidenceDiffStatsBounded(ctx, repo, parts[1], commit)
	if err != nil {
		return EvidenceRevision{}, err
	}
	return EvidenceRevision{
		WorkspaceID:           workspace,
		BaseCommit:            parts[1],
		Commit:                commit,
		Tree:                  tree,
		ChangedFiles:          files,
		ChangedFilesTruncated: truncated,
	}, nil
}

func (e *Engine) evidenceDiffStatsBounded(ctx context.Context, repo, base, commit string) ([]events.FileDiffStat, bool, error) {
	numstat, truncated, err := e.gitBareBounded(ctx, repo, evidenceDiffBytesLimit,
		"diff", "--numstat", "--no-renames", "-z", base, commit, "--")
	if err != nil {
		return nil, false, err
	}
	files, parsedTruncated, err := parseEvidenceDiffStats(numstat, truncated)
	return files, truncated || parsedTruncated, err
}

func parseEvidenceDiffStats(numstat string, truncated bool) ([]events.FileDiffStat, bool, error) {
	files := make([]events.FileDiffStat, 0, 8)
	pathBytes := 0
	for start := 0; start < len(numstat); {
		end := strings.IndexByte(numstat[start:], 0)
		if end < 0 {
			if truncated {
				return files, true, nil
			}
			return nil, false, errors.New("gitengine: malformed evidence diff stats")
		}
		record := numstat[start : start+end]
		start += end + 1
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			return nil, false, errors.New("gitengine: malformed evidence diff stats")
		}
		if len(files) >= evidenceDiffItemsLimit {
			return files, true, nil
		}
		if len(parts[2]) > evidenceDiffPathBytesLimit {
			truncated = true
			continue
		}
		if pathBytes > evidenceDiffPathsBytesLimit-len(parts[2]) {
			return files, true, nil
		}
		add, addErr := parseDiffCount(parts[0])
		if addErr != nil {
			return nil, false, fmt.Errorf("gitengine: malformed evidence additions: %w", addErr)
		}
		del, delErr := parseDiffCount(parts[1])
		if delErr != nil {
			return nil, false, fmt.Errorf("gitengine: malformed evidence deletions: %w", delErr)
		}
		pathBytes += len(parts[2])
		files = append(files, events.FileDiffStat{Path: parts[2], Additions: add, Deletions: del})
	}
	slices.SortFunc(files, func(a, b events.FileDiffStat) int { return strings.Compare(a.Path, b.Path) })
	return files, truncated, nil
}

func parseDiffCount(value string) (int, error) {
	if value == "-" {
		return 0, nil
	}
	if value == "" {
		return 0, errors.New("invalid diff count")
	}
	var count int
	maxInt := int(^uint(0) >> 1)
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid diff count")
		}
		digit := int(r - '0')
		if count > (maxInt-digit)/10 {
			return 0, errors.New("diff count overflows int")
		}
		count = count*10 + digit
	}
	return count, nil
}

func writeEvidenceAlternates(repo, store, checkout string) error {
	objects := filepath.Join(repo, "objects")
	if err := os.MkdirAll(filepath.Join(objects, "info"), 0o700); err != nil {
		return fmt.Errorf("gitengine: stage evidence alternates: %w", err)
	}
	alternates := []byte(filepath.Join(store, "objects") + "\n" + filepath.Join(checkout, ".git", "objects") + "\n")
	if err := os.WriteFile(filepath.Join(objects, "info", "alternates"), alternates, 0o600); err != nil {
		return fmt.Errorf("gitengine: stage evidence alternates: %w", err)
	}
	return nil
}

func (e *Engine) commitEvidence(ctx context.Context, repo, tree, base string, run domain.RunID, packetID string) (string, error) {
	env := append(gitEnv(),
		"GIT_AUTHOR_NAME=Aether",
		"GIT_AUTHOR_EMAIL=aether@localhost",
		"GIT_COMMITTER_NAME=Aether",
		"GIT_COMMITTER_EMAIL=aether@localhost",
	)
	commit, err := e.gitIn(ctx, repo, env, "commit-tree", tree, "-p", base,
		"-m", fmt.Sprintf("aether evidence %s for run %s", packetID, run))
	if err != nil {
		return "", fmt.Errorf("gitengine: create evidence commit: %w", err)
	}
	if !validObjectID(commit) {
		return "", fmt.Errorf("gitengine: create evidence commit returned %q", commit)
	}
	return commit, nil
}

func (e *Engine) gitBareBounded(ctx context.Context, repo string, limit int, args ...string) (string, bool, error) {
	argv := append([]string{"-C", repo, "-c", "safe.directory=*", "-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	out := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out.buf), out.over, nil
}
