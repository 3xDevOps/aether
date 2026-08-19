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

// Patch is a run checkout's diff against the fork point recorded at
// creation, as unified patch text.
type Patch struct {
	// Base is the fork-point commit the diff is taken against.
	Base string
	// Text is the unified diff, empty when the checkout matches Base.
	Text string
	// Truncated reports that the diff outgrew the byte limit and Text ends
	// early, at the last whole line that fit.
	Truncated bool
}

// RunPatch renders the run checkout's diff against its recorded fork point,
// covering committed work, uncommitted edits, and untracked files alike -
// the same set of changes the run.diff snapshots count. Text is capped at
// maxBytes (DefaultPatchBytes when not positive).
//
// It is read-only from the agent's point of view: the worktree is staged
// into a scratch index, and the blobs staging hashes go to a scratch object
// directory beside it, with the checkout's own objects readable as an
// alternate. Nothing under the checkout's .git is written - crucial because
// run provisioning chowned it to the run's user, and objects or fan-out
// directories created here as the server user would leave the agent unable
// to commit.
func (e *Engine) RunPatch(ctx context.Context, run domain.RunID, maxBytes int) (Patch, error) {
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return Patch{}, err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPatchBytes
	}
	// Staging re-hashes every untracked file, which the seeded stat cache
	// cannot cover, so a worktree holding a large un-ignored tree makes this
	// arbitrarily slow. Bounded like a diff snapshot's git work, rather than
	// by however long the browser is willing to wait.
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "aether-patch-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for run %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index := filepath.Join(dir, "index")
	// Repo discovery checks that the overriding object directory exists.
	objects := filepath.Join(dir, "objects")
	if mkErr := os.MkdirAll(filepath.Join(objects, "info"), 0o700); mkErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch objects for run %s: %w", run, mkErr)
	}
	// The checkout's database is linked in through the scratch directory's
	// alternates file, not GIT_ALTERNATE_OBJECT_DIRECTORIES: that env var is
	// colon-split with no quoting, so a data dir containing ':' would break
	// it apart.
	alternates := filepath.Join(objects, "info", "alternates")
	if wErr := os.WriteFile(alternates, []byte(filepath.Join(checkout, ".git", "objects")+"\n"), 0o600); wErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch alternates for run %s: %w", run, wErr)
	}

	// Seeding the scratch index from the checkout's own carries git's stat
	// cache over, so staging costs a scan of the worktree rather than a
	// rehash of every tracked file. A checkout without one still works.
	if data, readErr := os.ReadFile(filepath.Join(checkout, ".git", "index")); readErr == nil {
		if writeErr := os.WriteFile(index, data, 0o600); writeErr != nil {
			return Patch{}, fmt.Errorf("gitengine: seed scratch index for run %s: %w", run, writeErr)
		}
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, maxBytes,
		"diff", "--cached", "--no-color", "--no-renames", meta.Base)
	if err != nil {
		return Patch{}, err
	}
	if truncated {
		if i := strings.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i+1]
		}
	}
	return Patch{Base: meta.Base, Text: text, Truncated: truncated}, nil
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
