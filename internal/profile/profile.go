package profile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/rootfs"
	"github.com/3xDevOps/Aether/internal/store"
)

// MaxFileBytes and MaxTotalBytes are the size caps a push must satisfy.
// They are exported so a client can apply the same numbers before
// uploading anything: a preview that promised a file this rejects would
// be a promise the server breaks.
const (
	MaxFileBytes  = 1 << 20  // 1 MiB
	MaxTotalBytes = 20 << 20 // 20 MiB
)

const retainLatest = 10

// unix file-type bits a client might send as st_mode.
const (
	sIFMT  = 0o170000
	sIFREG = 0o100000
	sIFLNK = 0o120000
	sIFDIR = 0o040000
)

var (
	// ErrNotFound is returned when a snapshot or head does not exist.
	ErrNotFound = store.ErrNotFound
	// ErrDenied is returned when a path, basename, or mode is rejected.
	ErrDenied = errors.New("profile: denied")
	// ErrTooLarge is returned when a file or the tree exceeds the size cap.
	ErrTooLarge = errors.New("profile: too large")
)

// extraDeniedNames are token/credential basenames the server always
// refuses, even if a harness DenyNames list omitted them. Not a scanner.
var extraDeniedNames = []string{
	".credentials.json",
	"credentials.json",
	"auth.json",
	"keychain",
}

// File is one path in a profile snapshot. Path is slash-separated and
// relative to the harness LocalRoot.
type File struct {
	Path    string
	Mode    uint32
	Content []byte
}

// Service is the transport-agnostic profile snapshot API. A Bus is not
// wired here: Event requires WorkspaceID, and Put/Rollback have none, so
// they skip publish rather than inventing a workspace.
type Service struct {
	store store.Store
}

// New constructs a Service backed by st. Snapshot bytes live in the store
// as content-addressed blobs.
func New(st store.Store) (*Service, error) {
	if st == nil {
		return nil, errors.New("profile: store is required")
	}
	return &Service{store: st}, nil
}

// Put validates, stores, publishes, and prunes a snapshot. Identical trees
// reuse digest and snapshot identity.
func (s *Service) Put(ctx context.Context, member, harnessName string, files []File) (domain.ProfileSnapshot, error) {
	snap, stored, err := s.prepareSnapshot(member, harnessName, files)
	if err != nil {
		return domain.ProfileSnapshot{}, err
	}
	if err := s.store.SaveProfileSnapshot(ctx, &snap, stored); err != nil {
		return domain.ProfileSnapshot{}, err
	}
	if err := s.store.PruneProfileSnapshots(ctx, snap.MemberID, snap.Harness, retainLatest); err != nil {
		return domain.ProfileSnapshot{}, err
	}
	return snap, nil
}

// Stage validates and stores a snapshot without publishing it as the
// member+harness head. Publish must follow a successful materialization.
func (s *Service) Stage(ctx context.Context, member, harnessName string, files []File) (domain.ProfileSnapshot, error) {
	snap, stored, err := s.prepareSnapshot(member, harnessName, files)
	if err != nil {
		return domain.ProfileSnapshot{}, err
	}
	if err := s.store.SaveProfileSnapshotStaged(ctx, &snap, stored); err != nil {
		return domain.ProfileSnapshot{}, err
	}
	return snap, nil
}

// Publish makes a staged snapshot the current head and then prunes old
// snapshots while retaining rollback history.
func (s *Service) Publish(ctx context.Context, snap domain.ProfileSnapshot) error {
	if err := s.store.SetProfileHead(ctx, snap.MemberID, snap.Harness, snap.ID); err != nil {
		return err
	}
	return s.store.PruneProfileSnapshots(ctx, snap.MemberID, snap.Harness, retainLatest)
}

func (s *Service) prepareSnapshot(member, harnessName string, files []File) (domain.ProfileSnapshot, []store.ProfileFile, error) {
	prof, err := lookupProfile(harnessName)
	if err != nil {
		return domain.ProfileSnapshot{}, nil, err
	}
	normalized, err := validateFiles(prof, files)
	if err != nil {
		return domain.ProfileSnapshot{}, nil, err
	}
	snap := domain.ProfileSnapshot{
		MemberID: domain.MemberID(member),
		Harness:  harnessName,
		Digest:   canonicalDigest(normalized),
	}
	return snap, toStoreFiles(normalized), nil
}

