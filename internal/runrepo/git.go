package runrepo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Change struct {
	Path string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Index string `json:"index"`
	Worktree string `json:"worktree"`
	Untracked bool `json:"untracked,omitempty"`
	Conflicted bool `json:"conflicted,omitempty"`
}

type Status struct {
	Branch string `json:"branch"`
	Head string `json:"head"`
	Detached bool `json:"detached"`
	Unborn bool `json:"unborn"`
	Changes []Change `json:"changes"`
}

func (s *Service) Status(ctx context.Context, run Execution) (Status, error) {
	out, err := s.git(ctx, run, false, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	text, err := complete(out, err)
	if err != nil { return Status{}, err }
	state := Status{Changes: []Change{}}
	records := strings.Split(text, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" { continue }
		switch {
		case strings.HasPrefix(record, "# branch.oid "):
			state.Head = strings.TrimPrefix(record, "# branch.oid ")
			if state.Head == "(initial)" { state.Head = ""; state.Unborn = true }
		case strings.HasPrefix(record, "# branch.head "):
			state.Branch = strings.TrimPrefix(record, "# branch.head ")
			if state.Branch == "(detached)" { state.Branch = ""; state.Detached = true }
		case strings.HasPrefix(record, "# "):
		case strings.HasPrefix(record, "? "):
			state.Changes = append(state.Changes, Change{Path: record[2:], Index: "?", Worktree: "?", Untracked: true})
		default:
			fields := 9
			if record[0] == '2' { fields = 10 }
			if record[0] == 'u' { fields = 11 }
			parts := strings.SplitN(record, " ", fields)
			if (record[0] != '1' && record[0] != '2' && record[0] != 'u') || len(parts) != fields || len(parts[1]) != 2 {
				return Status{}, errors.New("runrepo: invalid native git status response")
			}
			change := Change{Path: parts[fields-1], Index: parts[1][:1], Worktree: parts[1][1:], Conflicted: record[0] == 'u'}
			if record[0] == '2' {
				i++
				if i >= len(records) || records[i] == "" { return Status{}, errors.New("runrepo: incomplete rename status") }
				change.OriginalPath = records[i]
			}
			state.Changes = append(state.Changes, change)
		}
	}
	if !state.Unborn && !validOID(state.Head) { return Status{}, errors.New("runrepo: native git status omitted HEAD") }
	return state, nil
}

type DiffRequest struct { Paths []string `json:"paths,omitempty"`; Staged bool `json:"staged"` }
type DiffResult struct { State Expected `json:"state"`; Output CommandOutput `json:"output"` }

// Diff is the native tracked-file diff, either worktree vs index or index vs
// HEAD. Untracked paths are explicitly present in Status, not fabricated as a
// tracked diff. State identifies the HEAD observed before reading the diff;
// worktree contents can change during any native read.
func (s *Service) Diff(ctx context.Context, run Execution, req DiffRequest) (DiffResult, error) {
	if err := validatePaths(req.Paths, false); err != nil { return DiffResult{}, err }
	state, err := s.Status(ctx, run)
	if err != nil { return DiffResult{}, err }
	args := []string{"diff", "--no-ext-diff", "--no-textconv"}
	if req.Staged { args = append(args, "--cached") }
	args = append(args, "--")
	args = append(args, req.Paths...)
	out, err := s.git(ctx, run, false, args...)
	return DiffResult{State: Expected{Branch: state.Branch, Head: state.Head}, Output: out}, err
}

type CommitRequest struct { Expected Expected `json:"expected"`; Paths []string `json:"paths"`; Message string `json:"message"` }
type CommitResult struct {
	Committed bool `json:"committed"`
	Head string `json:"head"`
	IndexUpdated bool `json:"index_updated"`
	Output CommandOutput `json:"output"`
}

// Commit constructs the selected worktree paths against the expected tree in
// an isolated index. It never includes another path's staged content. Publication
// is a native compare-and-swap of the explicit branch, not a scheduler lock.
// Signing follows commit.gpgsign; plumbing intentionally does not run commit
// hooks. Native writers can still change the worktree or switch branches during
// this multi-command operation. An index reconciliation failure after the CAS
// returns Committed=true, and must never be retried as a fresh commit blindly.
func (s *Service) Commit(ctx context.Context, run Execution, req CommitRequest) (result CommitResult, err error) {
	if err := validatePaths(req.Paths, true); err != nil { return result, err }
	if req.Message == "" || len(req.Message) > 64<<10 || strings.ContainsRune(req.Message, 0) { return result, errors.New("runrepo: commit message is required and limited to 64 KiB") }
	if _, err := s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	state, err := s.Status(ctx, run)
	if err != nil { return result, err }
	selected := make(map[string]bool, len(req.Paths))
	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		if selected[p] { continue }
		var found *Change
		for i := range state.Changes { if state.Changes[i].Path == p { found = &state.Changes[i]; break } }
		if found == nil { return result, fmt.Errorf("runrepo: selected path %q is not a changed file; refresh status", p) }
		if found.Conflicted { return result, fmt.Errorf("runrepo: selected path %q has unresolved conflicts", p) }
		selected[p] = true
		paths = append(paths, p)
		if found.OriginalPath != "" && !selected[found.OriginalPath] {
			selected[found.OriginalPath] = true
			paths = append(paths, found.OriginalPath)
		}
	}
	if err := validatePaths(paths, true); err != nil { return result, err }
	out, err := s.command(ctx, run, true, "mktemp", "-d", "/tmp/aether-runrepo-XXXXXXXXXX")
	dir, err := complete(out, err)
	if err != nil { return result, err }
	dir = strings.TrimSpace(dir)
	if !strings.HasPrefix(dir, "/tmp/aether-runrepo-") || strings.ContainsAny(strings.TrimPrefix(dir, "/tmp/"), "/\x00\n\r") { return result, errors.New("runrepo: invalid temporary index directory") }
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		// The private directory was allocated by this operation. Cleanup must
		// survive revocation; it never executes git or touches the checkout.
		_, _, _, cleanupErr := s.exec(cleanupCtx, run.ContainerID, []string{"rm", "-rf", "--", dir}, run.WorkDir)
		if cleanupErr != nil && err == nil { err = fmt.Errorf("runrepo: remove private index: %w", cleanupErr) }
	}()
	indexGit := func(args ...string) (CommandOutput, error) {
		argv := []string{"env", "GIT_INDEX_FILE="+dir+"/index", "git", "--no-pager", "-c", "color.ui=false"}
		return s.command(ctx, run, true, append(argv, args...)...)
	}
	if _, err = indexGit("read-tree", req.Expected.Head); err != nil { return result, err }
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err = indexGit(args...); err != nil { return result, err }
	out, err = indexGit("write-tree")
	tree, err := complete(out, err)
	if err != nil { return result, err }
	tree = strings.TrimSpace(tree)
	if !validOID(tree) { return result, errors.New("runrepo: invalid native tree ID") }
	out, err = s.git(ctx, run, false, "rev-parse", "--verify", req.Expected.Head+"^{tree}")
	oldTree, err := complete(out, err)
	if err != nil { return result, err }
	if strings.TrimSpace(oldTree) == tree { return result, errors.New("runrepo: selected paths have no worktree changes to commit") }
	out, err = s.git(ctx, run, false, "config", "--type=bool", "--get", "commit.gpgsign")
	if err != nil {
		var ce *CommandError
		if !errors.As(err, &ce) || ce.Cause != nil || out.ExitCode != 1 { return result, err }
	}
	args = []string{"commit-tree", tree, "-p", req.Expected.Head, "-m", req.Message}
	if strings.TrimSpace(out.Stdout) == "true" { args = append(args, "-S") }
	if _, err = s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	out, err = s.git(ctx, run, true, args...)
	result.Output = out
	commit, err := complete(out, err)
	if err != nil { return result, err }
	commit = strings.TrimSpace(commit)
	if !validOID(commit) { return result, errors.New("runrepo: invalid native commit ID") }
	if _, err = s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	out, err = s.git(ctx, run, true, "update-ref", "-m", "commit: "+strings.SplitN(req.Message, "\n", 2)[0], "refs/heads/"+req.Expected.Branch, commit, req.Expected.Head)
	if err != nil { result.Output = out; return result, err }
	result.Committed = true
	result.Head = commit
	if _, err = s.checkExpected(ctx, run, Expected{Branch: req.Expected.Branch, Head: commit}); err != nil { return result, err }
	// reset uses Git's real index.lock and changes only the selected paths.
	// It cannot exclude a native writer that deliberately edits the same paths.
	out, err = s.git(ctx, run, true, append([]string{"reset", "--quiet", commit, "--"}, paths...)...)
	if err != nil { result.Output = out; return result, err }
	result.IndexUpdated = true
	return result, nil
}

