package runrepo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type Change = protocol.RunGitChange

type Status struct {
	Branch   string   `json:"branch"`
	Head     string   `json:"head"`
	Detached bool     `json:"detached"`
	Unborn   bool     `json:"unborn"`
	Changes  []Change `json:"changes"`
}

func (s *Service) Status(ctx context.Context, run Execution) (Status, error) {
	out, err := s.git(ctx, run, false, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	text, err := complete(out, err)
	if err != nil {
		return Status{}, err
	}
	state := Status{Changes: []Change{}}
	records := strings.Split(text, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}
		switch {
		case strings.HasPrefix(record, "# branch.oid "):
			state.Head = strings.TrimPrefix(record, "# branch.oid ")
			if state.Head == "(initial)" {
				state.Head = ""
				state.Unborn = true
			}
		case strings.HasPrefix(record, "# branch.head "):
			state.Branch = strings.TrimPrefix(record, "# branch.head ")
			if state.Branch == "(detached)" {
				state.Branch = ""
				state.Detached = true
			}
		case strings.HasPrefix(record, "# "):
		case strings.HasPrefix(record, "? "):
			state.Changes = append(state.Changes, Change{Path: record[2:], Index: "?", Worktree: "?", Untracked: true})
		default:
			fields := 9
			if record[0] == '2' {
				fields = 10
			}
			if record[0] == 'u' {
				fields = 11
			}
			parts := strings.SplitN(record, " ", fields)
			if (record[0] != '1' && record[0] != '2' && record[0] != 'u') || len(parts) != fields || len(parts[1]) != 2 {
				return Status{}, errors.New("runrepo: invalid native git status response")
			}
			change := Change{Path: parts[fields-1], Index: parts[1][:1], Worktree: parts[1][1:], Conflicted: record[0] == 'u'}
			if record[0] == '2' {
				i++
				if i >= len(records) || records[i] == "" {
					return Status{}, errors.New("runrepo: incomplete rename status")
				}
				change.OriginalPath = records[i]
			}
			state.Changes = append(state.Changes, change)
		}
	}
	if !state.Unborn && !validOID(state.Head) {
		return Status{}, errors.New("runrepo: native git status omitted HEAD")
	}
	return state, nil
}

// Remotes reads the checkout's actual URLs and configured upstream. None is
// inferred from the workspace mirror or silently selected for publication.
func (s *Service) Remotes(ctx context.Context, run Execution, branch string) ([]protocol.RunGitRemote, *protocol.RunGitUpstream, error) {
	out, err := s.git(ctx, run, false, "remote")
	text, err := complete(out, err)
	if err != nil {
		return nil, nil, err
	}
	remotes := []protocol.RunGitRemote{}
	total := 0
	for _, name := range strings.Fields(text) {
		if len(remotes) >= 64 {
			return remotes, nil, ErrTruncated
		}
		remote := protocol.RunGitRemote{Name: name, FetchURLs: []string{}, PushURLs: []string{}}
		for _, push := range []bool{false, true} {
			args := []string{"remote", "get-url", "--all"}
			if push {
				args = append(args, "--push")
			}
			urlOutput, urlErr := s.git(ctx, run, false, append(args, name)...)
			urls, urlErr := complete(urlOutput, urlErr)
			if urlErr != nil {
				return remotes, nil, urlErr
			}
			total += len(urls)
			if total > MaxOutput {
				return remotes, nil, ErrTruncated
			}
			values := strings.Split(strings.TrimSuffix(urls, "\n"), "\n")
			if push {
				remote.PushURLs = values
			} else {
				remote.FetchURLs = values
			}
		}
		remotes = append(remotes, remote)
	}
	if branch == "" {
		return remotes, nil, nil
	}
	out, err = s.git(ctx, run, false, "for-each-ref", "--format=%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/"+branch)
	text, err = complete(out, err)
	if err != nil {
		return remotes, nil, err
	}
	parts := strings.Split(strings.TrimSuffix(text, "\n"), "\x00")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return remotes, &protocol.RunGitUpstream{Remote: parts[0], Branch: strings.TrimPrefix(parts[1], "refs/heads/")}, nil
	}
	return remotes, nil, nil
}