// Get returns a snapshot and a defensive copy of its files.
func (s *Service) Get(ctx context.Context, id domain.ProfileSnapshotID) (domain.ProfileSnapshot, []File, error) {
	snap, err := s.store.GetProfileSnapshot(ctx, id)
	if err != nil {
		return domain.ProfileSnapshot{}, nil, err
	}
	stored, err := s.store.GetProfileFiles(ctx, id)
	if err != nil {
		return domain.ProfileSnapshot{}, nil, err
	}
	return *snap, fromStoreFiles(stored), nil
}

// Latest returns the head snapshot for member+harness, or ErrNotFound.
func (s *Service) Latest(ctx context.Context, member, harnessName string) (domain.ProfileSnapshot, error) {
	snap, err := s.store.GetProfileHead(ctx, domain.MemberID(member), harnessName)
	if err != nil {
		return domain.ProfileSnapshot{}, err
	}
	return *snap, nil
}

// List returns snapshots for member+harness, newest first.
func (s *Service) List(ctx context.Context, member, harnessName string) ([]domain.ProfileSnapshot, error) {
	rows, err := s.store.ListProfileSnapshots(ctx, domain.MemberID(member), harnessName)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ProfileSnapshot, len(rows))
	for i, r := range rows {
		out[i] = *r
	}
	return out, nil
}

// Rollback points the head at an existing snapshot without deleting any.
func (s *Service) Rollback(ctx context.Context, member, harnessName string, id domain.ProfileSnapshotID) error {
	snap, err := s.store.GetProfileSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if snap.MemberID != domain.MemberID(member) || snap.Harness != harnessName {
		return fmt.Errorf("%w: snapshot %s is not %s/%s", ErrDenied, id, member, harnessName)
	}
	return s.store.SetProfileHead(ctx, domain.MemberID(member), harnessName, id)
}

// PinRun records snapshot id on the run row.
func (s *Service) PinRun(ctx context.Context, runID domain.RunID, id domain.ProfileSnapshotID) error {
	if id == "" {
		return fmt.Errorf("profile: pin: empty snapshot id")
	}
	return s.store.SetRunProfileSnapshot(ctx, runID, id)
}

// Materialize writes a writable copy of the snapshot into destDir.
func (s *Service) Materialize(ctx context.Context, id domain.ProfileSnapshotID, destDir string) error {
	if destDir == "" {
		return errors.New("profile: materialize: dest dir is required")
	}
	root, err := openMaterializeRoot(destDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return s.MaterializeRoot(ctx, id, root)
}

// MaterializeRoot applies a snapshot through an already-open destination
// descriptor. The descriptor must be rooted at the member's profile root.
func (s *Service) MaterializeRoot(ctx context.Context, id domain.ProfileSnapshotID, root *os.Root) error {
	if root == nil {
		return errors.New("profile: materialize: destination root is required")
	}
	files, err := s.store.GetProfileFiles(ctx, id)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		rel, err := safeRelPath(f.Path)
		if err != nil {
			return err
		}
		if _, dup := seen[rel]; dup {
			return fmt.Errorf("%w: duplicate materialized path %s", ErrDenied, rel)
		}
		seen[rel] = struct{}{}
		paths = append(paths, rel)
	}
	for _, rel := range paths {
		for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
			if _, exists := seen[parent]; exists {
				return fmt.Errorf("%w: file/directory path collision", ErrDenied)
			}
		}
	}
	for _, rel := range paths {
		if err := materializeCheckParent(root, rel); err != nil {
			return err
		}
		info, err := materializeTargetInfo(root, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: unsafe destination path", ErrDenied)
		}
	}
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := os.FileMode(f.Mode & 0o777)
		if mode == 0 {
			mode = 0o644
		}
		if err := materializeEnsureParent(root, paths[i]); err != nil {
			return err
		}
		if err := materializeAtomicWrite(root, paths[i], f.Content, mode); err != nil {
			return fmt.Errorf("profile: materialize %s: %w", f.Path, err)
		}
	}
	return nil
}

