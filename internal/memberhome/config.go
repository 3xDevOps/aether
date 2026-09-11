package memberhome

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/rootfs"
	"github.com/3xDevOps/Aether/internal/secretscan"
)

const (
	// ConfigMaxFileBytes is the maximum UTF-8 file an editor can read or save.
	ConfigMaxFileBytes = 512 << 10
	// ConfigImportMaxFileBytes is the per-file import limit.
	ConfigImportMaxFileBytes = 1 << 20
	// ConfigImportMaxBytes is the decoded aggregate import limit.
	ConfigImportMaxBytes = 20 << 20
	ConfigImportMaxFiles = 2000
)

var (
	ErrConfigNotFound = errors.New("config: not found")
	ErrConfigDenied   = errors.New("config: denied")
	ErrConfigConflict = errors.New("config: conflict")
	ErrConfigTooLarge = errors.New("config: too large")
	ErrConfigBinary   = errors.New("config: binary")
)

// ConfigFile is one browser-imported file, relative to a harness profile root.
type ConfigFile struct {
	Path    string
	Content []byte
	Mode    uint32
}

// ConfigExcluded is one imported file intentionally left out.
type ConfigExcluded struct {
	Path   string
	Reason string
	Detail string
}

// ConfigImportResult reports files installed in a member's persistent home.
type ConfigImportResult struct {
	Files    int
	Bytes    int64
	Excluded []ConfigExcluded
}

// ConfigTreeEntry is one immediate child of a profile directory.
type ConfigTreeEntry struct {
	Name string
	Kind string
	Size int64
}

// ConfigRead is the bounded file result used by config.read and config.write.
type ConfigRead struct {
	Content   []byte
	Truncated bool
	Binary    bool
	Size      int64
	Revision  string
	Writable  bool
}

// LockConfigRoot serializes Aether writes for one member/profile root. The
// lock is intentionally shared with profile materialization so legacy profile
// operations cannot race an editor save.
func (m *Manager) LockConfigRoot(member domain.MemberID, localRoot string) (func(), error) {
	if err := validateMemberID(string(member)); err != nil {
		return nil, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	root, err := configRootPath(localRoot)
	if err != nil {
		return nil, err
	}
	key := string(member) + "\x00" + root
	m.mu.Lock()
	lock := m.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[key] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock, nil
}

// OpenConfigRoot opens a member's profile root through the trusted home
// descriptor. Callers must hold LockConfigRoot for the member/root while
// using the returned descriptor.
func (m *Manager) OpenConfigRoot(member domain.MemberID, localRoot string, create bool) (*os.Root, error) {
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return nil, err
	}
	home, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	root, err := openProfileRoot(home, rootRel, create)
	_ = home.Close()
	return root, err
}

// ChownConfigPath hands one managed config path to the member-home owner.
// Callers must hold LockConfigRoot.
func (m *Manager) ChownConfigPath(member domain.MemberID, localRoot, rel string) error {
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return err
	}
	rel, err = configPath(rootRel, rel, false)
	if err != nil {
		return err
	}
	home, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	profileRoot, err := openProfileRoot(home, rootRel, false)
	if err != nil {
		return err
	}
	defer func() { _ = profileRoot.Close() }()
	parent, err := rootfs.OpenRoot(profileRoot, path.Dir(rel))
	if err != nil {
		return configPathError(err)
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(path.Base(rel))
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() && hasMultipleLinks(info) {
		return ErrConfigDenied
	}
	return chownLikeHomeAt(home, parent, path.Base(rel))
}

// ChownConfigTree hands all entries in a managed profile root to the member
// home owner. Callers must hold LockConfigRoot.
func (m *Manager) ChownConfigTree(member domain.MemberID, localRoot string) error {
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return err
	}
	home, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	if err = chownConfigDirs(home, rootRel); err != nil {
		return err
	}
	profileRoot, err := openProfileRoot(home, rootRel, false)
	if err != nil {
		return err
	}
	defer func() { _ = profileRoot.Close() }()
	return fs.WalkDir(profileRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && hasMultipleLinks(info) {
			return nil
		}
		return chownLikeHome(home, path.Join(rootRel, name))
	})
}

func chownConfigDirs(home *os.Root, rootRel string) error {
	current := home
	var owned *os.Root
	for _, seg := range strings.Split(rootRel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			if owned != nil {
				_ = owned.Close()
			}
			return ErrConfigDenied
		}
		child, err := rootfs.OpenRoot(current, seg)
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return configPathError(err)
		}
		if err := chownLikeHomeAt(home, current, seg); err != nil {
			_ = child.Close()
			if owned != nil {
				_ = owned.Close()
			}
			return err
		}
		if owned != nil {
			_ = owned.Close()
		}
		current, owned = child, child
	}
	if owned != nil {
		_ = owned.Close()
	}
	return nil
}

