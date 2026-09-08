package profile

import (
	"context"
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
// that were not explicitly allowed. A scanner finding drops the file it
// names and is reported as an exclusion; it never refuses the walk.
func Discover(ctx context.Context, harnessName string, allowSecret []string) ([]LocalFile, error) {
	files, _, err := DiscoverFiles(ctx, harnessName, allowSecret)
	return files, err
}

// DiscoverFiles is Discover plus the entries it left out: the ones the
// size caps dropped, symlinks pointing out of the profile root, and
// scanner findings. Those are the exclusions a caller has to be able to
// mention - a credential or an ignored file is excluded by a rule the
// user wrote or asked for, but a file dropped for its size, a link the
// walk would not follow, or one the scanner flagged is one they would
// otherwise expect to find on the server. Callers with somewhere to
// print report them; the daemon, which pushes unattended, logs them.
func DiscoverFiles(ctx context.Context, harnessName string, allowSecret []string) ([]LocalFile, []Exclusion, error) {
	root, prof, err := LocalDir(harnessName)
	if err != nil {
		return nil, nil, err
	}
	return discoverRoot(ctx, root, prof, allowSecret)
}

func discoverRoot(ctx context.Context, root string, prof harness.Profile, allowSecret []string) ([]LocalFile, []Exclusion, error) {
	if err := statRoot(root); err != nil {
		return nil, nil, err
	}
	var out []LocalFile
	var skipped []Exclusion
	err := walkRoot(ctx, root, prof, allowSet(root, allowSecret), func(f visited) error {
		switch f.Reason {
		// Nothing here is fatal. The walk never reads a symlink's target
		// and never carries a flagged file's bytes, so those stay off the
		// server either way; refusing the walk only decided that every
		// other file stayed off it too. A shared skills directory behind
		// a symlink, a plugin's own test fixture, and a curl example in
		// a README the member wrote are all ordinary, and each of them
		// used to keep a whole profile off the server.
		case ExcludeSecret, ExcludeSymlink, ExcludeVendoredSecret, ExcludeTooLarge, ExcludeOverBudget:
			skipped = append(skipped, Exclusion{Path: f.Rel, Reason: f.Reason, Detail: f.Detail})
		case "":
			out = append(out, LocalFile{Path: f.Rel, AbsPath: f.Abs, Mode: f.Mode, Content: f.Content})
		}
		// Credential and ignored files are simply not pushed.
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, skipped, nil
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

// UnacknowledgedSecrets returns the scanner findings in files the member
// wrote that skipSecret does not name. Discovery already leaves those
// files out, so this exists only for a surface that has to hear the
// member say so first. The dashboard shows each finding on the harness
// row before the import button; the CLI has a single command and no
// screen to show first, so it refuses until --skip-secret names the file
// or --allow-secret carries it. Findings in vendored plugin content are
// not included: nobody can remove a secret-shaped string from a package
// the harness installed.
//
// root is the profile root the exclusions came from, so skipSecret takes
// the same spellings --allow-secret does: relative to the root, or
// absolute.
func UnacknowledgedSecrets(root string, skipped []Exclusion, skipSecret []string) []Exclusion {
	named := allowSet(root, skipSecret)
	var out []Exclusion
	for _, s := range skipped {
		if s.Reason == ExcludeSecret && !named[s.Path] {
			out = append(out, s)
		}
	}
	return out
}
