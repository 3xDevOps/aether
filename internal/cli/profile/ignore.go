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