// ConfigTree lists the immediate safe children under localRoot/rel. Missing
// profile roots are an empty tree; unsafe entries are omitted rather than
// exposed to a browser.
func (m *Manager) ConfigTree(ctx context.Context, member domain.MemberID, harnessName, localRoot, rel string, deny []string) ([]ConfigTreeEntry, error) {
	unlock, err := m.LockConfigRoot(member, localRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()
	home, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = home.Close() }()
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return nil, err
	}
	requested, err := configPath(rootRel, rel, true)
	if err != nil {
		return nil, err
	}
	profileRoot, err := openProfileRoot(home, rootRel, false)
	if errors.Is(err, fs.ErrNotExist) {
		if requested == "." {
			return []ConfigTreeEntry{}, nil
		}
		return nil, fmt.Errorf("%w: directory is absent", ErrConfigNotFound)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = profileRoot.Close() }()
	if err = checkDir(profileRoot, requested); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: directory is absent", ErrConfigNotFound)
		}
		return nil, err
	}
	dir, err := rootfs.OpenRoot(profileRoot, requested)
	if err != nil {
		return nil, configPathError(err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return nil, err
	}
	out := make([]ConfigTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		child := entry.Name()
		childRel := child
		if requested != "." {
			childRel = path.Join(requested, child)
		}
		if child == ".aether-profile-ignore" || isDefaultIgnored(harnessName, childRel) || profilesvc.DeniedBasename(childRel, deny) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) || (info.Mode().IsRegular() && hasMultipleLinks(info)) {
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		}
		out = append(out, ConfigTreeEntry{Name: child, Kind: kind, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ConfigRead reads one UTF-8 editor file, returning a bounded read-only view
// for binary or oversized content rather than handing a browser an unbounded
// body. Revision is present only for a complete valid UTF-8 file.
func (m *Manager) ConfigRead(ctx context.Context, member domain.MemberID, harnessName, localRoot, rel string, deny []string) (ConfigRead, error) {
	unlock, err := m.LockConfigRoot(member, localRoot)
	if err != nil {
		return ConfigRead{}, err
	}
	defer unlock()
	home, err := m.openHome(member)
	if err != nil {
		return ConfigRead{}, err
	}
	defer func() { _ = home.Close() }()
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return ConfigRead{}, err
	}
	rel, err = configPath(rootRel, rel, false)
	if err != nil {
		return ConfigRead{}, err
	}
	if profilesvc.DeniedBasename(rel, deny) || isDefaultIgnored(harnessName, rel) {
		return ConfigRead{}, ErrConfigDenied
	}
	profileRoot, err := openProfileRoot(home, rootRel, false)
	if errors.Is(err, fs.ErrNotExist) {
		return ConfigRead{}, ErrConfigNotFound
	}
	if err != nil {
		return ConfigRead{}, err
	}
	defer func() { _ = profileRoot.Close() }()
	if err = checkParent(profileRoot, rel); err != nil {
		return ConfigRead{}, err
	}
	f, info, err := openConfigFile(profileRoot, rel)
	if err != nil {
		return ConfigRead{}, err
	}
	defer func() { _ = f.Close() }()
	if err = ctx.Err(); err != nil {
		return ConfigRead{}, err
	}
	truncated := info.Size() > ConfigMaxFileBytes
	content, err := io.ReadAll(io.LimitReader(f, ConfigMaxFileBytes))
	if err != nil {
		return ConfigRead{}, err
	}
	binary := isConfigBinary(content)
	out := ConfigRead{Content: content, Truncated: truncated, Binary: binary, Size: info.Size(), Writable: !truncated && !binary}
	if out.Writable {
		out.Revision = revision(content)
	}
	return out, nil
}

// ConfigWrite replaces one editor file after checking its exact-byte revision.
// An empty revision is accepted only when the file does not yet exist.
func (m *Manager) ConfigWrite(ctx context.Context, member domain.MemberID, harnessName, localRoot, rel string, content []byte, expected string, deny []string) (ConfigRead, error) {
	if len(content) > ConfigMaxFileBytes {
		return ConfigRead{}, ErrConfigTooLarge
	}
	if isConfigBinary(content) {
		return ConfigRead{}, ErrConfigBinary
	}
	unlock, err := m.LockConfigRoot(member, localRoot)
	if err != nil {
		return ConfigRead{}, err
	}
	defer unlock()
	home, err := m.openHome(member)
	if err != nil {
		return ConfigRead{}, err
	}
	defer func() { _ = home.Close() }()
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return ConfigRead{}, err
	}
	rel, err = configPath(rootRel, rel, false)
	if err != nil {
		return ConfigRead{}, err
	}
	if profilesvc.DeniedBasename(rel, deny) || isDefaultIgnored(harnessName, rel) {
		return ConfigRead{}, ErrConfigDenied
	}
	profileRoot, err := openProfileRoot(home, rootRel, expected == "")
	if errors.Is(err, fs.ErrNotExist) {
		if expected != "" {
			return ConfigRead{}, ErrConfigNotFound
		}
		return ConfigRead{}, err
	}
	if err != nil {
		return ConfigRead{}, err
	}
	defer func() { _ = profileRoot.Close() }()
	info, err := configTargetInfo(profileRoot, rel)
	exists := err == nil
	var mode os.FileMode = 0o644
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expected != "" {
			return ConfigRead{}, ErrConfigConflict
		}
	case err != nil:
		return ConfigRead{}, err
	case !info.Mode().IsRegular():
		return ConfigRead{}, ErrConfigDenied
	default:
		current, readErr := readConfigBytes(profileRoot, rel, ConfigMaxFileBytes)
		if readErr != nil {
			return ConfigRead{}, readErr
		}
		if isConfigBinary(current) {
			return ConfigRead{}, ErrConfigBinary
		}
		if expected == "" || expected != revision(current) {
			return ConfigRead{}, ErrConfigConflict
		}
		mode = info.Mode().Perm()
	}
	if err := ensureDirPathOwned(home, profileRoot, path.Dir(rel), true); err != nil {
		return ConfigRead{}, err
	}
	if err := atomicConfigWrite(home, profileRoot, rel, content, mode, expected, !exists); err != nil {
		return ConfigRead{}, err
	}
	return ConfigRead{Content: append([]byte(nil), content...), Size: int64(len(content)), Revision: revision(content), Writable: true}, nil
}

