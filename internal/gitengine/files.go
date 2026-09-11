package gitengine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// MaxFileBytes is the maximum file or one-file diff response. The dashboard
// reads files inline and reports that larger files were truncated.
const MaxFileBytes = 512 << 10

// ErrInvalidPath identifies a client-supplied path that cannot be read or
// written from a repository or checkout.
var ErrInvalidPath = errors.New("invalid file path")

// ErrRevisionConflict reports that the file changed after the client read it.
var ErrRevisionConflict = errors.New("gitengine: file revision conflict")

// ErrWorkspaceMismatch reports that a run does not belong to the addressed
// workspace. It is intentionally distinct so the RPC can reject bad input.
var ErrWorkspaceMismatch = errors.New("gitengine: workspace and run do not match")

// FileRead is the engine-side file response. Writable is filled by the
// authenticated server handler; the engine sets it true for a safe file.
type FileRead struct {
	Content   []byte
	Truncated bool
	Binary    bool
	Size      int64
	Revision  string
	Writable  bool
}

// TreeEntry is one immediate child of a repository directory.
type TreeEntry struct {
	Name string
	Kind string
	Size int64
}

// ValidatePath rejects paths that could address data outside a repository or
// checkout's logical root. A blank path is the repository root for ListTree.
func ValidatePath(name string) error {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) ||
		(len(name) >= 2 && name[1] == ':') || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: path must be relative", ErrInvalidPath)
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("%w: path must use slash separators", ErrInvalidPath)
	}
	components := strings.Split(name, "/")
	for i, component := range components {
		if component == ".." || component == "." || component == ".git" ||
			(component == "" && i != len(components)-1) ||
			strings.ContainsFunc(component, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return fmt.Errorf("%w: path contains forbidden component", ErrInvalidPath)
		}
	}
	return nil
}

// ListTree lists immediate children of dir. Bare repositories use ref as the
// tree reference. A checkout is identified by its .git directory and uses the
// working tree regardless of ref, so uncommitted files are visible.
func (e *Engine) ListTree(ctx context.Context, repoPath, ref, dir string) ([]TreeEntry, error) {
	if err := ValidatePath(dir); err != nil {
		return nil, err
	}
	if e.isCheckout(repoPath) {
		return e.listCheckout(ctx, repoPath, dir)
	}
	return e.listBare(ctx, repoPath, ref, dir)
}

func (e *Engine) listBare(ctx context.Context, repoPath, ref, dir string) ([]TreeEntry, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("gitengine: repository ref is required")
	}
	args := []string{"ls-tree", "-z", "-l", "refs/heads/" + ref, "--"}
	if dir != "" {
		args = append(args, strings.TrimSuffix(dir, "/")+"/")
	}
	output, err := e.gitBytes(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]TreeEntry)
	prefix := strings.TrimSuffix(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 {
			return nil, errors.New("gitengine: malformed ls-tree output")
		}
		meta := strings.Fields(string(fields[0]))
		if len(meta) < 3 {
			return nil, errors.New("gitengine: malformed ls-tree metadata")
		}
		kind := meta[1]
		if kind != "blob" && kind != "tree" {
			continue
		}
		name := string(fields[1])
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		child, _, nested := strings.Cut(name, "/")
		entry := TreeEntry{Name: child, Kind: "file"}
		if nested || kind == "tree" {
			entry.Kind = "dir"
		}
		if entry.Kind == "file" && len(meta) >= 4 && meta[3] != "-" {
			entry.Size, err = strconv.ParseInt(meta[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("gitengine: parse size for %s: %w", child, err)
			}
		}
		entries[child] = entry
	}
	return sortedTreeEntries(entries), nil
}

