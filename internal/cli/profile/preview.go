package profile

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"
)

// The categories a preview groups files into, in the order it reports
// them. They exist so a developer can recognize what is about to be
// uploaded without reading a file list; they carry no meaning for the
// push itself, which uploads whatever Discover returns.
const (
	// CategoryMemory is the standing instructions an agent reads on every
	// run: CLAUDE.md, AGENTS.md, and anything under memory/.
	CategoryMemory = "memory"
	// CategorySkills is skills/.
	CategorySkills = "skills"
	// CategoryCommands is custom commands: commands/ and codex's prompts/.
	CategoryCommands = "commands"
	// CategorySettings is settings and config files.
	CategorySettings = "settings"
	// CategoryMCP is MCP server configuration.
	CategoryMCP = "mcp"
	// CategoryPlugins is plugins/.
	CategoryPlugins = "plugins"
	// CategoryOther is everything the rules above do not name.
	CategoryOther = "other"
)

// categoryOrder is the order previews and prompts list categories in.
var categoryOrder = []string{
	CategoryMemory,
	CategorySkills,
	CategoryCommands,
	CategorySettings,
	CategoryMCP,
	CategoryPlugins,
	CategoryOther,
}

// maxCategoryPaths bounds the path list one category carries. Counts and
// byte totals stay exact; a category that hit the cap says so, so neither
// a huge profile nor the prompt built from it grows without limit.
const maxCategoryPaths = 200

// memoryNames are the standing-instruction files agents read by name.
var memoryNames = map[string]bool{
	"claude.md": true,
	"agents.md": true,
	"agent.md":  true,
	"gemini.md": true,
	"memory.md": true,
}

// Category is one group of files a preview reports.
type Category struct {
	Name  string `json:"category"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	// Paths is the group's files, sorted, capped at maxCategoryPaths.
	Paths []string `json:"paths"`
	// Truncated says Paths was cut at the cap; Files is still exact.
	Truncated bool `json:"truncated,omitempty"`
}

// Exclusion is one file a push would leave behind, and why. Reason is one
// of the Exclude constants.
type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Preview is what a push of one harness profile would carry: the files
// grouped into categories, and everything the guards left out. Nothing is
// uploaded to produce it.
type Preview struct {
	Harness string `json:"harness"`
	Root    string `json:"root"`
	// Present is false when this machine has no profile root for the
	// harness at all - the normal answer for a harness the user does not
	// use, not an error.
	Present    bool        `json:"present"`
	Files      int         `json:"files"`
	Bytes      int64       `json:"bytes"`
	Categories []Category  `json:"categories"`
	Excluded   []Exclusion `json:"excluded"`
	// Blocked is true when a push of this profile would be refused rather
	// than partially carried. It covers every condition Discover aborts
	// on, so a preview can never promise files a push then refuses.
	Blocked bool `json:"blocked"`
	// BlockedReason is the Exclude constant behind Blocked, and
	// BlockedDetail the sentence for the user. Only a secret finding has
	// a CLI override, so a surface offering one keys off the reason
	// rather than assuming.
	BlockedReason string `json:"blocked_reason,omitempty"`
	BlockedPath   string `json:"blocked_path,omitempty"`
	BlockedDetail string `json:"blocked_detail,omitempty"`
}

// blocksPush reports whether an exclusion reason is one discoverRoot
// aborts on rather than skips. It is the single place the two agree, so
// adding an aborting reason to the walk cannot leave the preview behind.
func blocksPush(reason string) bool {
	return reason == ExcludeSecret || reason == ExcludeSymlink
}

// CategoryNames returns the categories the preview found files in, in
// report order.
func (p Preview) CategoryNames() []string {
	out := make([]string, 0, len(p.Categories))
	for _, c := range p.Categories {
		out = append(out, c.Name)
	}
	return out
}

// Inventory reports what `aether profile push --agent <harness>` would
// upload from this machine, and what it would leave behind, without
// uploading anything. It runs the same walk Discover runs, so the two can
// never disagree about the denylist, the ignore file, or the scanner; a
// finding is recorded here instead of aborting, because the point of a
// preview is to show the user the file they have to fix.
func Inventory(ctx context.Context, harnessName string) (Preview, error) {
	root, prof, err := LocalDir(harnessName)
	if err != nil {
		return Preview{}, err
	}
	preview := Preview{Harness: harnessName, Root: root, Excluded: []Exclusion{}, Categories: []Category{}}
	// A harness the user does not have on this machine is a normal
	// answer, not an error: the wizard lists every harness and marks this
	// one "nothing to import".
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return preview, nil
	}
	if statErr := statRoot(root); statErr != nil {
		return Preview{}, statErr
	}
	preview.Present = true
	groups := map[string]*Category{}
	walkErr := walkRoot(ctx, root, prof, nil, func(f visited) error {
		if f.Reason != "" {
			detail := f.Detail
			if f.Reason == ExcludeSecret && f.Finding.Location != "" {
				detail += " at " + f.Finding.Location
			}
			preview.Excluded = append(preview.Excluded, Exclusion{Path: f.Rel, Reason: f.Reason, Detail: detail})
			// Whatever aborts a push must block the preview, or the
			// preview promises files the push then refuses. The first
			// such finding is the one the user has to fix, so it is the
			// one named.
			if blocksPush(f.Reason) && !preview.Blocked {
				preview.Blocked = true
				preview.BlockedReason = f.Reason
				preview.BlockedPath = f.Rel
				preview.BlockedDetail = detail
			}
			return nil
		}
		name := Classify(f.Rel)
		group := groups[name]
		if group == nil {
			group = &Category{Name: name}
			groups[name] = group
		}
		group.Files++
		group.Bytes += f.Size
		if len(group.Paths) < maxCategoryPaths {
			group.Paths = append(group.Paths, f.Rel)
		} else {
			group.Truncated = true
		}
		preview.Files++
		preview.Bytes += f.Size
		return nil
	})
	if walkErr != nil {
		return Preview{}, walkErr
	}
	for _, name := range categoryOrder {
		if group := groups[name]; group != nil {
			sort.Strings(group.Paths)
			preview.Categories = append(preview.Categories, *group)
		}
	}
	sort.Slice(preview.Excluded, func(i, j int) bool { return preview.Excluded[i].Path < preview.Excluded[j].Path })
	return preview, nil
}

// Classify names the category a profile-relative path belongs to. The
// first path segment decides for the directory-shaped categories; the
// basename decides for the loose files at a profile root.
func Classify(rel string) string {
	rel = path.Clean(rel)
	segments := strings.Split(rel, "/")
	if len(segments) > 1 {
		switch strings.ToLower(segments[0]) {
		case "skills":
			return CategorySkills
		case "commands", "prompts":
			return CategoryCommands
		case "plugins":
			return CategoryPlugins
		case "memory", "memories":
			return CategoryMemory
		case "mcp":
			return CategoryMCP
		}
	}
	base := strings.ToLower(path.Base(rel))
	switch {
	case memoryNames[base]:
		return CategoryMemory
	case strings.Contains(base, "mcp"):
		return CategoryMCP
	case strings.HasPrefix(base, "settings.") || strings.HasPrefix(base, "config."):
		return CategorySettings
	default:
		return CategoryOther
	}
}