type DiffRequest struct {
	Paths  []string `json:"paths,omitempty"`
	Staged bool     `json:"staged"`
}
type DiffResult struct {
	State  Expected      `json:"state"`
	Output CommandOutput `json:"output"`
}

// Diff is the native tracked-file diff, either worktree vs index or index vs
// HEAD. Untracked paths are explicitly present in Status, not fabricated as a
// tracked diff. State identifies the HEAD observed before reading the diff;
// worktree contents can change during any native read.
func (s *Service) Diff(ctx context.Context, run Execution, req DiffRequest) (DiffResult, error) {
	if err := validatePaths(req.Paths, false); err != nil {
		return DiffResult{}, err
	}
	state, err := s.Status(ctx, run)
	if err != nil {
		return DiffResult{}, err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv"}
	if req.Staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	args = append(args, req.Paths...)
	out, err := s.git(ctx, run, false, args...)
	return DiffResult{State: Expected{Branch: state.Branch, Head: state.Head}, Output: out}, err
}

type CommitRequest struct {
	Expected Expected `json:"expected"`
	Paths    []string `json:"paths"`
	Message  string   `json:"message"`
}
type CommitResult struct {
	Committed    bool          `json:"committed"`
	Head         string        `json:"head"`
	IndexUpdated bool          `json:"index_updated"`
	Output       CommandOutput `json:"output"`
}

// Commit constructs selected worktree paths in an isolated index and publishes
// with a native HEAD/referent compare-and-swap transaction. After Git prepares
// and holds both reference locks, symbolic HEAD is checked before commit.
// Signing follows commit.gpgsign; commit-tree does not run commit hooks.
// Native writers can still edit selected files while their contents are read.
// Index reconciliation is separate and may fail after publication; Committed
// remains true and the caller must not replay it.
func (s *Service) Commit(ctx context.Context, run Execution, req CommitRequest) (result CommitResult, err error) {
	if err = validatePaths(req.Paths, true); err != nil {
		return result, err
	}
	if req.Message == "" || len(req.Message) > protocol.MaxRunGitMessageBytes || strings.ContainsRune(req.Message, 0) {
		return result, errors.New("runrepo: commit message is required and limited to 16 KiB")
	}
	if err = s.CheckExpected(ctx, run, req.Expected); err != nil {
		return result, err
	}
	req.Message, err = s.withCoAuthors(ctx, run, req.Message, protocol.MaxRunGitMessageBytes)
	if err != nil {
		return result, err
	}
	state, err := s.Status(ctx, run)
	if err != nil {
		return result, err
	}
	for _, change := range state.Changes {
		if change.Conflicted {
			return result, errors.New("runrepo: checkout has unresolved conflicts; resolve with native Git before committing selected paths")
		}
	}
	for _, operation := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		out, operationErr := s.git(ctx, run, false, "rev-parse", "--quiet", "--verify", operation)
		if operationErr == nil {
			return result, fmt.Errorf("runrepo: %s is active; complete the operation with native Git", operation)
		}
		var commandErr *CommandError
		if !errors.As(operationErr, &commandErr) || commandErr.Cause != nil || out.ExitCode != 1 {
			return result, operationErr
		}
	}
	selected := make(map[string]bool, len(req.Paths))
	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		if selected[p] {
			continue
		}
		var found *Change
		for i := range state.Changes {
			if state.Changes[i].Path == p {
				found = &state.Changes[i]
				break
			}
		}
		if found == nil {
			return result, fmt.Errorf("runrepo: selected path %q is not a changed file; refresh status", p)
		}
		selected[p] = true
		paths = append(paths, p)
		if found.OriginalPath != "" && (found.Index == "R" || found.Worktree == "R") && !selected[found.OriginalPath] {
			selected[found.OriginalPath] = true
			paths = append(paths, found.OriginalPath)
		}
	}
	if err = validatePaths(paths, true); err != nil {
		return result, err
	}
	out, err := s.command(ctx, run, true, "mktemp", "-d", "/tmp/aether-runrepo-XXXXXXXXXX")
	dir, err := complete(out, err)
	if err != nil {
		return result, err
	}
	dir = strings.TrimSpace(dir)
	if !strings.HasPrefix(dir, "/tmp/aether-runrepo-") || strings.ContainsAny(strings.TrimPrefix(dir, "/tmp/"), "/\x00\n\r") {
		return result, errors.New("runrepo: invalid temporary index directory")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		// Even private-index cleanup rechecks the current account authority.
		// Revocation can leave this private /tmp allocation for the run's
		// normal container cleanup; it never justifies another native exec.
		_, cleanupErr := s.command(cleanupCtx, run, true, "rm", "-rf", "--", dir)
		if cleanupErr != nil {
			if err == nil {
				result.Output = FailureOutput(result.Output, cleanupErr)
			}
			err = errors.Join(err, fmt.Errorf("runrepo: remove private index: %w", cleanupErr))
		}
	}()
	indexGit := func(args ...string) (CommandOutput, error) {
		argv := []string{"env", "GIT_INDEX_FILE=" + dir + "/index", "git", "--no-pager", "-c", "color.ui=false"}
		return s.command(ctx, run, true, append(argv, args...)...)
	}
	if _, err = indexGit("read-tree", req.Expected.Head); err != nil {
		return result, err
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err = indexGit(args...); err != nil {
		return result, err
	}
	out, err = indexGit("write-tree")
	tree, err := complete(out, err)
	if err != nil {
		return result, err
	}
	tree = strings.TrimSpace(tree)
	if !validOID(tree) {
		return result, errors.New("runrepo: invalid native tree ID")
	}
	// A selected file may become a directory while native Git stages it.
	// Verify the resulting tree contains no descendants or other implicit
	// additions: the selection is an exact file set, not a pathspec prefix.
	out, err = s.git(ctx, run, false, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", req.Expected.Head, tree)
	changed, err := complete(out, err)
	if err != nil {
		return result, err
	}
	for _, path := range strings.Split(changed, "\x00") {
		if path != "" && !selected[path] {
			return result, fmt.Errorf("runrepo: selected paths changed while staging; unexpected path %q; refresh before committing", path)
		}
	}
	out, err = s.git(ctx, run, false, "rev-parse", "--verify", req.Expected.Head+"^{tree}")
	oldTree, err := complete(out, err)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(oldTree) == tree {
		return result, errors.New("runrepo: selected paths have no worktree changes to commit")
	}
	out, err = s.git(ctx, run, false, "config", "--type=bool", "--get", "commit.gpgsign")
	if err != nil {
		var ce *CommandError
		if !errors.As(err, &ce) || ce.Cause != nil || out.ExitCode != 1 {
			return result, err
		}
	}
	args = []string{"commit-tree", tree, "-p", req.Expected.Head, "-m", req.Message}
	if strings.TrimSpace(out.Stdout) == "true" {
		args = append(args, "-S")
	}
	if err = s.CheckExpected(ctx, run, req.Expected); err != nil {
		return result, err
	}
	out, err = s.git(ctx, run, true, args...)
	result.Output = out
	commit, err := complete(out, err)
	if err != nil {
		return result, err
	}
	commit = strings.TrimSpace(commit)
	if !validOID(commit) {
		return result, errors.New("runrepo: invalid native commit ID")
	}
	if err = s.CheckExpected(ctx, run, req.Expected); err != nil {
		return result, err
	}
	out, err = s.refTransaction(ctx, run, dir, req.Expected, commit, "commit: "+strings.SplitN(req.Message, "\n", 2)[0])
	if err != nil {
		result.Output = out
		// A lost exec response may hide a successful reference transaction.
		// Reconcile only by reading its explicit branch, never by retrying.
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		current, readErr := s.git(reconcileCtx, run, false, "rev-parse", "--verify", "refs/heads/"+req.Expected.Branch)
		if readErr == nil && !current.Truncated && strings.TrimSpace(current.Stdout) == commit {
			result.Committed = true
			result.Head = commit
		}
		if readErr != nil {
			err = errors.Join(err, fmt.Errorf("runrepo: reconcile commit publication: %w", readErr))
		}
		// Preserve native stderr and also report current branch/full HEAD.
		if stateErr := s.CheckExpected(reconcileCtx, run, req.Expected); stateErr != nil {
			err = errors.Join(err, stateErr)
		}
		return result, err
	}
	result.Committed = true
	result.Head = commit
	if err = s.CheckExpected(ctx, run, Expected{Branch: req.Expected.Branch, Head: commit}); err != nil {
		return result, err
	}
	// reset uses Git's real index.lock and changes only the selected paths.
	// It cannot exclude a native writer that deliberately edits the same paths.
	out, err = s.git(ctx, run, true, append([]string{"reset", "--quiet", commit, "--"}, paths...)...)
	if err != nil {
		result.Output = out
		return result, err
	}
	result.IndexUpdated = true
	return result, nil
}

// Updating HEAD dereferences it under Git's own HEAD and branch locks. A
// prepared transaction retains both until commit/abort (Git 2.27+). The fixed
// shell checks the symbolic target only after the native prepare acknowledgement.
// Git routes transaction-hook stdout to stderr, so hooks cannot spoof that ACK.
// See Git refs/files-backend.c: split_symref_update, lock_ref_for_update;
// refs.c: ref_transaction_prepare; builtin/update-ref.c: report_ok.
// FIFOs carry protocol bytes only; they are not application-owned ref locks.
const preparedRefInput = `set -eu
dir=$1; expected=$2; text=$3; shift 3
pid=
cleanup() {
	status=$?
	trap - EXIT HUP INT TERM PIPE
	exec 3>&- 4<&-
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || :
		wait "$pid" || :
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 141' PIPE
mkfifo "$dir/ref-input" "$dir/ref-output"
git --no-pager -c color.ui=false "$@" <"$dir/ref-input" >"$dir/ref-output" &
pid=$!
exec 3>"$dir/ref-input"
exec 4<"$dir/ref-output"
ack() {
	if IFS= read -r reply <&4 && [ "$reply" = "$1: ok" ]; then
		printf '%s\n' "$reply"
		return
	fi
	exec 3>&-
	if wait "$pid"; then status=1; else status=$?; fi
	pid=
	printf 'runrepo: native reference transaction did not acknowledge %s\n' "$1" >&2
	exit "$status"
}
printf '%s' "$text" >&3
ack start
ack prepare
if actual=$(git symbolic-ref --quiet --no-recurse HEAD); then
	if [ "$actual" = "$expected" ]; then
		printf 'commit\n' >&3
		ack commit
		exec 3>&-
		if wait "$pid"; then status=0; else status=$?; fi
		pid=
		exit "$status"
	fi
	printf 'runrepo: symbolic HEAD changed: expected %s, found %s\n' "$expected" "$actual" >&2
else
	printf '%s\n' 'runrepo: symbolic HEAD changed to a detached or unreadable reference while preparing commit' >&2
fi
printf 'abort\n' >&3
ack abort
exec 3>&-
if wait "$pid"; then status=1; else status=$?; fi
pid=
exit "$status"
`

func (s *Service) refTransaction(ctx context.Context, run Execution, dir string, expected Expected, newHead, message string) (CommandOutput, error) {
	input := "start\nupdate HEAD " + newHead + " " + expected.Head + "\nprepare\n"
	argv := []string{"sh", "-c", preparedRefInput, "aether-runrepo", dir, "refs/heads/" + expected.Branch, input, "update-ref", "--stdin", "-m", message}
	return s.command(ctx, run, true, argv...)
}

// runtime.Exec has no stdin parameter. The fixed shell feeds only a positional
// data argument to one native Git process; no request text is shell code.
func (s *Service) gitInput(ctx context.Context, run Execution, mutate bool, input string, args ...string) (CommandOutput, error) {
	argv := []string{"sh", "-c", `text=$1; shift; printf '%s' "$text" | "$@"`, "aether-runrepo", input, "git", "--no-pager", "-c", "color.ui=false"}
	return s.command(ctx, run, mutate, append(argv, args...)...)
}

func (s *Service) withCoAuthors(ctx context.Context, run Execution, message string, limit int) (string, error) {
	if run.CoAuthors == nil {
		return message, nil
	}
	out, err := s.git(ctx, run, false, "var", "GIT_AUTHOR_IDENT")
	identity, err := complete(out, err)
	if err != nil {
		return "", err
	}
	start, end := strings.LastIndex(identity, "<"), strings.LastIndex(identity, ">")
	if start < 0 || end <= start {
		return "", errors.New("runrepo: native Git author has no email")
	}
	trailers, err := run.CoAuthors(ctx, identity[start+1:end])
	if err != nil {
		return "", err
	}
	if len(trailers) == 0 {
		return message, nil
	}
	args := []string{"interpret-trailers", "--if-exists=addIfDifferent", "--if-missing=add"}
	for _, trailer := range trailers {
		if !cleanText(trailer, 4096) || !strings.HasPrefix(trailer, "Co-authored-by: ") {
			return "", errors.New("runrepo: invalid configured coauthor trailer")
		}
		args = append(args, "--trailer", trailer)
	}
	out, err = s.gitInput(ctx, run, false, message, args...)
	text, err := complete(out, err)
	if err != nil {
		return "", err
	}
	if len(text) > limit {
		return "", errors.New("runrepo: message with coauthors exceeds input limit")
	}
	return text, nil
}

type PushTarget struct {
	Remote     string `json:"remote"`
	Repository string `json:"repository"`
	HeadBranch string `json:"head_branch"`
}
type PushRequest struct {
	Expected Expected   `json:"expected"`
	Target   PushTarget `json:"target"`
}
type PushResult struct {
	Pushed bool          `json:"pushed"`
	Head   string        `json:"head"`
	Target PushTarget    `json:"target"`
	Output CommandOutput `json:"output"`
}

func validPushRepository(repository string) bool {
	if !cleanText(repository, 4096) || strings.HasPrefix(repository, "-") {
		return false
	}
	if strings.HasPrefix(repository, "/") {
		return true
	}
	if strings.HasPrefix(repository, "git@") && strings.Contains(repository, ":") && !strings.Contains(repository, "://") {
		return true
	}
	u, err := url.Parse(repository)
	return err == nil && (u.Scheme == "https" || u.Scheme == "ssh") && u.Host != "" && u.RawQuery == "" && u.Fragment == "" && (u.User == nil || u.Scheme == "ssh")
}

func (s *Service) Push(ctx context.Context, run Execution, req PushRequest) (PushResult, error) {
	result := PushResult{Target: req.Target, Head: req.Expected.Head}
	if !cleanText(req.Target.Remote, 256) || strings.HasPrefix(req.Target.Remote, "-") || strings.ContainsAny(req.Target.Remote, "/\\:") || !validPushRepository(req.Target.Repository) {
		return result, errors.New("runrepo: explicit remote and push repository are required")
	}
	if err := s.branch(ctx, run, req.Target.HeadBranch); err != nil {
		return result, err
	}
	out, err := s.git(ctx, run, false, "remote", "get-url", "--push", "--all", req.Target.Remote)
	urls, err := complete(out, err)
	if err != nil {
		return result, err
	}
	if strings.TrimSuffix(urls, "\n") != req.Target.Repository {
		return result, fmt.Errorf("runrepo: remote %q push destination changed or has multiple destinations: %s", req.Target.Remote, strings.TrimSpace(urls))
	}
	if err = s.CheckExpected(ctx, run, req.Expected); err != nil {
		return result, err
	}
	// Push the reviewed object ID, never a moving symbolic ref. Use the
	// captured explicit URL, so a concurrently retargeted remote cannot choose
	// the destination. Git still enforces non-fast-forward rejection.
	out, err = s.git(ctx, run, true, "-c", "push.followTags=false", "push", "--porcelain", "--no-force", "--no-mirror", "--recurse-submodules=no", "--", req.Target.Repository, req.Expected.Head+":refs/heads/"+req.Target.HeadBranch)
	result.Output = out
	result.Pushed = err == nil
	return result, err
}
