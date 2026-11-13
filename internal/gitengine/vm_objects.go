package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
)

// maxVMPackBytes bounds one pack of a snapshot, whose input is bounded by
// MaxSnapshotInputBytes; maxVMHistoryBytes one pack of new commits, which a
// checkout on the server's disk publishes whatever their size.
const (
	maxVMPackBytes    = MaxSnapshotInputBytes
	maxVMHistoryBytes = 4 << 30
)

// vmStore returns the snapshot store of a checkout in a VM, creating it on
// first use. Its objects borrow the workspace repository's, which hold the
// fork point; everything else arrives from the VM.
func (e *Engine) vmStore(ctx context.Context, run domain.RunID, meta runMeta) (string, error) {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", err
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(filepath.Join(store, "HEAD")); statErr == nil {
		return store, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	format, err := e.git(ctx, repo, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	// The store counts as made once HEAD exists, so it is built aside and
	// moved in whole.
	tmp, err := os.MkdirTemp(filepath.Dir(store), filepath.Base(store)+".tmp-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err = e.git(ctx, "", "init", "--bare", "--quiet", "--object-format="+format, tmp); err != nil {
		return "", fmt.Errorf("gitengine: initialize snapshot store of session %s: %w", run, err)
	}
	if _, err = borrowObjects(tmp, filepath.Join(repo, "objects"), run); err != nil {
		return "", err
	}
	tree, _, err := e.gitBareBounded(ctx, tmp, 256, "rev-parse", "--verify", meta.Base+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("gitengine: fork point of session %s: %w", run, err)
	}
	if _, _, err = e.gitBareBounded(ctx, tmp, 0, "update-ref", "refs/aether/base", strings.TrimSpace(tree)); err != nil {
		return "", err
	}
	// Another caller that made the store first wins.
	if err = os.Rename(tmp, store); err != nil && !errors.Is(err, fs.ErrExist) && !errors.Is(err, syscall.ENOTEMPTY) {
		return "", fmt.Errorf("gitengine: initialize snapshot store of session %s: %w", run, err)
	}
	return store, nil
}

// fetchVMObjects copies from the VM every object reachable from wants that
// store lacks, in a pack of at most limit bytes and of no more than leaves
// the server's disk 128 MiB free. index-pack --strict checks every object it receives and
// that every object one names is in the pack or the store, so the store
// only ever holds whole histories, whatever the VM sends.
func (e *Engine) fetchVMObjects(ctx context.Context, run domain.RunID, store string, wants, haves []string, limit int64) error {
	var missing []string
	for _, id := range wants {
		if !validObjectID(id) {
			return fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
		if !e.hasObject(ctx, store, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// A pack never takes the disk's last maxVMPackBytes.
	free, err := disk.Free(store)
	if err != nil || free <= 2*maxVMPackBytes {
		return fmt.Errorf("%w: the server has too little free disk for objects from session %s", ErrSnapshotStorageLimit, run)
	}
	limit = min(limit, int64(free-maxVMPackBytes))
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return err
	}
	var present []string
	for _, id := range haves {
		if validObjectID(id) && e.hasObject(ctx, store, id) {
			present = append(present, id)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, "-C", store, "-c", "safe.directory=*",
		"index-pack", "--strict", "--stdin", "--max-input-size="+strconv.FormatInt(limit, 10))
	cmd.Env = gitEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("gitengine: receive objects of session %s: %w", run, err)
	}
	sendErr := g.Objects(ctx, string(run), missing, present, stdin, limit)
	_ = stdin.Close()
	if sendErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if sendErr != nil {
		return fmt.Errorf("gitengine: receive objects of session %s: %w", run, sendErr)
	}
	if waitErr != nil {
		return fmt.Errorf("gitengine: refuse objects of session %s: %w: %s", run, waitErr, strings.TrimSpace(stderr.String()))
	}
	for _, id := range missing {
		if !e.hasObject(ctx, store, id) {
			return fmt.Errorf("gitengine: session %s sent a pack without %s", run, id)
		}
	}
	return nil
}

func (e *Engine) hasObject(ctx context.Context, store, id string) bool {
	_, _, err := e.gitBareBounded(ctx, store, 0, "cat-file", "-e", id+"^{object}")
	return err == nil
}

// objectType is the type of an object store holds, "" when it holds none.
func (e *Engine) objectType(ctx context.Context, store, id string) string {
	out, _, err := e.gitBareBounded(ctx, store, 64, "cat-file", "-t", id)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// PackObjects writes to w a pack of every object reachable from wants and
// from none of the haves the run's snapshot store can read. It is the git
// service's side of fetchVMObjects.
func (e *Engine) PackObjects(ctx context.Context, run domain.RunID, wants, haves []string, w io.Writer) error {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(store, "HEAD")); err != nil {
		return fmt.Errorf("%w: session %s has no snapshot store", ErrCheckoutNotFound, run)
	}
	var input strings.Builder
	for _, id := range wants {
		if !validObjectID(id) {
			return fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
		input.WriteString(id + "\n")
	}
	for _, id := range haves {
		if validObjectID(id) && e.hasObject(ctx, store, id) {
			input.WriteString("^" + id + "\n")
		}
	}
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, "-C", store, "-c", "safe.directory=*", "pack-objects", "--revs", "--stdout", "-q")
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gitengine: pack objects of session %s: %w: %s", run, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// vmOf names the VM a checkout lives in: its run's, for an extra
// repository's checkout too.
func vmOf(key domain.RunID) domain.RunID {
	run, _ := domain.ParentRun(key)
	return run
}

// vmGit returns the git service of the VM the checkout of run lives in. A
// server that runs no VMs treats every one as stopped, so what it shows of
// the session comes from the recorded history.
func (e *Engine) vmGit(ctx context.Context, run domain.RunID) (*guest.Git, error) {
	if e.cfg.VM == nil {
		return nil, fmt.Errorf("gitengine: session %s has its checkout in a VM, and this server runs no VMs: %w", run, guest.ErrVMNotRunning)
	}
	g, err := e.cfg.VM.Guest(ctx, vmOf(run))
	if err != nil {
		return nil, fmt.Errorf("gitengine: session %s: %w", run, err)
	}
	return g, nil
}
