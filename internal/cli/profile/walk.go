package profile

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
)

// Why a file was left out of a push. Discover turns secret and symlink
// into a blocking error; Inventory reports every reason to the user.
const (
	// ExcludeCredential is a basename on the harness credential denylist.
	ExcludeCredential = "credential"
	// ExcludeSecret is a content-scanner finding in a file the user
	// wrote. It refuses the whole push: the fix is on this machine.
	ExcludeSecret = "secret"
	// ExcludeVendoredSecret is a content-scanner finding inside one of
	// the harness's vendoredRoots - third-party content the user did not
	// write and cannot edit. That one file is dropped and reported; the
	// rest of the profile still syncs.
	ExcludeVendoredSecret = "vendored-secret"
	// ExcludeIgnored is a .aether-profile-ignore match, or one of the
	// per-harness defaults below.
	ExcludeIgnored = "ignored"
	// ExcludeSymlink is a symlink pointing outside the profile root.
	ExcludeSymlink = "symlink"
	// ExcludeTooLarge is a file over the server's per-file cap. It is
	// never read, so its content is never scanned either - which is what
	// keeps a profile root full of multi-megabyte transcripts from
	// costing minutes of regex time to preview.
	ExcludeTooLarge = "too-large"
	// ExcludeOverBudget is a file left out because higher-priority files
	// already filled the server's per-snapshot cap.
	ExcludeOverBudget = "over-budget"
	// ExcludeNotRegular is a socket, FIFO, or device node. Opening one is
	// not merely useless: a FIFO blocks until something writes to it, and
	// nothing can interrupt a blocked read, so it is refused on its mode.
	ExcludeNotRegular = "not-regular"
)

// defaultIgnores are the gitignore-style patterns Aether applies to a
// harness before the user's own .aether-profile-ignore. These are the
// paths a harness writes as it runs - transcripts, shell snapshots,
// telemetry, scratch trees - rather than anything the user configured.
// They are large enough to spend the whole per-snapshot budget, none of
// it is configuration another machine wants, and some of it is actively
// hostile to a walk: codex plants a symlink to its own installed binary
// under tmp/ on every run.
//
// They come first, so a user's ignore file overrides them the way
// gitignore does: a later `!projects/` re-includes what this dropped.
var defaultIgnores = map[string][]string{
	"claude": {
		"projects/", "shell-snapshots/", "statsig/", "todos/",
		"file-history/", "history.jsonl", "daemon/",
	},
	// codex/tmp holds a per-run scratch directory whose apply_patch entry
	// is a symlink out to the codex binary; sessions/ is codex's own
	// transcript archive, the same thing claude keeps in projects/.
	// claude's plugins/cache is deliberately in neither list: it is real
	// third-party content a user may want on the server. vendoredRoots
	// below covers it instead.
	"codex": {"tmp/", ".tmp/", "sessions/"},
}

// vendoredRoots are the directories inside a harness profile root that
// hold third-party content: packages a harness installed from a
// marketplace, not files the user wrote. They still sync - an installed
// plugin is configuration the server needs - but a scanner finding
// inside one is not a secret anybody can remove locally, so it drops
// that one file instead of refusing the whole push.
//
// claude installs a marketplace plugin at
// plugins/cache/<marketplace>/<plugin>/<version>/, and plugins ship
// their own test suites. A fixture holding a secret-shaped string used
// to block the entire import until the user passed --allow-secret with a
// path carrying the plugin version - an override the next update of that
// plugin invalidated, because the version segment had moved.
var vendoredRoots = map[string][]string{
	"claude": {"plugins/cache/"},
}

