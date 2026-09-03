package profile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
)

// Why a file was left out of a push. Discover turns secret and symlink
// into a blocking error; Inventory reports all four to the user.
const (
	// ExcludeCredential is a basename on the harness credential denylist.
	ExcludeCredential = "credential"
	// ExcludeSecret is a content-scanner finding.
	ExcludeSecret = "secret"
	// ExcludeIgnored is a .aether-profile-ignore match.
	ExcludeIgnored = "ignored"
	// ExcludeSymlink is a symlink pointing outside the profile root.
	ExcludeSymlink = "symlink"
)

// visited is one file the walk classified. Reason is empty for a file
// that would be pushed; otherwise it is one of the Exclude constants and
// Detail says which rule fired.
type visited struct {
	Rel     string
	Abs     string
	Mode    uint32
	Size    int64
	Content []byte
	Reason  string
	Detail  string
	// Finding carries the scanner hit behind an ExcludeSecret, so callers
	// can name the file and the location.
	Finding Finding
}

// walkRoot walks a harness profile root once, applying the same rules a
// push applies - credential denylist, .aether-profile-ignore, symlink
// escape rejection, content scan - and hands every file to visit with its
// verdict. allowed names files whose scanner findings pass (the CLI's
// --allow-secret); Inventory passes none. A visit error aborts the walk
// and is returned unchanged, which is how Discover stops at the first
// blocking finding.
func walkRoot(root string, prof harness.Profile, allowed map[string]bool, visit func(visited) error) error {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootResolved = root
	}
	ignoreData, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	var matcher *ignoreMatcher
	switch {
	case err == nil:
		matcher = parseIgnoreFile(ignoreData)
	case !os.IsNotExist(err):
		return err
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return visit(visited{Rel: relSlash, Abs: path, Reason: ExcludeSymlink, Detail: "symlink escape: cannot resolve"})
			}
			if escapesRoot(rootResolved, target) {
				return visit(visited{Rel: relSlash, Abs: path, Reason: ExcludeSymlink, Detail: "symlink escape"})
			}
			return nil
		}
		if d.IsDir() {
			if matcher != nil && matcher.ignored(relSlash) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Base(relSlash) == IgnoreFileName {
			return nil
		}
		if profilesvc.DeniedBasename(relSlash, prof.DenyNames) {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeCredential, Detail: "credential file excluded for " + prof.Name})
		}
		if matcher != nil && matcher.ignored(relSlash) {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeIgnored, Detail: IgnoreFileName})
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file := visited{
			Rel:     relSlash,
			Abs:     path,
			Mode:    uint32(info.Mode().Perm()),
			Size:    info.Size(),
			Content: content,
		}
		if findings := scanContent(relSlash, content); len(findings) > 0 && !allowed[relSlash] && !allowed[path] {
			file.Content = nil
			file.Reason = ExcludeSecret
			file.Finding = findings[0]
			file.Detail = "secret detected (" + findings[0].Kind + ")"
		}
		return visit(file)
	})
}

func escapesRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(rel)
	return rel == ".." || strings.HasPrefix(rel, "../")
}

func allowSet(root string, allowSecret []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range allowSecret {
		if a == "" {
			continue
		}
		out[filepath.ToSlash(a)] = true
		abs := a
		if !filepath.IsAbs(a) {
			abs = filepath.Join(root, a)
		}
		if resolved, err := filepath.Abs(abs); err == nil {
			out[resolved] = true
			if rel, err := filepath.Rel(root, resolved); err == nil {
				out[filepath.ToSlash(rel)] = true
			}
		}
	}
	return out
}