func (e *Engine) listCheckout(ctx context.Context, checkout, dir string) ([]TreeEntry, error) {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	if dir != "" {
		args = append(args, "--", strings.TrimSuffix(dir, "/")+"/")
	}
	output, err := e.gitBytes(ctx, checkout, args...)
	if err != nil {
		return nil, err
	}
	root, err := e.openCheckoutRoot(checkout)
	if err != nil {
		return nil, fmt.Errorf("gitengine: open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries := make(map[string]TreeEntry)
	prefix := strings.TrimSuffix(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		name := string(record)
		if err := ValidatePath(name); err != nil {
			continue
		}
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		child, _, nested := strings.Cut(name, "/")
		if child == "" {
			continue
		}
		entry := TreeEntry{Name: child, Kind: "dir"}
		if !nested {
			statPath := child
			if dir != "" {
				statPath = strings.TrimSuffix(dir, "/") + "/" + child
			}
			file, openErr := rootfs.Open(root, statPath)
			if openErr != nil {
				// Missing, symlink, and special-file leaves are intentionally
				// omitted from a tree listing.
				continue
			}
			info, statErr := file.Stat()
			_ = file.Close()
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			entry.Kind = "file"
			entry.Size = info.Size()
		}
		entries[child] = entry
	}
	return sortedTreeEntries(entries), nil
}

func sortedTreeEntries(entries map[string]TreeEntry) []TreeEntry {
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReadFile reads path from a bare repository ref or directly from a checkout.
// It preserves the historical compact return shape; ReadFileMeta includes
// revision and writable metadata.
func (e *Engine) ReadFile(ctx context.Context, repoPath, ref, path string, maxBytes int) ([]byte, bool, bool, error) {
	result, err := e.readFile(ctx, repoPath, ref, path, maxBytes)
	if err != nil {
		return nil, false, false, err
	}
	return result.Content, result.Truncated, result.Binary, nil
}

// ReadFileMeta is ReadFile with the revision and safety metadata required by
// the editor. The revision is omitted unless the complete file is valid text.
func (e *Engine) ReadFileMeta(ctx context.Context, repoPath, ref, path string, maxBytes int) (FileRead, error) {
	return e.readFile(ctx, repoPath, ref, path, maxBytes)
}

func (e *Engine) readFile(ctx context.Context, repoPath, ref, path string, maxBytes int) (FileRead, error) {
	if err := ValidatePath(path); err != nil {
		return FileRead{}, err
	}
	if path == "" {
		return FileRead{}, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	var (
		content   []byte
		truncated bool
		size      int64
		err       error
	)
	if e.isCheckout(repoPath) {
		content, truncated, size, err = e.readCheckoutFile(repoPath, path, maxBytes)
	} else {
		if strings.TrimSpace(ref) == "" {
			return FileRead{}, errors.New("gitengine: repository ref is required")
		}
		content, truncated, size, err = e.readBareFile(ctx, repoPath, ref, path, maxBytes)
	}
	if err != nil {
		return FileRead{}, err
	}
	binary := isBinary(content) || !utf8.Valid(content)
	result := FileRead{Content: content, Truncated: truncated, Binary: binary, Size: size, Writable: !truncated && !binary}
	if !truncated && !binary {
		sum := sha256.Sum256(content)
		result.Revision = fmt.Sprintf("%x", sum[:])
	}
	return result, nil
}

func (e *Engine) openCheckoutRoot(checkout string) (*os.Root, error) {
	if e.checkoutsRoot == nil {
		return nil, ErrInvalidPath
	}
	rel, err := filepath.Rel(filepath.Clean(e.cfg.CheckoutsDir), filepath.Clean(checkout))
	if err != nil || rel == "." || rel == ".." || strings.ContainsRune(rel, filepath.Separator) {
		return nil, ErrInvalidPath
	}
	return rootfs.OpenRoot(e.checkoutsRoot, filepath.ToSlash(rel))
}

func (e *Engine) readCheckoutFile(checkout, path string, maxBytes int) ([]byte, bool, int64, error) {
	root, err := e.openCheckoutRoot(checkout)
	if err != nil {
		return nil, false, 0, fmt.Errorf("gitengine: open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := rootfs.Open(root, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, 0, fmt.Errorf("gitengine: stat checkout path %s: %w", path, err)
		}
		return nil, false, 0, fmt.Errorf("%w: checkout path %s: %v", ErrInvalidPath, path, err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("%w: checkout path is not a regular file", ErrInvalidPath)
		}
		return nil, false, 0, err
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, false, 0, fmt.Errorf("gitengine: read checkout file: %w", err)
	}
	truncated := len(content) > maxBytes
	if truncated {
		content = content[:maxBytes]
	}
	return content, truncated, opened.Size(), nil
}

func isBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0
}

func checkoutCurrentFile(parent *os.Root, name string) (os.FileInfo, []byte, bool, error) {
	file, err := rootfs.Open(parent, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: checkout path %s: %v", ErrInvalidPath, name, err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, false, err
	}
	if !opened.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("%w: checkout path is not a regular file", ErrInvalidPath)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, nil, false, err
	}
	if len(data) > MaxFileBytes {
		return opened, nil, true, nil
	}
	return opened, data, true, nil
}

func ensureCheckoutParents(root *os.Root, name string) (*os.Root, error) {
	dirName := "."
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		dirName = name[:i]
	}
	parent := root
	ownedParent := false
	closeParent := func() {
		if ownedParent {
			_ = parent.Close()
		}
	}
	owner, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	if dirName != "." {
		for _, part := range strings.Split(dirName, "/") {
			child, openErr := rootfs.OpenRoot(parent, part)
			created := false
			if openErr != nil {
				if !errors.Is(openErr, fs.ErrNotExist) {
					closeParent()
					return nil, fmt.Errorf("%w: checkout parent %s: %v", ErrInvalidPath, part, openErr)
				}
				if mkdirErr := parent.Mkdir(part, 0o755); mkdirErr != nil {
					if !errors.Is(mkdirErr, fs.ErrExist) {
						closeParent()
						return nil, fmt.Errorf("gitengine: create checkout parent %s: %w", part, mkdirErr)
					}
				} else {
					created = true
				}
				child, openErr = rootfs.OpenRoot(parent, part)
				if openErr != nil {
					closeParent()
					return nil, fmt.Errorf("%w: checkout parent %s: %v", ErrInvalidPath, part, openErr)
				}
			}
			if created {
				if err = chownCheckoutDir(child, owner); err != nil {
					_ = child.Close()
					closeParent()
					return nil, fmt.Errorf("gitengine: own checkout parent %s: %w", part, err)
				}
			}
			closeParent()
			parent, ownedParent = child, true
			owner, err = parent.Stat(".")
			if err != nil {
				closeParent()
				return nil, err
			}
		}
	}
	if !ownedParent {
		parent, err = rootfs.OpenRoot(root, ".")
		if err != nil {
			return nil, err
		}
	}
	return parent, nil
}

type bareFileEntry struct {
	mode string
	oid  string
	size int64
}

func (e *Engine) bareFileEntry(ctx context.Context, repo, ref, name string) (bareFileEntry, error) {
	if err := e.checkBranchName(ctx, ref); err != nil {
		return bareFileEntry{}, err
	}
	output, err := e.gitBytes(ctx, repo, "ls-tree", "-z", "-l", "refs/heads/"+ref, "--", name)
	if err != nil {
		return bareFileEntry{}, err
	}
	var entry bareFileEntry
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 || string(fields[1]) != name {
			return bareFileEntry{}, fmt.Errorf("gitengine: malformed file tree")
		}
		meta := strings.Fields(string(fields[0]))
		if len(meta) < 4 || entry.oid != "" {
			return bareFileEntry{}, fmt.Errorf("gitengine: malformed file tree")
		}
		entry.mode = meta[0]
		if meta[1] != "blob" || (entry.mode != "100644" && entry.mode != "100755") {
			return bareFileEntry{}, fmt.Errorf("%w: file is not a regular file", ErrInvalidPath)
		}
		entry.oid = meta[2]
		entry.size, err = strconv.ParseInt(meta[3], 10, 64)
		if err != nil {
			return bareFileEntry{}, fmt.Errorf("gitengine: parse file size: %w", err)
		}
	}
	return entry, nil
}

