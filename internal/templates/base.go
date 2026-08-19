package templates

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RepoBase reads base branch ages out of the workspace bare repos the git
// engine keeps under <data>/repos. That is the whole truth available to
// the server: it never fetches from upstream on its own, so the tip it
// sees is the last one a member pushed.
type RepoBase struct {
	// Dir is the repos directory, <data>/repos.
	Dir string
	// Git is the git binary; empty means "git" on PATH.
	Git string
}

// BaseCommitTime reports when ws's branch tip was committed. A branch the
// server has never seen is an error, not a zero time.
func (r RepoBase) BaseCommitTime(ctx context.Context, ws domain.WorkspaceID, branch string) (time.Time, error) {
	if strings.ContainsAny(string(ws), `/\`) || strings.Contains(string(ws), "..") {
		return time.Time{}, fmt.Errorf("templates: invalid workspace id %q", ws)
	}
	git := r.Git
	if git == "" {
		git = "git"
	}
	repo := filepath.Join(r.Dir, string(ws)+".git")
	out, err := exec.CommandContext(ctx, git, "-C", repo,
		"log", "-1", "--format=%ct", "refs/heads/"+branch, "--").Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("templates: read %s tip of %s: %w", branch, ws, err)
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("templates: parse commit time of %s: %w", branch, err)
	}
	return time.Unix(secs, 0).UTC(), nil
}
