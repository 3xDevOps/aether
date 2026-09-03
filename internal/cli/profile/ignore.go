package profile

import (
	"path"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// IgnoreFileName is the gitignore-style exclude file at a profile root.
const IgnoreFileName = ".aether-profile-ignore"

type ignoreMatcher struct {
	gi *ignore.GitIgnore
}

func parseIgnoreFile(data []byte) *ignoreMatcher {
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return &ignoreMatcher{}
	}
	return &ignoreMatcher{gi: ignore.CompileIgnoreLines(lines...)}
}

func (m *ignoreMatcher) ignored(rel string) bool {
	if m == nil || m.gi == nil || rel == "" || rel == "." {
		return false
	}
	return m.gi.MatchesPath(path.Clean(rel))
}

// ignoredDir reports whether a directory is excluded. A `projects/`
// pattern is directory-only, and the matcher cannot tell a directory from
// a file by its path alone, so the trailing slash is supplied here.
// Matching the directory lets the walk skip the whole subtree and report
// it as one entry, instead of walking it to produce one entry per file.
func (m *ignoreMatcher) ignoredDir(rel string) bool {
	if m == nil || m.gi == nil || rel == "" || rel == "." {
		return false
	}
	clean := path.Clean(rel)
	return m.gi.MatchesPath(clean) || m.gi.MatchesPath(clean+"/")
}
