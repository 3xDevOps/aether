package profile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
)

// LocalFile is one discovered profile file, relative to the harness LocalRoot.
type LocalFile struct {
	Path    string
	AbsPath string
	Mode    uint32
	Content []byte
}

// DiscoverError is a blocking discovery/scan failure (symlink escape or secret).
type DiscoverError struct {
	Path     string
	Location string
	Message  string
}

func (e *DiscoverError) Error() string {
	if e.Location != "" {
		return fmt.Sprintf("%s:%s: %s", e.Path, e.Location, e.Message)
	}
	if e.Path != "" {
		return e.Path + ": " + e.Message
	}
	return e.Message
}

// userHome is os.UserHomeDir, overridden in tests.
var userHome = os.UserHomeDir

// LocalDir returns the absolute host path of a harness LocalRoot.
func LocalDir(harnessName string) (string, harness.Profile, error) {
	p, ok := harness.Lookup(harnessName)
	if !ok {
		return "", harness.Profile{}, fmt.Errorf("unknown harness %q", harnessName)
	}
	if p.LocalRoot == "" {
		return "", p, fmt.Errorf("harness %q has no profile sync", harnessName)
	}
	home, err := userHome()
	if err != nil {
		return "", p, err
	}
	return filepath.Join(home, filepath.FromSlash(p.LocalRoot)), p, nil
}

// Discover walks harness LocalRoot, applying the denylist, gitignore-style
// .aether-profile-ignore, symlink-escape rejection, and content scanning.
// allowSecret names files (relative, basename, or absolute) that may pass
// scanner findings. Negation in the ignore file cannot re-include denied
// credential paths, extra credential names, symlink escapes, or findings
// that were not explicitly allowed.
func Discover(harnessName string, allowSecret []string) ([]LocalFile, error) {
	root, prof, err := LocalDir(harnessName)
	if err != nil {
		return nil, err
	}
	return discoverRoot(root, prof, allowSecret)
}

func discoverRoot(root string, prof harness.Profile, allowSecret []string) ([]LocalFile, error) {
	fi, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("profile root %s does not exist", root)
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("profile root %s is not a directory", root)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootResolved = root
	}
	ignoreData, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	var matcher *ignoreMatcher
	if err == nil {
		matcher = parseIgnoreFile(ignoreData)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	allowed := allowSet(root, allowSecret)
	var out []LocalFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
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
				return &DiscoverError{Path: relSlash, Message: "symlink escape: cannot resolve"}
			}
			if escapesRoot(rootResolved, target) {
				return &DiscoverError{Path: relSlash, Message: "symlink escape"}
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
			return nil
		}
		if matcher != nil && matcher.ignored(relSlash) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		findings := scanContent(relSlash, content)
		if len(findings) > 0 && !allowed[relSlash] && !allowed[path] {
			f := findings[0]
			return &DiscoverError{Path: f.Path, Location: f.Location, Message: "secret detected (" + f.Kind + ")"}
		}
		out = append(out, LocalFile{
			Path:    relSlash,
			AbsPath: path,
			Mode:    uint32(info.Mode().Perm()),
			Content: content,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
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
