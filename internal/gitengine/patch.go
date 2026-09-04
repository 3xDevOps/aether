package gitengine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// DefaultPatchBytes caps rendered patch text when the caller names no
// limit of its own.
const DefaultPatchBytes = 1 << 20

// Patch is one rendered diff of a run checkout, as unified patch text.
type Patch struct {
	// Base is what the diff is taken against: the recorded fork point for a
	// cumulative render, the from tree for an interval render.
	Base string
	// Text is the unified diff, empty when nothing changed against Base.
	Text string
	// Truncated reports that the diff outgrew the byte limit and Text ends
	// early, at the last whole line that fit.
	Truncated bool
}

// PatchRequest names one rendering. From and To are empty for the run's
// current diff against its fork point; set to snapshot trees recorded by
// run.diff events they render what one interval changed. MaxBytes caps the
// patch text (DefaultPatchBytes when not positive).
type PatchRequest struct {
	From     string
	To       string
	MaxBytes int
}

// RunPatch renders a run checkout's diff.
//
// With an empty range it covers committed work, uncommitted edits, and
// untracked files alike against the recorded fork point - the same set of
// changes the run.diff snapshots count. With both ends of the range set it
// renders one snapshot tree against another, which is what a single diff
// interval changed; one end alone is ErrInvalidObjectID because a range
// needs both.
//
// It is read-only from the agent's point of view: the worktree is staged
// into a scratch index, and the blobs staging hashes go to a scratch object
// directory beside it, with the checkout's own objects readable as an
// alternate. Nothing under the checkout's .git is written.
func (e *Engine) RunPatch(ctx context.Context, run domain.RunID, req PatchRequest) (Patch, error) {
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return Patch{}, err
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultPatchBytes
	}
	// Staging re-hashes every untracked file, which the seeded stat cache
	// cannot cover, so a worktree holding a large un-ignored tree makes this
	// arbitrarily slow. Bounded like a diff snapshot's git work, rather than
	// by however long the browser is willing to wait.
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	if req.From != "" || req.To != "" {
		return e.rangePatch(ctx, run, checkout, req.From, req.To, maxBytes)
	}

	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	dir, err := os.MkdirTemp("", "aether-patch-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for run %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index, err := scratchIndex(dir, checkout, run)
	if err != nil {
		return Patch{}, err
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, maxBytes,
		"diff", "--cached", "--no-color", "--no-renames", meta.Base)
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: meta.Base, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// rangePatch renders one snapshot tree against another out of the run's own
// snapshot store. Validating both ids and resolving them only inside that
// store is what keeps a client-supplied id from reaching anything but this
// run's objects.
func (e *Engine) rangePatch(ctx context.Context, run domain.RunID, checkout, from, to string, maxBytes int) (Patch, error) {
	if from == "" || to == "" {
		return Patch{}, fmt.Errorf("%w: a snapshot range needs both ends", ErrInvalidObjectID)
	}
	for _, id := range []string{from, to} {
		if !validObjectID(id) {
			return Patch{}, fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return Patch{}, err
	}
	index, err := scratchIndex(store, checkout, run)
	if err != nil {
		return Patch{}, err
	}
	for _, id := range []string{from, to} {
		if resolveErr := e.snapshotTreeExists(ctx, checkout, index, id); resolveErr != nil {
			return Patch{}, resolveErr
		}
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, maxBytes,
		"diff", "--no-color", "--no-renames", from, to)
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: from, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// trimToLastLine cuts truncated patch text back to its last whole line, so
// a reader never sees half a hunk header.
func trimToLastLine(text string, truncated bool) string {
	if !truncated {
		return text
	}
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		return text[:i+1]
	}
	return text
}

// gitStaged runs a git command in a run checkout against a scratch index
// file, capturing at most limit bytes of stdout. It exists beside Engine.git
// because that one sanitizes the environment away entirely and trims its
// output: patch text needs the scratch index and object redirection, its
// own byte ceiling, and its bytes verbatim.
func (e *Engine) gitStaged(ctx context.Context, dir, index string, limit int, args ...string) (string, bool, error) {
	// quotePath off keeps non-ASCII paths raw instead of octal-escaped, so a
	// patch names its files exactly as the -z listings behind run.diff do -
	// the client matches the two by path.
	argv := append([]string{"-C", dir, "-c", "safe.directory=*", "-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = append(gitEnv(),
		"GIT_INDEX_FILE="+index,
		// New objects land beside the scratch index and vanish with it; the
		// checkout's own database stays readable through the scratch
		// directory's alternates file but is never written to.
		"GIT_OBJECT_DIRECTORY="+filepath.Join(filepath.Dir(index), "objects"),
	)
	out := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("gitengine: git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out.buf), out.over, nil
}

// boundedBuffer keeps the first limit bytes written to it and reports
// whether anything was dropped. It never errors, so the command it drains
// runs to completion instead of dying on a broken pipe mid-diff.
type boundedBuffer struct {
	buf   []byte
	limit int
	over  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	switch room := b.limit - len(b.buf); {
	case room >= len(p):
		b.buf = append(b.buf, p...)
	case room > 0:
		b.buf = append(b.buf, p[:room]...)
		b.over = true
	case len(p) > 0:
		b.over = true
	}
	return len(p), nil
}