// isVendored reports whether a profile-relative path sits under one of
// the harness's vendored roots. It matches on the root prefix alone, so
// it holds across a plugin version bump, a new plugin, and a new
// marketplace without needing an entry per plugin.
func isVendored(harnessName, rel string) bool {
	clean := path.Clean(rel) + "/"
	for _, prefix := range vendoredRoots[harnessName] {
		if strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// visited is one entry the walk classified. Reason is empty for a file
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
	// Finding carries the scanner hit behind an ExcludeSecret or an
	// ExcludeVendoredSecret, so callers can name the file and the
	// location.
	Finding Finding
}

// candidate is a file that survived pass one: everything about it is
// known except its content.
type candidate struct {
	rel      string
	abs      string
	mode     uint32
	size     int64
	priority int
}

// walkRoot walks a harness profile root once, applying the same rules a
// push applies - default and user ignores, credential denylist, symlink
// escape rejection, regular-file check, size caps, content scan - and
// hands every entry to visit with its verdict. allowed names files whose
// scanner findings pass (the CLI's --allow-secret); Inventory passes
// none. A visit error aborts the walk and is returned unchanged, which is
// how Discover stops at the first blocking finding.
//
// It runs in two passes. The first stats and classifies the tree without
// opening anything. The second reads what survived, in category priority
// order, so the per-snapshot budget is spent on the configuration this
// feature exists to carry - memory, skills, commands - rather than on
// whatever a directory listing happens to put first.
//
// The walk checks ctx between entries: a profile root can hold thousands
// of files, so a caller that has gone away - a closed HTTP request, a
// closed scan socket - must be able to stop the work rather than leave it
// running against the user's home directory.
func walkRoot(ctx context.Context, root string, prof harness.Profile, allowed map[string]bool, visit func(visited) error) error {
	matcher, err := rootMatcher(root, prof.Name)
	if err != nil {
		return err
	}
	candidates, err := classifyRoot(ctx, root, prof, matcher, visit)
	if err != nil {
		return err
	}
	return readCandidates(ctx, prof.Name, candidates, allowed, visit)
}

// rootMatcher compiles the harness defaults followed by the user's own
// .aether-profile-ignore, so the user's file has the last word.
func rootMatcher(root, harnessName string) (*ignoreMatcher, error) {
	lines := append([]string(nil), defaultIgnores[harnessName]...)
	data, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	switch {
	case err == nil:
		lines = append(lines, strings.Split(string(data), "\n")...)
	case !os.IsNotExist(err):
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return parseIgnoreFile([]byte(strings.Join(lines, "\n"))), nil
}

// classifyRoot is pass one: it stats every entry, reports the ones no
// push would carry, and returns the rest for pass two. Nothing is opened
// here, so a tree of transcripts costs one stat each.
func classifyRoot(ctx context.Context, root string, prof harness.Profile, matcher *ignoreMatcher, visit func(visited) error) ([]candidate, error) {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootResolved = root
	}
	var out []candidate
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
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
			if matcher != nil && matcher.ignoredDir(relSlash) {
				// One entry for the directory rather than one per file
				// inside it: the user needs to know the directory was
				// dropped, not to scroll a transcript archive.
				if err := visit(visited{Rel: relSlash, Abs: path, Reason: ExcludeIgnored, Detail: ignoreDetail(prof.Name, relSlash)}); err != nil {
					return err
				}
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Base(relSlash) == IgnoreFileName {
			return nil
		}
		// A socket, FIFO, or device node is refused on its mode alone.
		// os.ReadFile on a FIFO blocks until a writer appears, and the
		// context check above runs only between entries, so one of these
		// would hang the walk for good on an fd nothing can reclaim.
		if !info.Mode().IsRegular() {
			return visit(visited{Rel: relSlash, Abs: path, Reason: ExcludeNotRegular,
				Detail: "not a regular file (" + irregularKind(info.Mode()) + ")"})
		}
		if profilesvc.DeniedBasename(relSlash, prof.DenyNames) {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeCredential, Detail: "credential file excluded for " + prof.Name})
		}
		if matcher != nil && matcher.ignored(relSlash) {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeIgnored, Detail: ignoreDetail(prof.Name, relSlash)})
		}
		// The per-file cap is applied from the stat, before the file is
		// opened. The server refuses these files anyway, so reading one
		// buys nothing - and reading it would also hand it to the secret
		// scanner, whose cost grows sharply with size. A profile root
		// holding large agent transcripts is ordinary, so this is the
		// difference between a preview that answers and one that hangs.
		if info.Size() > profilesvc.MaxFileBytes {
			return visit(visited{Rel: relSlash, Abs: path, Size: info.Size(), Reason: ExcludeTooLarge,
				Detail: fmt.Sprintf("%d bytes, over the %d-byte limit for one file", info.Size(), profilesvc.MaxFileBytes)})
		}
		out = append(out, candidate{
			rel:      relSlash,
			abs:      path,
			mode:     uint32(info.Mode().Perm()),
			size:     info.Size(),
			priority: categoryRank(Classify(relSlash)),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// readCandidates is pass two: it reads and scans what pass one kept, in
// category priority order, spending the per-snapshot budget on the
// highest-priority files first. Directory order would otherwise decide
// it, and on a real profile root that means transcripts and plugin
// caches crowd out every skill and command the user actually wrote.
func readCandidates(ctx context.Context, harnessName string, candidates []candidate, allowed map[string]bool, visit func(visited) error) error {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].rel < candidates[j].rel
	})
	var total int64
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if total+c.size > profilesvc.MaxTotalBytes {
			err := visit(visited{Rel: c.rel, Abs: c.abs, Size: c.size, Reason: ExcludeOverBudget,
				Detail: fmt.Sprintf("the %d-byte limit for one snapshot was already reached", profilesvc.MaxTotalBytes)})
			if err != nil {
				return err
			}
			continue
		}
		content, err := os.ReadFile(c.abs)
		if err != nil {
			return err
		}
		total += c.size
		file := visited{Rel: c.rel, Abs: c.abs, Mode: c.mode, Size: c.size, Content: content}
		if findings := scanContent(c.rel, content); len(findings) > 0 && !allowed[c.rel] && !allowed[c.abs] {
			file.Content = nil
			file.Finding = findings[0]
			file.Reason, file.Detail = findingVerdict(harnessName, c.rel, findings[0])
		}
		if err := visit(file); err != nil {
			return err
		}
	}
	return nil
}