// ConfigImport validates every browser file and every existing destination
// before the first mutation. Credentials, runtime/history defaults, and
// scanner findings are reported as exclusions; arbitrary regular bytes are
// preserved under the import size caps.
func (m *Manager) ConfigImport(ctx context.Context, member domain.MemberID, harnessName, localRoot string, files []ConfigFile, deny []string) (ConfigImportResult, error) {
	if len(files) > ConfigImportMaxFiles {
		return ConfigImportResult{}, fmt.Errorf("%w: import contains too many files", ErrConfigTooLarge)
	}
	unlock, err := m.LockConfigRoot(member, localRoot)
	if err != nil {
		return ConfigImportResult{}, err
	}
	defer unlock()
	rootRel, err := configRootPath(localRoot)
	if err != nil {
		return ConfigImportResult{}, err
	}
	seen := make(map[string]struct{}, len(files))
	candidates := make([]ConfigFile, 0, len(files))
	result := ConfigImportResult{Excluded: []ConfigExcluded{}}
	var total int64
	for _, file := range files {
		if err = ctx.Err(); err != nil {
			return ConfigImportResult{}, err
		}
		rel, pathErr := configPath(rootRel, file.Path, false)
		if pathErr != nil {
			return ConfigImportResult{}, pathErr
		}
		if _, duplicate := seen[rel]; duplicate {
			return ConfigImportResult{}, fmt.Errorf("%w: duplicate path", ErrConfigDenied)
		}
		seen[rel] = struct{}{}
		if err = validateConfigMode(file.Mode); err != nil {
			return ConfigImportResult{}, err
		}
		if len(file.Content) > ConfigImportMaxFileBytes {
			return ConfigImportResult{}, ErrConfigTooLarge
		}
		total += int64(len(file.Content))
		if total > ConfigImportMaxBytes {
			return ConfigImportResult{}, ErrConfigTooLarge
		}
		if profilesvc.DeniedBasename(rel, deny) {
			result.Excluded = append(result.Excluded, ConfigExcluded{Path: rel, Reason: "credential", Detail: "credential file excluded"})
			continue
		}
		if rel == ".aether-profile-ignore" || isDefaultIgnored(harnessName, rel) {
			result.Excluded = append(result.Excluded, ConfigExcluded{Path: rel, Reason: "ignored", Detail: "runtime or history file excluded"})
			continue
		}
		if hits := secretscan.Scan(rel, file.Content); len(hits) != 0 {
			h := hits[0]
			result.Excluded = append(result.Excluded, ConfigExcluded{Path: rel, Reason: "secret", Detail: fmt.Sprintf("secret detected at %s (%s)", h.Location, h.Kind)})
			continue
		}
		mode := os.FileMode(file.Mode & 0o777)
		if mode == 0 {
			mode = 0o644
		}
		candidates = append(candidates, ConfigFile{Path: rel, Content: append([]byte(nil), file.Content...), Mode: uint32(mode.Perm())})
	}
	home, err := m.openHome(member)
	if err != nil {
		return ConfigImportResult{}, err
	}
	defer func() { _ = home.Close() }()
	if err = preflightConfigTargets(home, rootRel, candidates); err != nil {
		return ConfigImportResult{}, err
	}
	if len(candidates) != 0 {
		profileRoot, openErr := openProfileRoot(home, rootRel, true)
		if openErr != nil {
			return ConfigImportResult{}, openErr
		}
		defer func() { _ = profileRoot.Close() }()
		for _, file := range candidates {
			if err = ctx.Err(); err != nil {
				return ConfigImportResult{}, err
			}
			if err := ensureDirPathOwned(home, profileRoot, path.Dir(file.Path), true); err != nil {
				return ConfigImportResult{}, err
			}
			if err := atomicConfigWrite(home, profileRoot, file.Path, file.Content, os.FileMode(file.Mode&0o777), "", false); err != nil {
				return ConfigImportResult{}, err
			}
			result.Files++
			result.Bytes += int64(len(file.Content))
		}
	}
	sort.Slice(result.Excluded, func(i, j int) bool { return result.Excluded[i].Path < result.Excluded[j].Path })
	return result, nil
}

