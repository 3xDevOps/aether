package profile

import (
	"context"
	"fmt"
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
	// ExcludeTooLarge is a file over the server's per-file cap. It is
	// never read, so its content is never scanned either - which is what
	// keeps a profile root full of multi-megabyte transcripts from
	// costing minutes of regex time to preview.
	ExcludeTooLarge = "too-large"
	// ExcludeOverBudget is a file left out because the files before it
	// already filled the server's per-snapshot cap.
	ExcludeOverBudget = "over-budget"
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
// escape rejection, size caps, content scan - and hands every file to
// visit with its verdict. allowed names files whose scanner findings pass
// (the CLI's --allow-secret); Inventory passes none. A visit error aborts
// the walk and is returned unchanged, which is how Discover stops at the
// first blocking finding.
//
// The walk checks ctx between entries: a profile root can hold thousands
// of files, so a caller that has gone away - a closed HTTP request, a
// closed scan socket - must be able to stop the work rather than leave it
// running against the user's home directory.
func walkRoot(ctx context.Context, root string, prof harness.Profile, allowed map[string]bool, visit func(visited) error) error {
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
	// total is the size of everything the walk has accepted so far, so a
	// tree that would blow the per-snapshot cap stops costing time (and
	// promising files) at the point the server would stop accepting them.
	var total int64
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
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
		// The size caps are applied from the stat, before the file is
		// opened. The server refuses these files anyway, so reading one
		// buys nothing - and reading it would also hand it to the secret
		// scanner, whose cost grows sharply with size. A profile root
		// holding large agent transcripts is ordinary, so this is the
		// difference between a preview that answers and one that hangs.
		if info.Size() > profilesvc.MaxFileBytes {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeTooLarge,
				Detail: fmt.Sprintf("%d bytes, over the %d-byte limit for one file", info.Size(), profilesvc.MaxFileBytes)})
		}
		if total+info.Size() > profilesvc.MaxTotalBytes {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeOverBudget,
				Detail: fmt.Sprintf("the %d-byte limit for one snapshot was already reached", profilesvc.MaxTotalBytes)})
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += info.Size()
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
