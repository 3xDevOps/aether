package profile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
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

// Put validates, stores, and points the member+harness head at the
// snapshot. Identical trees reuse digest and snapshot identity.
func (s *Service) Put(ctx context.Context, member, harnessName string, files []File) (domain.ProfileSnapshot, error) {
	prof, err := lookupProfile(harnessName)
	if err != nil {
		return domain.ProfileSnapshot{}, err
	}
	normalized, err := validateFiles(prof, files)
	if err != nil {
		return domain.ProfileSnapshot{}, err
	}
	digest := canonicalDigest(normalized)
	snap := domain.ProfileSnapshot{
		MemberID: domain.MemberID(member),
		Harness:  harnessName,
		Digest:   digest,
	}
	stored := toStoreFiles(normalized)
	if err := s.store.SaveProfileSnapshot(ctx, &snap, stored); err != nil {
		return domain.ProfileSnapshot{}, err
	}
	if err := s.store.PruneProfileSnapshots(ctx, snap.MemberID, snap.Harness, retainLatest); err != nil {
		return domain.ProfileSnapshot{}, err
	}
	return snap, nil
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
// Subsequent writes to destDir never mutate stored blobs.
func (s *Service) Materialize(ctx context.Context, id domain.ProfileSnapshotID, destDir string) error {
	if destDir == "" {
		return errors.New("profile: materialize: dest dir is required")
	}
	files, err := s.store.GetProfileFiles(ctx, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("profile: materialize: %w", err)
	}
	for _, f := range files {
		rel, err := safeRelPath(f.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("profile: materialize %s: %w", f.Path, err)
		}
		mode := os.FileMode(f.Mode & 0o777)
		if mode == 0 {
			mode = 0o644
		}
		content := append([]byte(nil), f.Content...)
		if err := os.WriteFile(target, content, mode); err != nil {
			return fmt.Errorf("profile: materialize %s: %w", f.Path, err)
		}
		if err := os.Chmod(target, mode); err != nil {
			return fmt.Errorf("profile: materialize %s: chmod: %w", f.Path, err)
		}
	}
	return nil
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