func (e *Engine) readBareFile(ctx context.Context, repo, ref, name string, maxBytes int) ([]byte, bool, int64, error) {
	entry, err := e.bareFileEntry(ctx, repo, ref, name)
	if err != nil {
		return nil, false, 0, err
	}
	if entry.oid == "" {
		return nil, false, 0, fmt.Errorf("gitengine: file not found: %w", fs.ErrNotExist)
	}
	content, truncated, err := e.gitBounded(ctx, repo, maxBytes, "cat-file", "-p", entry.oid)
	if err != nil {
		return nil, false, 0, err
	}
	return content, truncated, entry.size, nil
}

// FilesWrite writes one complete UTF-8 text file to a run checkout or to a
// workspace branch. The caller supplies the authenticated member identity for
// base-branch commit attribution.
func (e *Engine) FilesWrite(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, name string, content []byte, revision string, author domain.GitIdentity, signingKey []byte) (FileRead, error) {
	if err := ValidatePath(name); err != nil {
		return FileRead{}, err
	}
	if name == "" {
		return FileRead{}, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	if len(content) > MaxFileBytes {
		return FileRead{}, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalidPath, MaxFileBytes)
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return FileRead{}, fmt.Errorf("%w: file must be valid UTF-8 text", ErrInvalidPath)
	}
	if revision != "" && !validRevision(revision) {
		return FileRead{}, fmt.Errorf("%w: invalid file revision", ErrInvalidPath)
	}

	e.fileWriteMu.Lock()
	defer e.fileWriteMu.Unlock()
	if run != "" {
		checkout, err := e.existingCheckoutPath(run)
		if err != nil {
			return FileRead{}, err
		}
		meta, err := e.readRunMeta(run)
		if err != nil {
			return FileRead{}, err
		}
		if meta.Workspace != workspace {
			return FileRead{}, fmt.Errorf("%w: run belongs to another workspace", ErrWorkspaceMismatch)
		}
		return writeCheckoutFile(e, checkout, name, content, revision)
	}
	repo, err := e.existingRepoPath(workspace)
	if err != nil {
		return FileRead{}, err
	}
	if strings.TrimSpace(ref) == "" {
		return FileRead{}, errors.New("gitengine: repository ref is required")
	}
	return e.writeBareFile(ctx, repo, ref, name, content, revision, author, signingKey)
}