func preflightConfigTargets(home *os.Root, localRoot string, files []ConfigFile) error {
	candidates := make(map[string]struct{}, len(files))
	for _, file := range files {
		candidates[file.Path] = struct{}{}
	}
	for _, file := range files {
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if _, exists := candidates[parent]; exists {
				return fmt.Errorf("%w: file/directory path collision", ErrConfigDenied)
			}
		}
	}
	if err := checkDir(home, localRoot); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	profileRoot, err := openProfileRoot(home, localRoot, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = profileRoot.Close() }()
	for _, file := range files {
		if err := checkParent(profileRoot, file.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		info, err := configTargetInfo(profileRoot, file.Path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrConfigDenied
		}
	}
	return nil
}

func openProfileRoot(home *os.Root, localRoot string, create bool) (*os.Root, error) {
	current := home
	var owned *os.Root
	for _, seg := range strings.Split(localRoot, "/") {
		if seg == "" || seg == "." || seg == ".." {
			if owned != nil {
				_ = owned.Close()
			}
			return nil, ErrConfigDenied
		}
		child, err := rootfs.OpenRoot(current, seg)
		created := false
		if errors.Is(err, fs.ErrNotExist) && create {
			if mkdirErr := current.Mkdir(seg, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				if owned != nil {
					_ = owned.Close()
				}
				return nil, mkdirErr
			}
			child, err = rootfs.OpenRoot(current, seg)
			created = err == nil
		}
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return nil, configPathError(err)
		}
		if created {
			if err := chownLikeHomeAt(home, current, seg); err != nil {
				_ = child.Close()
				if owned != nil {
					_ = owned.Close()
				}
				return nil, err
			}
		}
		if owned != nil {
			_ = owned.Close()
		}
		current, owned = child, child
	}
	return owned, nil
}
func configRootPath(localRoot string) (string, error) {
	localRoot = harness.HomeRelative(localRoot)
	if localRoot == "" || strings.Contains(localRoot, "\\") || strings.ContainsRune(localRoot, 0) || path.IsAbs(localRoot) {
		return "", fmt.Errorf("%w: invalid profile root", ErrConfigDenied)
	}
	clean := path.Clean(localRoot)
	if clean != localRoot || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("%w: invalid profile root", ErrConfigDenied)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: invalid profile root", ErrConfigDenied)
		}
	}
	return clean, nil
}

func configPath(localRoot, raw string, allowRoot bool) (string, error) {
	if raw == localRoot {
		raw = "."
	} else if strings.HasPrefix(raw, localRoot+"/") {
		raw = strings.TrimPrefix(raw, localRoot+"/")
	}
	if (raw == "." || raw == "") && allowRoot {
		return ".", nil
	}
	if raw == "" || strings.Contains(raw, "\\") || strings.ContainsRune(raw, 0) || path.IsAbs(raw) {
		return "", fmt.Errorf("%w: invalid config path", ErrConfigDenied)
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: invalid config path", ErrConfigDenied)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: invalid config path", ErrConfigDenied)
		}
	}
	return clean, nil
}
func configPathError(err error) error {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrConfigDenied, err)
}