func openMaterializeRoot(destDir string) (*os.Root, error) {
	clean := filepath.Clean(destDir)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, fmt.Errorf("profile: materialize destination: %w", err)
	}
	volume := filepath.VolumeName(abs)
	rootName := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(abs, rootName)
	parts := strings.Split(filepath.ToSlash(relative), "/")
	base, err := os.OpenRoot(rootName)
	if err != nil {
		return nil, fmt.Errorf("profile: materialize: open destination parent: %w", err)
	}
	current := base
	var owned *os.Root
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		child, openErr := rootfs.OpenRoot(current, seg)
		if errors.Is(openErr, fs.ErrNotExist) {
			if mkdirErr := current.Mkdir(seg, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				if owned != nil {
					_ = owned.Close()
				}
				_ = base.Close()
				return nil, fmt.Errorf("profile: materialize destination: %w", mkdirErr)
			}
			child, openErr = rootfs.OpenRoot(current, seg)
		}
		if openErr != nil {
			if owned != nil {
				_ = owned.Close()
			}
			_ = base.Close()
			return nil, fmt.Errorf("profile: materialize destination: %w", materializePathError(openErr))
		}
		if owned != nil {
			_ = owned.Close()
		}
		current, owned = child, child
	}
	if owned == nil {
		_ = base.Close()
		return nil, fmt.Errorf("%w: destination must not be the filesystem root", ErrDenied)
	}
	_ = base.Close()
	return owned, nil
}
func materializePathError(err error) error {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrDenied, err)
}

func materializeTargetInfo(root *os.Root, rel string) (fs.FileInfo, error) {
	parentName := path.Dir(rel)
	parent, err := rootfs.OpenRoot(root, parentName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, materializePathError(err)
	}
	defer func() { _ = parent.Close() }()
	return parent.Lstat(path.Base(rel))
}

