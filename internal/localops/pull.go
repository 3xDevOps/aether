package localops

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// PullCommand builds the git fetch that lands a run branch in repo under
// refs/remotes/aether/<branch>, from the run.pull coordinates. The caller
// wires the command's output (the CLI streams it to the terminal) and
// runs it.
func PullCommand(repo, user, addr string, coords protocol.RunPullResult) (string, *exec.Cmd, error) {
	if coords.Branch == "" {
		return "", nil, errors.New("run has no branch")
	}
	url := cli.GitURL(user, addr, coords.WorkspaceID)
	return coords.Branch, fetchCommand(repo, url, coords.Branch), nil
}

// Pull runs the fetch built by PullCommand with its combined output
// captured instead of streamed, for callers answering an API request
// rather than a terminal. It returns the branch, the local tracking ref,
// and everything git printed; on failure the output is folded into the
// error as well.
func Pull(repo, user, addr string, coords protocol.RunPullResult) (branch, ref, output string, err error) {
	if coords.Branch == "" {
		return "", "", "", errors.New("run has no branch")
	}
	url := cli.GitURL(user, addr, coords.WorkspaceID)
	ref, output, err = pullFetch(repo, url, coords.Branch)
	return coords.Branch, ref, output, err
}

// pullFetch is the captured-output fetch core, taking the resolved git
// URL so it stays testable against a filesystem remote.
func pullFetch(repo, url, branch string) (ref, output string, err error) {
	ref = "refs/remotes/aether/" + branch
	out, err := fetchCommand(repo, url, branch).CombinedOutput()
	output = string(out)
	if err != nil {
		return ref, output, fmt.Errorf("git fetch: %w: %s", err, strings.TrimSpace(output))
	}
	return ref, output, nil
}

// fetchCommand is the one fetch both surfaces run: no tags, one refspec
// landing the run branch under the aether remote-tracking namespace.
func fetchCommand(repo, url, branch string) *exec.Cmd {
	refspec := "+refs/heads/" + branch + ":refs/remotes/aether/" + branch
	return exec.Command("git", "-C", repo, "fetch", "--no-tags", url, refspec)
}
