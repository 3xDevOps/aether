package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
)

// vmCommit is commitAll for a checkout in a VM. The VM stages the checkout
// as commitAll stages it; the server builds and signs the commit from the
// staged tree, so the signing key never enters the VM, and the VM installs
// it as the checkout's HEAD with the staged index.
func (e *Engine) vmCommit(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (string, error) {
	meta, err := e.readRunMeta(run)
	if err != nil {
		return "", err
	}
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return "", err
	}
	tree, head, err := g.Stage(ctx, string(run), false)
	if err != nil {
		return "", vmErr(err)
	}
	store, err := e.vmStore(ctx, run, meta)
	if err != nil {
		return "", err
	}
	if err = e.fetchStagedTree(ctx, run, meta, store, tree, head, MaxSnapshotInputBytes, ErrSnapshotStorageLimit); err != nil {
		return "", err
	}
	if e.objectType(ctx, store, head) != "commit" {
		return "", fmt.Errorf("%w: session %s reported %s as its HEAD commit", ErrInvalidObjectID, run, head)
	}
	headTree, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", head+"^{tree}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(headTree) == tree {
		return "", nil
	}
	commit, subject, err := e.commitTree(ctx, store, tree, head, message, author, signingKey)
	if err != nil {
		return "", err
	}
	var pack bytes.Buffer
	if err = e.packObjects(ctx, store, []string{commit}, []string{head}, &pack); err != nil {
		return "", err
	}
	err = g.Install(ctx, string(run), []guest.RefUpdate{{Ref: "HEAD", New: commit, Old: head}}, "commit: "+subject, tree, &pack)
	if err != nil {
		return "", vmErr(err)
	}
	return commit, nil
}

// fetchStagedTree copies a tree the VM staged, and the head it staged it
// on, into the run's store and checks the tree against limit.
func (e *Engine) fetchStagedTree(ctx context.Context, run domain.RunID, meta runMeta, store, tree, head string, limit uint64, limitErr error) error {
	wants := []string{tree}
	if head != "" {
		wants = append(wants, head)
	}
	if err := e.fetchVMObjects(ctx, run, store, wants, []string{meta.Base, e.lastSnapshotTree(run)}); err != nil {
		return err
	}
	if kind := e.objectType(ctx, store, tree); kind != "tree" {
		return fmt.Errorf("%w: session %s staged a %s, not a tree", ErrInvalidObjectID, run, kind)
	}
	return e.checkTreeBounds(ctx, store, tree, limit, limitErr)
}

// commitTree builds the commit commitAll would: the message cleaned up as
// git commit -m does, authored by author, committed by Aether, and signed
// with signingKey when one is given.
func (e *Engine) commitTree(ctx context.Context, store, tree, parent, message string, author domain.GitIdentity, signingKey []byte) (string, string, error) {
	cleaned, err := e.gitInput(ctx, store, gitEnv(), []byte(message), "stripspace")
	if err != nil {
		return "", "", err
	}
	subject, _, _ := strings.Cut(cleaned, "\n")
	name, email := "Aether", "aether@localhost"
	if author.Name != "" && author.Email != "" {
		name, email = author.Name, author.Email
	}
	env := append(gitEnv(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME=Aether", "GIT_COMMITTER_EMAIL=aether@localhost",
	)
	args := []string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.program=ssh-keygen"}
	if len(signingKey) > 0 {
		keyPath, cleanup, keyErr := writeTempSigningKey(signingKey)
		if keyErr != nil {
			return "", "", keyErr
		}
		defer cleanup()
		args = append(args, "-c", "user.signingkey="+keyPath, "commit-tree", "-S")
	} else {
		args = append(args, "commit-tree")
	}
	args = append(args, tree, "-p", parent, "-F", "-")
	commit, err := e.gitInput(ctx, store, env, []byte(cleaned+"\n"), args...)
	if err != nil {
		return "", "", fmt.Errorf("gitengine: build the session commit: %w", err)
	}
	if !validObjectID(commit) {
		return "", "", fmt.Errorf("gitengine: commit-tree returned %q", commit)
	}
	return commit, subject, nil
}

// packObjects writes a pack of what wants reach in repo and haves do not.
func (e *Engine) packObjects(ctx context.Context, repo string, wants, haves []string, w io.Writer) error {
	var input strings.Builder
	for _, id := range wants {
		input.WriteString(id + "\n")
	}
	for _, id := range haves {
		input.WriteString("^" + id + "\n")
	}
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, "-C", repo, "-c", "safe.directory=*", "pack-objects", "--revs", "--stdout", "-q")
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gitengine: pack objects: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// vmFetchBranch is FetchRunBranch into a checkout in a VM: the server packs
// the branch's new objects and the VM indexes them and sets ref.
func (e *Engine) vmFetchBranch(ctx context.Context, repo, branch string, into domain.RunID, dst runMeta, ref string) (string, error) {
	commit, err := e.git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("gitengine: fetch %s into session %s: %w", branch, into, err)
	}
	g, err := e.vmGit(ctx, into)
	if err != nil {
		return "", err
	}
	haves := []string{dst.Base}
	if tip, tipErr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+dst.Branch); tipErr == nil {
		haves = append(haves, tip)
	}
	install := func(haves []string) error {
		var pack bytes.Buffer
		if packErr := e.packObjects(ctx, repo, []string{commit}, haves, &pack); packErr != nil {
			return packErr
		}
		return g.Install(ctx, string(into), []guest.RefUpdate{{Ref: ref, New: commit}}, "fetch: "+branch, "", &pack)
	}
	// The VM may have dropped the commits of a branch it rewrote since it
	// published them; then only the fork point is known to be there.
	if err = install(haves); err != nil && len(haves) > 1 {
		err = install(haves[:1])
	}
	if err != nil {
		return "", fmt.Errorf("gitengine: fetch %s into session %s: %w", branch, into, vmErr(err))
	}
	return commit, nil
}

// vmEvidenceTree is the tree evidence of a checkout in a VM retains: staged
// now while the VM runs, or its last recorded snapshot when it does not,
// which the checkout cannot have moved from since. It holds the run's
// snapshot lock.
func (e *Engine) vmEvidenceTree(ctx context.Context, run domain.RunID, meta runMeta, store string) (string, error) {
	g, err := e.vmGit(ctx, run)
	if errors.Is(err, ErrCheckoutNotFound) {
		out, _, refErr := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "--quiet", snapshotPublishedRef)
		if refErr != nil {
			return "", fmt.Errorf("%w: session %s recorded no snapshot", ErrCheckoutNotFound, run)
		}
		tree := strings.TrimSpace(out)
		return tree, e.checkTreeBounds(ctx, store, tree, MaxEvidenceInputBytes, ErrEvidenceStorageLimit)
	}
	if err != nil {
		return "", err
	}
	tree, _, err := g.Stage(ctx, string(run), true)
	if err != nil {
		return "", vmErr(err)
	}
	if err = e.fetchVMObjects(ctx, run, store, []string{tree}, []string{meta.Base}); err != nil {
		return "", err
	}
	if kind := e.objectType(ctx, store, tree); kind != "tree" {
		return "", fmt.Errorf("%w: session %s staged a %s, not a tree", ErrInvalidObjectID, run, kind)
	}
	return tree, e.checkTreeBounds(ctx, store, tree, MaxEvidenceInputBytes, ErrEvidenceStorageLimit)
}