func validRevision(revision string) bool {
	if len(revision) != sha256.Size*2 {
		return false
	}
	for _, r := range revision {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func fileRevision(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum[:])
}
func writeCheckoutFile(e *Engine, checkout, name string, content []byte, revision string) (FileRead, error) {
	root, err := e.openCheckoutRoot(checkout)
	if err != nil {
		return FileRead{}, fmt.Errorf("gitengine: open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	parent, err := ensureCheckoutParents(root, name)
	if err != nil {
		return FileRead{}, err
	}
	defer func() { _ = parent.Close() }()
	leaf := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		leaf = name[i+1:]
	}
	oldInfo, oldContent, exists, err := checkoutCurrentFile(parent, leaf)
	if err != nil {
		return FileRead{}, err
	}
	mode := os.FileMode(0o644)
	if !exists {
		if revision != "" {
			return FileRead{}, ErrRevisionConflict
		}
	} else {
		if revision == "" || oldContent == nil || fileRevision(oldContent) != revision {
			return FileRead{}, ErrRevisionConflict
		}
		mode = oldInfo.Mode().Perm()
	}
	tmp := ".aether-file-" + rand.Text()
	file, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		_ = parent.Remove(tmp)
	}
	if err = chownCheckoutTemp(file, parent, ".", oldInfo); err != nil {
		cleanup()
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	if err = file.Chmod(mode.Perm()); err != nil {
		cleanup()
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	if _, err = file.Write(content); err != nil {
		cleanup()
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	if err = file.Sync(); err != nil {
		cleanup()
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	if err = file.Close(); err != nil {
		cleanup()
		return FileRead{}, fmt.Errorf("gitengine: stage checkout file: %w", err)
	}
	newInfo, newContent, nowExists, err := checkoutCurrentFile(parent, leaf)
	if err != nil {
		_ = parent.Remove(tmp)
		return FileRead{}, err
	}
	if nowExists != exists || (exists && (newContent == nil || fileRevision(newContent) != revision || newInfo.Mode().Perm() != mode)) {
		_ = parent.Remove(tmp)
		return FileRead{}, ErrRevisionConflict
	}
	if err := parent.Rename(tmp, leaf); err != nil {
		_ = parent.Remove(tmp)
		return FileRead{}, fmt.Errorf("gitengine: replace checkout file: %w", err)
	}
	return FileRead{
		Content:  append([]byte(nil), content...),
		Size:     int64(len(content)),
		Revision: fileRevision(content),
		Writable: true,
	}, nil
}

func (e *Engine) writeBareFile(ctx context.Context, repo, ref, name string, content []byte, revision string, author domain.GitIdentity, signingKey []byte) (FileRead, error) {
	if err := e.checkBranchName(ctx, ref); err != nil {
		return FileRead{}, err
	}
	base, err := e.git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+ref)
	if err != nil {
		return FileRead{}, err
	}
	entry, err := e.bareFileEntry(ctx, repo, ref, name)
	if err != nil {
		return FileRead{}, err
	}
	var oldRevision string
	if entry.oid != "" {
		old, truncated, _, readErr := e.readBareFile(ctx, repo, ref, name, MaxFileBytes)
		if readErr != nil {
			return FileRead{}, readErr
		}
		if truncated || len(old) > MaxFileBytes || !utf8.Valid(old) || isBinary(old) {
			return FileRead{}, ErrRevisionConflict
		}
		oldRevision = fileRevision(old)
	}
	if (entry.oid == "") != (revision == "") || (entry.oid != "" && oldRevision != revision) {
		return FileRead{}, ErrRevisionConflict
	}
	blob, err := e.gitInput(ctx, repo, gitEnv(), content, "hash-object", "-w", "--stdin")
	if err != nil {
		return FileRead{}, err
	}
	scratch, err := os.MkdirTemp("", "aether-file-index-")
	if err != nil {
		return FileRead{}, fmt.Errorf("gitengine: create file index: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	index := filepath.Join(scratch, "index")
	env := append(gitEnv(), "GIT_INDEX_FILE="+index)
	if _, err = e.gitIn(ctx, repo, env, "read-tree", base); err != nil {
		return FileRead{}, err
	}
	mode := entry.mode
	if mode == "" {
		mode = "100644"
	}
	if _, err = e.gitIn(ctx, repo, env, "update-index", "--add", "--cacheinfo", mode, blob, name); err != nil {
		return FileRead{}, err
	}
	tree, err := e.gitIn(ctx, repo, env, "write-tree")
	if err != nil {
		return FileRead{}, err
	}
	if author.Name == "" {
		author.Name = "Aether"
	}
	if author.Email == "" {
		author.Email = "aether@localhost"
	}
	commitEnv := append(env, "GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_COMMITTER_NAME=Aether", "GIT_COMMITTER_EMAIL=aether@localhost")
	commitArgs := []string{
		"-c", "user.name=Aether", "-c", "user.email=aether@localhost",
		"-c", "gpg.format=ssh", "-c", "gpg.ssh.program=ssh-keygen",
	}
	if len(signingKey) > 0 {
		keyPath, cleanup, keyErr := writeTempSigningKey(signingKey)
		if keyErr != nil {
			return FileRead{}, keyErr
		}
		defer cleanup()
		commitArgs = append(commitArgs, "-c", "user.signingkey="+keyPath, "-c", "commit.gpgsign=true")
	} else {
		commitArgs = append(commitArgs, "-c", "commit.gpgsign=false")
	}
	commitArgs = append(commitArgs, "commit-tree", tree, "-p", base, "-m", "Edit file in Aether")
	commit, err := e.gitIn(ctx, repo, commitEnv, commitArgs...)
	if err != nil {
		return FileRead{}, err
	}
	if _, err = e.git(ctx, repo, "update-ref", "refs/heads/"+ref, commit, base); err != nil {
		now, nowErr := e.git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+ref)
		if nowErr == nil && now != base {
			return FileRead{}, ErrRevisionConflict
		}
		return FileRead{}, err
	}
	result, err := e.ReadFileMeta(ctx, repo, ref, name, MaxFileBytes)
	if err != nil {
		return FileRead{}, err
	}
	result.Writable = true
	return result, nil
}

func (e *Engine) gitInput(ctx context.Context, dir string, env []string, input []byte, args ...string) (string, error) {
	argv := append([]string{"-C", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// FileDiff renders only path from a run checkout against its recorded base.
// Like RunPatch, it stages into a scratch index and object directory without
// writing to the checkout's .git database.
func (e *Engine) FileDiff(ctx context.Context, run domain.RunID, path string) (Patch, error) {
	if err := ValidatePath(path); err != nil {
		return Patch{}, err
	}
	if path == "" {
		return Patch{}, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return Patch{}, err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "aether-file-diff-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for run %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index := filepath.Join(dir, "index")
	objects := filepath.Join(dir, "objects")
	if mkErr := os.MkdirAll(filepath.Join(objects, "info"), 0o700); mkErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch objects for run %s: %w", run, mkErr)
	}
	checkoutRoot, rootErr := e.openCheckoutRoot(checkout)
	if rootErr != nil {
		return Patch{}, fmt.Errorf("gitengine: open checkout for diff: %w", rootErr)
	}
	defer func() { _ = checkoutRoot.Close() }()
	gitRoot, rootErr := rootfs.OpenRoot(checkoutRoot, ".git")
	if rootErr != nil {
		return Patch{}, fmt.Errorf("gitengine: open checkout git directory: %w", rootErr)
	}
	defer func() { _ = gitRoot.Close() }()
	objectsRoot, rootErr := rootfs.OpenRoot(gitRoot, "objects")
	if rootErr != nil {
		return Patch{}, fmt.Errorf("gitengine: open checkout git objects: %w", rootErr)
	}
	defer func() { _ = objectsRoot.Close() }()
	alternates := filepath.Join(objects, "info", "alternates")
	if writeErr := os.WriteFile(alternates, []byte(objectsRoot.Name()+"\n"), 0o600); writeErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch alternates for run %s: %w", run, writeErr)
	}
	if dataFile, readErr := rootfs.Open(gitRoot, "index"); readErr == nil {
		data, fileErr := io.ReadAll(dataFile)
		_ = dataFile.Close()
		if fileErr != nil {
			return Patch{}, fmt.Errorf("gitengine: read checkout index for run %s: %w", run, fileErr)
		}
		if seedErr := os.WriteFile(index, data, 0o600); seedErr != nil {
			return Patch{}, fmt.Errorf("gitengine: seed scratch index for run %s: %w", run, seedErr)
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return Patch{}, fmt.Errorf("%w: read checkout index for run %s: %v", ErrInvalidPath, run, readErr)
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, MaxFileBytes,
		"diff", "--cached", "--no-color", "--no-renames", meta.Base, "--", path)
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
func (e *Engine) isCheckout(path string) bool {
	root, err := e.openCheckoutRoot(path)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(".git")
	return err == nil && info.Mode()&os.ModeSymlink == 0 && (info.IsDir() || info.Mode().IsRegular())
}
func (e *Engine) gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv := append([]string{"-C", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (e *Engine) gitBounded(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error) {
	argv := append([]string{"-C", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	out := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, false, fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.buf, out.over, nil
}

// FilesTree addresses a workspace base tree or a run's working checkout by
// identifiers. It is the server-facing adapter over ListTree.
func (e *Engine) FilesTree(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, dir string) ([]TreeEntry, error) {
	if run != "" {
		checkout, err := e.existingCheckoutPath(run)
		if err != nil {
			return nil, err
		}
		meta, err := e.readRunMeta(run)
		if err != nil {
			return nil, err
		}
		if meta.Workspace != workspace {
			return nil, fmt.Errorf("%w: run belongs to another workspace", ErrWorkspaceMismatch)
		}
		return e.ListTree(ctx, checkout, "", dir)
	}
	repo, err := e.existingRepoPath(workspace)
	if err != nil {
		return nil, err
	}
	return e.ListTree(ctx, repo, ref, dir)
}

// FilesRead addresses a workspace base file or a run's working checkout by
// identifiers. It is the server-facing adapter over ReadFileMeta.
func (e *Engine) FilesRead(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, path string, maxBytes int) (FileRead, error) {
	if run != "" {
		checkout, err := e.existingCheckoutPath(run)
		if err != nil {
			return FileRead{}, err
		}
		meta, err := e.readRunMeta(run)
		if err != nil {
			return FileRead{}, err
		}
		if meta.Workspace != workspace {
			return FileRead{}, fmt.Errorf("%w: run belongs to another workspace", ErrWorkspaceMismatch)
		}
		return e.ReadFileMeta(ctx, checkout, "", path, maxBytes)
	}
	repo, err := e.existingRepoPath(workspace)
	if err != nil {
		return FileRead{}, err
	}
	return e.ReadFileMeta(ctx, repo, ref, path, maxBytes)
}
