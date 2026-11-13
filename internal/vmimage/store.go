// Package vmimage keeps the disks a session VM boots from: a read-only base
// disk per image, a copy-on-write root disk and a data disk per session, and
// a read-only helper disk per helper binary.
package vmimage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// rootSize is the size of every root filesystem. Disks are sparse and the
// root disk is thin, so only written data takes host space.
const rootSize = 64 << 30

// buildTimeout bounds a shared pull and build, which outlive the callers
// that started them.
const buildTimeout = time.Hour

// Disk formats, as Cloud Hypervisor's --disk image_type names them.
const (
	FormatRaw   = "raw"
	FormatQcow2 = "qcow2"
)

// Store is the image store under one directory. Only one Store may use a
// directory at a time.
type Store struct {
	dir    string
	kernel string
	docker DockerImages

	mu     sync.Mutex
	builds singleflight.Group
}

// DockerImages streams `docker save` of a local image. It supplies the
// images that exist only in Docker, the aether/ ones saved before sessions
// ran in VMs.
type DockerImages interface {
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
}

// Base is a read-only base disk and the image configuration a session runs
// with.
type Base struct {
	// ID is the image's config digest, or local:<hex> for a saved base.
	ID     string
	Path   string
	Config ImageConfig
}

// ImageConfig is the part of an OCI image configuration a session uses.
type ImageConfig struct {
	User       string            `json:"user,omitempty"`
	Env        []string          `json:"env,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// Disk is a disk file and its Cloud Hypervisor image_type.
type Disk struct {
	Path   string
	Format string
}

type record struct {
	ID     string      `json:"id"`
	Config ImageConfig `json:"config"`
}

// Open opens the store at dir, creating it, and removes what an interrupted
// build left behind. kernel is the guest kernel Kernel returns. docker may
// be nil.
func Open(dir, kernel string, docker DockerImages) (*Store, error) {
	s := &Store{dir: dir, kernel: kernel, docker: docker}
	if err := os.RemoveAll(s.tmpDir()); err != nil {
		return nil, fmt.Errorf("vmimage: clear %s: %w", s.tmpDir(), err)
	}
	for _, d := range []string{dir, s.tmpDir(), s.basesDir(), s.helpersDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("vmimage: create %s: %w", d, err)
		}
	}
	return s, nil
}

// Kernel returns the guest kernel's path.
func (s *Store) Kernel() (string, error) {
	info, err := os.Stat(s.kernel)
	if err != nil {
		return "", fmt.Errorf("vmimage: guest kernel: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("vmimage: guest kernel %s is not a regular file", s.kernel)
	}
	return s.kernel, nil
}

// BaseDisk returns the base disk of ref, building it on first use. A tag
// resolves locally once known, as in Docker. aether/ references are never
// pulled from a registry: they are saved bases or come from Docker.
// Concurrent first uses share one pull, which a cancelled caller leaves
// running for the others.
func (s *Store) BaseDisk(ctx context.Context, ref string) (Base, error) {
	if base, err := s.LookupBase(ref); !errors.Is(err, runtime.ErrNotFound) {
		return base, err
	}
	pulled := s.builds.DoChan("ref:"+ref, func() (any, error) {
		pullCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
		defer cancel()
		return nil, s.pull(pullCtx, ref)
	})
	select {
	case <-ctx.Done():
		return Base{}, fmt.Errorf("vmimage: base disk for %s: %w", ref, ctx.Err())
	case res := <-pulled:
		if res.Err != nil {
			return Base{}, res.Err
		}
	}
	return s.LookupBase(ref)
}

// pull resolves ref, builds its base disk unless one with the same config
// digest exists, and tags it ref.
func (s *Store) pull(ctx context.Context, ref string) error {
	img, cleanup, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	defer cleanup()
	digest, err := img.ConfigName()
	if err != nil {
		return fmt.Errorf("vmimage: image %s: %w", ref, err)
	}
	id := digest.String()
	if _, err, _ = s.builds.Do("id:"+id, func() (any, error) { return nil, s.build(ctx, img, id, ref) }); err != nil {
		return fmt.Errorf("vmimage: base disk for %s: %w", ref, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tagLocked(ref, id)
}

// LookupBase returns the base disk ref is tagged to, without building one;
// an unknown ref is runtime.ErrNotFound.
func (s *Store) LookupBase(ref string) (Base, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags, err := s.readTags()
	if err != nil {
		return Base{}, err
	}
	id, ok := tags[ref]
	if !ok {
		return Base{}, fmt.Errorf("vmimage: image %s: %w", ref, runtime.ErrNotFound)
	}
	return s.load(id)
}

// NewRootDisk creates a qcow2 root disk at path over base, replacing any
// disk already there. Its backing file is a hard link to the base disk named
// path.<base id>.base, so the base stays until the link is deleted with the
// session's files; path must be on the store's filesystem. A failed rebuild
// leaves the previous root disk and its base as they were.
func (s *Store) NewRootDisk(ctx context.Context, base Base, path string) (disk Disk, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return Disk{}, fmt.Errorf("vmimage: root disk: %w", err)
	}
	defer func() {
		if pruneErr := prunePins(path); err == nil && pruneErr != nil {
			err = pruneErr
		}
	}()
	pin := path + "." + dirName(base.ID) + ".base"
	if err = s.pin(base.Path, pin); err != nil {
		return Disk{}, err
	}
	tmp := path + ".tmp"
	if err = removeIfExists(tmp); err != nil {
		return Disk{}, err
	}
	if err = run(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "raw", "-b", pin, tmp); err != nil {
		return Disk{}, err
	}
	if err = os.Rename(tmp, path); err != nil {
		return Disk{}, fmt.Errorf("vmimage: root disk %s: %w", path, err)
	}
	return Disk{Path: path, Format: FormatQcow2}, nil
}

// prunePins removes the base links next to the root disk at path that it
// does not back onto.
func prunePins(path string) error {
	keep, err := backingFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("vmimage: root disk %s: %w", path, err)
	}
	prefix := filepath.Base(path) + "."
	var errs []error
	for _, e := range entries {
		name := filepath.Join(filepath.Dir(path), e.Name())
		pin := strings.HasSuffix(e.Name(), ".base") || strings.HasSuffix(e.Name(), ".base.tmp")
		if strings.HasPrefix(e.Name(), prefix) && pin && name != keep {
			errs = append(errs, removeIfExists(name))
		}
	}
	return errors.Join(errs...)
}

// backingFile reads the backing file name from a qcow2 header.
func backingFile(disk string) (string, error) {
	f, err := os.Open(disk)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var hdr [20]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return "", fmt.Errorf("vmimage: read %s: %w", disk, err)
	}
	if string(hdr[:4]) != "QFI\xfb" {
		return "", fmt.Errorf("vmimage: %s is not a qcow2 disk", disk)
	}
	offset, size := binary.BigEndian.Uint64(hdr[8:16]), binary.BigEndian.Uint32(hdr[16:20])
	if offset == 0 || size == 0 || size > 1023 {
		return "", fmt.Errorf("vmimage: %s has no backing file", disk)
	}
	name := make([]byte, size)
	if _, err := f.ReadAt(name, int64(offset)); err != nil {
		return "", fmt.Errorf("vmimage: read %s: %w", disk, err)
	}
	return string(name), nil
}

// NewDataDisk creates an empty ext4 data disk of size bytes at path, labelled
// aether-data. It refuses to replace a disk already there.
func (s *Store) NewDataDisk(ctx context.Context, path string, size int64) (Disk, error) {
	tmp := path + ".tmp"
	if err := removeIfExists(tmp); err != nil {
		return Disk{}, err
	}
	if err := mkfs(ctx, tmp, size, "aether-data", ""); err != nil {
		return Disk{}, errors.Join(err, removeIfExists(tmp))
	}
	err := os.Link(tmp, path)
	if err = errors.Join(err, removeIfExists(tmp)); err != nil {
		return Disk{}, fmt.Errorf("vmimage: data disk %s: %w", path, err)
	}
	return Disk{Path: path, Format: FormatRaw}, nil
}

// SaveBase saves a root disk as a new base disk tagged ref. The base keeps
// the image configuration of the base the root disk was made from, with
// labels replacing those of the same keys. The VM must be stopped.
func (s *Store) SaveBase(ctx context.Context, rootDisk, ref string, labels map[string]string) (Base, error) {
	pin, err := backingFile(rootDisk)
	if err != nil {
		return Base{}, err
	}
	parent, err := s.baseOf(pin)
	if err != nil {
		return Base{}, err
	}
	work, err := os.MkdirTemp(s.tmpDir(), "save-")
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: save %s: %w", rootDisk, err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err = run(ctx, "qemu-img", "convert", "-f", "qcow2", "-O", "raw", rootDisk, filepath.Join(work, "disk.raw")); err != nil {
		return Base{}, err
	}
	cfg := parent.Config
	cfg.Labels = maps.Clone(cfg.Labels)
	if cfg.Labels == nil && len(labels) > 0 {
		cfg.Labels = map[string]string{}
	}
	maps.Copy(cfg.Labels, labels)
	id, err := newLocalID()
	if err != nil {
		return Base{}, err
	}
	return s.commit(work, record{ID: id, Config: cfg}, ref)
}

// Tag points target at the base source is tagged to.
func (s *Store) Tag(source, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags, err := s.readTags()
	if err != nil {
		return err
	}
	id, ok := tags[source]
	if !ok {
		return fmt.Errorf("vmimage: image %s: %w", source, runtime.ErrNotFound)
	}
	return s.tagLocked(target, id)
}

// Untag removes ref, then deletes the base disks nothing uses any more. An
// unknown ref is not an error.
func (s *Store) Untag(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags, err := s.readTags()
	if err != nil {
		return err
	}
	if _, ok := tags[ref]; ok {
		delete(tags, ref)
		if err := s.writeTags(tags); err != nil {
			return err
		}
	}
	return s.sweepLocked()
}

// Tags returns every tag of repo, as repo:tag.
func (s *Store) Tags(repo string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags, err := s.readTags()
	if err != nil {
		return nil, err
	}
	var out []string
	for ref := range tags {
		if strings.HasPrefix(ref, repo+":") {
			out = append(out, ref)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Sweep deletes base disks that have no tag and no root disk, and helper
// disks no session links to.
func (s *Store) Sweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked()
}

func (s *Store) sweepLocked() error {
	tags, err := s.readTags()
	if err != nil {
		return err
	}
	tagged := map[string]bool{}
	for _, id := range tags {
		tagged[dirName(id)] = true
	}
	var errs []error
	for _, d := range []struct{ dir, file string }{{s.basesDir(), "disk.raw"}, {s.helpersDir(), "helper.raw"}} {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			return fmt.Errorf("vmimage: sweep: %w", err)
		}
		for _, e := range entries {
			if tagged[e.Name()] && d.dir == s.basesDir() {
				continue
			}
			dir := filepath.Join(d.dir, e.Name())
			linked, err := linkedElsewhere(filepath.Join(dir, d.file))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !linked {
				errs = append(errs, os.RemoveAll(dir))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("vmimage: sweep: %w", err)
	}
	return nil
}

// pin hard-links target to link under the store lock, so a sweep cannot
// delete target first.
func (s *Store) pin(target, link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinLocked(target, link)
}

// pinLocked replaces link with a hard link to target in one rename, so a
// disk already backing onto link never sees it missing.
func (s *Store) pinLocked(target, link string) error {
	// rename(2) between two links of one file does nothing and leaves the
	// temporary link behind.
	if sameFile(target, link) {
		return nil
	}
	tmp := link + ".tmp"
	if err := removeIfExists(tmp); err != nil {
		return err
	}
	if err := os.Link(target, tmp); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, unix.EXDEV) {
			return fmt.Errorf("vmimage: %s must be on the same filesystem as the image store %s", link, s.dir)
		}
		return fmt.Errorf("vmimage: link %s: %w", link, err)
	}
	if err := os.Rename(tmp, link); err != nil {
		return errors.Join(fmt.Errorf("vmimage: link %s: %w", link, err), removeIfExists(tmp))
	}
	return nil
}

// baseOf finds the base disk that pin is a hard link of.
func (s *Store) baseOf(pin string) (Base, error) {
	want, err := os.Stat(pin)
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk of root disk: %w", err)
	}
	entries, err := os.ReadDir(s.basesDir())
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk of root disk: %w", err)
	}
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(s.basesDir(), e.Name(), "disk.raw"))
		if err == nil && os.SameFile(info, want) {
			return s.loadDir(e.Name())
		}
	}
	return Base{}, fmt.Errorf("vmimage: %s is not linked to a base disk in %s", pin, s.basesDir())
}

// commit moves a finished work directory holding disk.raw into the bases
// and tags it ref, under the store lock so a sweep cannot remove it untagged.
func (s *Store) commit(work string, rec record, ref string) (Base, error) {
	disk := filepath.Join(work, "disk.raw")
	if err := os.Chmod(disk, 0o444); err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(work, "image.json"), data, 0o600); err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk record: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dest := filepath.Join(s.basesDir(), dirName(rec.ID))
	if err := os.Rename(work, dest); err != nil {
		return Base{}, fmt.Errorf("vmimage: store base disk: %w", err)
	}
	if err := s.tagLocked(ref, rec.ID); err != nil {
		return Base{}, err
	}
	return Base{ID: rec.ID, Path: filepath.Join(dest, "disk.raw"), Config: rec.Config}, nil
}

func (s *Store) load(id string) (Base, error) {
	return s.loadDir(dirName(id))
}

func (s *Store) loadDir(name string) (Base, error) {
	dir := filepath.Join(s.basesDir(), name)
	data, err := os.ReadFile(filepath.Join(dir, "image.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Base{}, fmt.Errorf("vmimage: base disk %s: %w", dir, runtime.ErrNotFound)
	}
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk record: %w", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Base{}, fmt.Errorf("vmimage: base disk record %s: %w", dir, err)
	}
	return Base{ID: rec.ID, Path: filepath.Join(dir, "disk.raw"), Config: rec.Config}, nil
}

func (s *Store) readTags() (map[string]string, error) {
	data, err := os.ReadFile(s.tagsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vmimage: read tags: %w", err)
	}
	tags := map[string]string{}
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("vmimage: read tags %s: %w", s.tagsPath(), err)
	}
	return tags, nil
}

func (s *Store) tagLocked(ref, id string) error {
	tags, err := s.readTags()
	if err != nil {
		return err
	}
	tags[ref] = id
	return s.writeTags(tags)
}

func (s *Store) writeTags(tags map[string]string) error {
	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return fmt.Errorf("vmimage: write tags: %w", err)
	}
	tmp := s.tagsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("vmimage: write tags: %w", err)
	}
	if err := os.Rename(tmp, s.tagsPath()); err != nil {
		return fmt.Errorf("vmimage: write tags: %w", err)
	}
	return nil
}

func (s *Store) tmpDir() string     { return filepath.Join(s.dir, "tmp") }
func (s *Store) basesDir() string   { return filepath.Join(s.dir, "bases") }
func (s *Store) helpersDir() string { return filepath.Join(s.dir, "helpers") }
func (s *Store) tagsPath() string   { return filepath.Join(s.dir, "tags.json") }

func dirName(id string) string { return strings.ReplaceAll(id, ":", "-") }

func newLocalID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("vmimage: new base id: %w", err)
	}
	return "local:" + hex.EncodeToString(b), nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// linkedElsewhere reports whether a session still hard-links file.
func linkedElsewhere(file string) (bool, error) {
	info, err := os.Stat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("vmimage: no link count for %s", file)
	}
	return st.Nlink > 1, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("vmimage: remove %s: %w", path, err)
	}
	return nil
}
