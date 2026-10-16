package gitengine

import (
	"context"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

const snapshotUpstreamRef = "refs/aether/upstream"

// effectiveBase is the commit a run's current diff is measured against: the
// newest upstream commit the run's HEAD contains, found as the merge base of
// HEAD and the checkout's origin/<baseBranch>. A run that merged or rebased
// onto newer upstream work is then charged only for its own changes. The
// result never precedes forkPoint: whenever the merge base is not a
// descendant of it, or any input is missing or invalid, forkPoint is used.
//
// The tracking ref and HEAD are read as inert data and validated in the
// server-owned scratch store; Git never reads the checkout's own repository.
func (e *Engine) effectiveBase(ctx context.Context, run domain.RunID, checkout, forkPoint, baseBranch string) string {
	tip := e.upstreamTip(checkout, baseBranch)
	if tip == "" {
		return forkPoint
	}
	head, err := e.checkoutHead(ctx, run, checkout)
	if err != nil {
		return forkPoint
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return forkPoint
	}
	base, err := e.git(ctx, store, "merge-base", head, tip)
	if err != nil || !validObjectID(base) {
		return forkPoint
	}
	if _, ancestorErr := e.git(ctx, store, "merge-base", "--is-ancestor", forkPoint, base); ancestorErr != nil {
		return forkPoint
	}
	return base
}

// upstreamTip is the commit the checkout's origin/<baseBranch> names, read as
// inert data; empty when it is missing or invalid.
func (e *Engine) upstreamTip(checkout, baseBranch string) string {
	ref := "refs/remotes/origin/" + baseBranch
	if baseBranch == "" || !validCheckoutRef(ref) {
		return ""
	}
	tip, err := e.resolveCheckoutRef(checkout, ref)
	if err != nil || !validObjectID(tip) {
		return ""
	}
	return tip
}

// publishedBaseTree is the tree of the base the published snapshot's stats
// were measured against: refs/aether/upstream, or the fork-point tree for a
// store written before it was recorded. Empty when nothing is published.
func (e *Engine) publishedBaseTree(ctx context.Context, run domain.RunID) string {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return ""
	}
	for _, ref := range []string{snapshotUpstreamRef, "refs/aether/base"} {
		if tree, _, refErr := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "--quiet", ref); refErr == nil {
			return strings.TrimSpace(tree)
		}
	}
	return ""
}
