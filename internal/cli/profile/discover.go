package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/harness"
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
	if err := statRoot(root); err != nil {
		return nil, err
	}
	var out []LocalFile
	err := walkRoot(root, prof, allowSet(root, allowSecret), func(f visited) error {
		switch f.Reason {
		case ExcludeSecret:
			return &DiscoverError{Path: f.Finding.Path, Location: f.Finding.Location, Message: f.Detail}
		case ExcludeSymlink:
			return &DiscoverError{Path: f.Rel, Message: f.Detail}
		case "":
			out = append(out, LocalFile{Path: f.Rel, AbsPath: f.Abs, Mode: f.Mode, Content: f.Content})
		}
		// Credential and ignored files are simply not pushed.
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// statRoot checks a profile root exists and is a directory before a walk.
func statRoot(root string) error {
	fi, err := os.Stat(root)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("profile root %s does not exist", root)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("profile root %s is not a directory", root)
	}
	return nil
}