// findingVerdict decides what a scanner hit means for the push. In a
// file the user wrote it is a secret to remove, and it refuses the push.
// In vendored third-party content it is a string inside a package the
// user installed: that file is dropped, so its bytes still never leave
// the machine, and the push carries everything else.
func findingVerdict(harnessName, rel string, finding Finding) (string, string) {
	if isVendored(harnessName, rel) {
		return ExcludeVendoredSecret, "secret detected (" + finding.Kind +
			") in third-party plugin content; this file is left out and the rest of the profile still syncs"
	}
	return ExcludeSecret, "secret detected (" + finding.Kind + ")"
}

// categoryRank orders the categories the budget is spent on: the
// configuration a developer wrote first, the caches a harness generated
// last. It follows categoryOrder, so adding a category in one place
// cannot leave the other behind.
func categoryRank(category string) int {
	for i, name := range categoryOrder {
		if name == category {
			return i
		}
	}
	return len(categoryOrder)
}

// ignoreDetail says which rule dropped a path: one Aether ships for this
// harness, or the user's own file. They are compiled together, so the
// answer is whether a default pattern names this path at all.
func ignoreDetail(harnessName, rel string) string {
	for _, pattern := range defaultIgnores[harnessName] {
		if strings.HasPrefix(rel+"/", strings.TrimSuffix(pattern, "/")+"/") {
			return "skipped by default for " + harnessName + " (" + pattern +
				"); add !" + pattern + " to " + IgnoreFileName + " to include it"
		}
	}
	return IgnoreFileName
}

// irregularKind names the file type for the exclusion message, so the
// user can tell a stray socket from a device node.
func irregularKind(mode os.FileMode) string {
	switch {
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeDevice != 0:
		return "device"
	default:
		return "irregular"
	}
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