func materializeCheckParent(root *os.Root, rel string) error {
	parent := path.Dir(rel)
	if parent == "." {
		return nil
	}
	current := root
	var owned *os.Root
	for _, seg := range strings.Split(parent, "/") {
		child, err := rootfs.OpenRoot(current, seg)
		if errors.Is(err, fs.ErrNotExist) {
			if owned != nil {
				_ = owned.Close()
			}
			return nil
		}
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return materializePathError(err)
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

func materializeEnsureParent(root *os.Root, rel string) error {
	parent := path.Dir(rel)
	if parent == "." {
		return nil
	}
	current := root
	var owned *os.Root
	for _, seg := range strings.Split(parent, "/") {
		child, err := rootfs.OpenRoot(current, seg)
		if errors.Is(err, fs.ErrNotExist) {
			if mkdirErr := current.Mkdir(seg, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				if owned != nil {
					_ = owned.Close()
				}
				return mkdirErr
			}
			child, err = rootfs.OpenRoot(current, seg)
		}
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return materializePathError(err)
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

func materializeAtomicWrite(root *os.Root, rel string, content []byte, mode os.FileMode) error {
	parentName := path.Dir(rel)
	parent := root
	var owned *os.Root
	if parentName != "." {
		var err error
		owned, err = rootfs.OpenRoot(root, parentName)
		if err != nil {
			return materializePathError(err)
		}
		parent = owned
	}
	defer func() {
		if owned != nil {
			_ = owned.Close()
		}
	}()
	target := path.Base(rel)
	for range 10 {
		tmp := ".aether-profile-" + rand.Text()
		f, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err = f.Write(content); err == nil {
			err = f.Sync()
		}
		targetInfo, statErr := parent.Lstat(target)
		if errors.Is(statErr, fs.ErrNotExist) {
			targetInfo = nil
			statErr = nil
		}
		if err == nil {
			err = statErr
		}
		if err == nil {
			err = materializeOwner(f, parent, targetInfo)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = parent.Remove(tmp)
			return err
		}
		if err := parent.Rename(tmp, target); err != nil {
			_ = parent.Remove(tmp)
			return err
		}
		return nil
	}
	return errors.New("profile: materialize: could not stage file")
}

func lookupProfile(name string) (harness.Profile, error) {
	p, ok := harness.Lookup(name)
	if !ok {
		return harness.Profile{}, fmt.Errorf("%w: unknown harness %q", ErrDenied, name)
	}
	if p.LocalRoot == "" {
		return harness.Profile{}, fmt.Errorf("%w: harness %q has no profile sync", ErrDenied, name)
	}
	return p, nil
}

func validateFiles(prof harness.Profile, files []File) ([]File, error) {
	seen := make(map[string]struct{}, len(files))
	out := make([]File, 0, len(files))
	var total int
	for _, f := range files {
		rel, err := normalizePutPath(prof.LocalRoot, f.Path)
		if err != nil {
			return nil, err
		}
		if err := rejectMode(f.Mode); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrDenied, rel, err)
		}
		if deniedBasename(rel, prof.DenyNames) {
			return nil, fmt.Errorf("%w: %s is a credential/token basename", ErrDenied, path.Base(rel))
		}
		n := len(f.Content)
		if n > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes (max %d)", ErrTooLarge, rel, n, MaxFileBytes)
		}
		total += n
		if total > MaxTotalBytes {
			return nil, fmt.Errorf("%w: snapshot exceeds %d bytes", ErrTooLarge, MaxTotalBytes)
		}
		if _, dup := seen[rel]; dup {
			return nil, fmt.Errorf("%w: duplicate path %s", ErrDenied, rel)
		}
		seen[rel] = struct{}{}
		content := append([]byte(nil), f.Content...)
		out = append(out, File{Path: rel, Mode: f.Mode, Content: content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func normalizePutPath(localRoot, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: empty path", ErrDenied)
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: path %q contains a backslash", ErrDenied, raw)
	}
	if strings.HasPrefix(raw, "/") || path.IsAbs(raw) {
		return "", fmt.Errorf("%w: path %q is absolute", ErrDenied, raw)
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: path %q is not a clean relative path", ErrDenied, raw)
		}
	}
	cleaned := path.Clean(raw)
	if cleaned != raw {
		return "", fmt.Errorf("%w: path %q is not a clean relative path", ErrDenied, raw)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: path %q escapes the profile root", ErrDenied, raw)
	}
	root := path.Clean(localRoot)
	if cleaned == root {
		return "", fmt.Errorf("%w: path %q is the profile root, not a file", ErrDenied, raw)
	}
	if root != "." && strings.HasPrefix(cleaned, root+"/") {
		cleaned = strings.TrimPrefix(cleaned, root+"/")
	}
	if cleaned == "" {
		return "", fmt.Errorf("%w: empty path", ErrDenied)
	}
	return cleaned, nil
}

func safeRelPath(p string) (string, error) {
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: stored path %q is invalid", ErrDenied, p)
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != p {
		return "", fmt.Errorf("%w: stored path %q is invalid", ErrDenied, p)
	}
	return cleaned, nil
}

func rejectMode(mode uint32) error {
	if os.FileMode(mode)&os.ModeSymlink != 0 {
		return errors.New("symlink")
	}
	if os.FileMode(mode)&os.ModeType != 0 {
		return errors.New("non-regular file")
	}
	switch mode & sIFMT {
	case 0, sIFREG:
		return nil
	case sIFLNK:
		return errors.New("symlink")
	case sIFDIR:
		return errors.New("directory")
	default:
		return errors.New("non-regular file")
	}
}

// DeniedBasename reports whether rel's basename is a credential or token
// name the profile service always refuses: harness DenyNames, extra
// credential names, or *.pem. Used by the client denylist so it matches
// the server.
func DeniedBasename(rel string, harnessDeny []string) bool {
	return deniedBasename(rel, harnessDeny)
}

func deniedBasename(rel string, harnessDeny []string) bool {
	base := path.Base(rel)
	if base == "." || base == "/" {
		return true
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".pem") {
		return true
	}
	for _, n := range extraDeniedNames {
		if base == n || lower == strings.ToLower(n) {
			return true
		}
	}
	for _, n := range harnessDeny {
		if base == n || lower == strings.ToLower(n) {
			return true
		}
	}
	return false
}

// canonicalDigest is sha256 over sorted path records: path, mode, content sha256.
func canonicalDigest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		_, _ = fmt.Fprintf(h, "%s %08x %x\n", f.Path, f.Mode, sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func toStoreFiles(files []File) []store.ProfileFile {
	out := make([]store.ProfileFile, len(files))
	for i, f := range files {
		out[i] = store.ProfileFile{Path: f.Path, Mode: f.Mode, Content: f.Content}
	}
	return out
}

func fromStoreFiles(files []store.ProfileFile) []File {
	out := make([]File, len(files))
	for i, f := range files {
		out[i] = File{Path: f.Path, Mode: f.Mode, Content: append([]byte(nil), f.Content...)}
	}
	return out
}