func checkParent(root *os.Root, full string) error { return ensureDirPath(root, path.Dir(full), false) }
func checkDir(root *os.Root, name string) error    { return ensureDirPath(root, name, false) }

func ensureDirPath(root *os.Root, name string, create bool) error {
	return ensureDirPathOwned(nil, root, name, create)
}

func ensureDirPathOwned(owner, root *os.Root, name string, create bool) error {
	clean := path.Clean(name)
	if clean == "." {
		return nil
	}
	current := root
	var owned *os.Root
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			if owned != nil {
				_ = owned.Close()
			}
			return fmt.Errorf("%w: invalid directory", ErrConfigDenied)
		}
		child, err := rootfs.OpenRoot(current, seg)
		created := false
		if errors.Is(err, fs.ErrNotExist) && create {
			if mkdirErr := current.Mkdir(seg, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				if owned != nil {
					_ = owned.Close()
				}
				return mkdirErr
			}
			child, err = rootfs.OpenRoot(current, seg)
			created = err == nil
		}
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return configPathError(err)
		}
		if created && owner != nil {
			if err := chownLikeHomeAt(owner, current, seg); err != nil {
				_ = child.Close()
				if owned != nil {
					_ = owned.Close()
				}
				return err
			}
		}
		if owned != nil {
			_ = owned.Close()
		}
		current, owned = child, child
	}
	if owned != nil {
		_ = owned.Close()
	}
	return nil
}

func configTargetInfo(root *os.Root, name string) (fs.FileInfo, error) {
	parentName := path.Dir(name)
	parent, err := rootfs.OpenRoot(root, parentName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, configPathError(err)
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(path.Base(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (info.Mode().IsRegular() && hasMultipleLinks(info)) {
		return nil, ErrConfigDenied
	}
	return info, nil
}

func openConfigFile(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	f, err := rootfs.Open(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, nil, configPathError(err)
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || hasMultipleLinks(opened) {
		_ = f.Close()
		return nil, nil, ErrConfigDenied
	}
	return f, opened, nil
}

func readConfigBytes(root *os.Root, name string, limit int64) ([]byte, error) {
	f, info, err := openConfigFile(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > limit {
		return nil, ErrConfigTooLarge
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

func atomicConfigWrite(owner, root *os.Root, name string, content []byte, mode os.FileMode, expected string, requireAbsent bool) error {
	parentName := path.Dir(name)
	parent := root
	var owned *os.Root
	if parentName != "." {
		var err error
		owned, err = rootfs.OpenRoot(root, parentName)
		if err != nil {
			return configPathError(err)
		}
		parent = owned
	}
	defer func() {
		if owned != nil {
			_ = owned.Close()
		}
	}()
	target := path.Base(name)
	for range 10 {
		tmp := ".aether-config-" + rand.Text()
		f, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err = f.Write(content); err == nil {
			err = chownFileLikeHome(owner, f)
		}
		if err == nil {
			err = f.Chmod(mode.Perm())
		}
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = parent.Remove(tmp)
			return err
		}
		switch {
		case expected != "":
			current, checkErr := readConfigBytes(parent, target, ConfigMaxFileBytes)
			if checkErr != nil || revision(current) != expected {
				_ = parent.Remove(tmp)
				return ErrConfigConflict
			}
		case requireAbsent:
			if _, checkErr := configTargetInfo(parent, target); checkErr == nil || !errors.Is(checkErr, fs.ErrNotExist) {
				_ = parent.Remove(tmp)
				return ErrConfigConflict
			}
		}
		if err := parent.Rename(tmp, target); err != nil {
			_ = parent.Remove(tmp)
			return err
		}
		return nil
	}
	return errors.New("config: could not stage file")
}

func validateConfigMode(mode uint32) error {
	typeBits := mode & 0o170000
	if typeBits != 0 && typeBits != 0o100000 {
		return ErrConfigDenied
	}
	return nil
}

func revision(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

var defaultIgnores = map[string][]string{
	"claude": {"projects", "shell-snapshots", "statsig", "todos", "file-history", "history.jsonl", "daemon"},
	"codex":  {"tmp", ".tmp", "sessions"},
}

func isDefaultIgnored(harnessName, rel string) bool {
	clean := path.Clean(rel)
	if clean == ".aether-profile-ignore" {
		return true
	}
	for _, ignored := range defaultIgnores[harnessName] {
		if clean == ignored || strings.HasPrefix(clean, ignored+"/") {
			return true
		}
	}
	return false
}
func isConfigBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
}