type PushTarget struct { Remote string `json:"remote"`; Repository string `json:"repository"`; HeadBranch string `json:"head_branch"` }
type PushRequest struct { Expected Expected `json:"expected"`; Target PushTarget `json:"target"` }
type PushResult struct { Pushed bool `json:"pushed"`; Head string `json:"head"`; Target PushTarget `json:"target"`; Output CommandOutput `json:"output"` }

func validPushRepository(repository string) bool {
	if !cleanText(repository, 4096) || strings.HasPrefix(repository, "-") { return false }
	if strings.HasPrefix(repository, "/") { return true }
	if strings.HasPrefix(repository, "git@") && strings.Contains(repository, ":") && !strings.Contains(repository, "://") { return true }
	u, err := url.Parse(repository)
	return err == nil && (u.Scheme == "https" || u.Scheme == "ssh") && u.Host != "" && u.RawQuery == "" && u.Fragment == "" && (u.User == nil || u.Scheme == "ssh")
}

func (s *Service) Push(ctx context.Context, run Execution, req PushRequest) (PushResult, error) {
	result := PushResult{Target: req.Target, Head: req.Expected.Head}
	if !cleanText(req.Target.Remote, 256) || strings.HasPrefix(req.Target.Remote, "-") || strings.ContainsAny(req.Target.Remote, "/\\:") || !validPushRepository(req.Target.Repository) { return result, errors.New("runrepo: explicit remote and push repository are required") }
	if err := s.branch(ctx, run, req.Target.HeadBranch); err != nil { return result, err }
	out, err := s.git(ctx, run, false, "remote", "get-url", "--push", "--all", req.Target.Remote)
	urls, err := complete(out, err)
	if err != nil { return result, err }
	if strings.TrimSuffix(urls, "\n") != req.Target.Repository { return result, fmt.Errorf("runrepo: remote %q push destination changed or has multiple destinations: %s", req.Target.Remote, strings.TrimSpace(urls)) }
	if _, err := s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	// Push the reviewed object ID, never a moving symbolic ref. Use the
	// captured explicit URL, so a concurrently retargeted remote cannot choose
	// the destination. Git still enforces non-fast-forward rejection.
	out, err = s.git(ctx, run, true, "-c", "push.followTags=false", "push", "--porcelain", "--no-force", "--no-mirror", "--recurse-submodules=no", "--", req.Target.Repository, req.Expected.Head+":refs/heads/"+req.Target.HeadBranch)
	result.Output = out
	result.Pushed = err == nil
	return result, err
}
